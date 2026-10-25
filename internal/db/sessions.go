package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DefaultProjectName is used whenever a project name is blank.
const DefaultProjectName = "Focus"

// execQuerier is satisfied by both *sql.DB and *sql.Tx.
type execQuerier interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

func normalizeProjectName(name string) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return trimmed
	}
	return DefaultProjectName
}

// GetOrCreateProject returns the project with this name (case-insensitive),
// creating it with the next palette color if needed.
func (d *DB) GetOrCreateProject(name string) (*Project, error) {
	return getOrCreateProject(d.conn, name)
}

func getOrCreateProject(q execQuerier, name string) (*Project, error) {
	name = normalizeProjectName(name)
	_, err := q.Exec(`
		INSERT INTO projects (name, color, color_index, created_at)
		SELECT ?, '', COALESCE(MAX(color_index) + 1, 0), ? FROM projects WHERE true
		ON CONFLICT(name) DO NOTHING`,
		name, formatTime(time.Now()))
	if err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}

	var p Project
	var createdAt string
	err = q.QueryRow(
		`SELECT id, name, color, color_index, created_at FROM projects WHERE name = ?`, name,
	).Scan(&p.ID, &p.Name, &p.Color, &p.ColorIndex, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("read project: %w", err)
	}
	p.CreatedAt = parseTime(createdAt)

	// A freshly inserted project gets its color from its index.
	if p.Color == "" {
		p.Color = RosePinePalette[p.ColorIndex%len(RosePinePalette)]
		if _, err := q.Exec(`UPDATE projects SET color = ? WHERE id = ?`, p.Color, p.ID); err != nil {
			return nil, fmt.Errorf("set project color: %w", err)
		}
	}
	return &p, nil
}

func (d *DB) queryProjects(query string, args ...any) ([]Project, error) {
	rows, err := d.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	projects := []Project{}
	for rows.Next() {
		var p Project
		var createdAt string
		if err := rows.Scan(&p.ID, &p.Name, &p.Color, &p.ColorIndex, &createdAt); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTime(createdAt)
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// ListProjects returns every project, sorted by name.
func (d *DB) ListProjects() ([]Project, error) {
	return d.queryProjects(`SELECT id, name, color, color_index, created_at FROM projects ORDER BY name`)
}

// GetMonthProjects returns the projects having at least one session in the month.
func (d *DB) GetMonthProjects(year, month int) ([]Project, error) {
	from, to := monthBounds(year, month)
	return d.queryProjects(`
		SELECT p.id, p.name, p.color, p.color_index, p.created_at
		FROM projects p
		WHERE EXISTS (
			SELECT 1 FROM sessions s
			WHERE s.project_name = p.name AND s.start_time < ? AND s.end_time > ?
		)
		ORDER BY p.name`,
		formatTime(to), formatTime(from))
}

// GetLastProject returns the project of the most recent session, or of the
// most recently created project, or DefaultProjectName.
func (d *DB) GetLastProject() (string, error) {
	var name string
	err := d.conn.QueryRow(`SELECT project_name FROM sessions ORDER BY start_time DESC LIMIT 1`).Scan(&name)
	if err == nil && name != "" {
		return name, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	err = d.conn.QueryRow(`SELECT name FROM projects ORDER BY id DESC LIMIT 1`).Scan(&name)
	if err == nil && name != "" {
		return name, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return DefaultProjectName, nil
}

// RecordSession inserts a session, or updates the one sharing its UUID.
//
// The stored interval always satisfies end - start = duration: when a
// positive duration is given the start time is derived from the end time
// (pauses are not part of a session), otherwise the duration is derived from
// the interval, with a minimum of one second.
func (d *DB) RecordSession(s Session) (*Session, error) {
	if s.UUID == "" {
		s.UUID = GenerateUUID()
	}
	if s.Mode != ModeFree {
		s.Mode = ModePomodoro
	}
	p, err := d.GetOrCreateProject(s.ProjectName)
	if err != nil {
		return nil, err
	}
	s.ProjectName = p.Name
	s.ProjectColor = p.Color

	if s.DurationSeconds <= 0 {
		s.DurationSeconds = max(int(s.EndTime.Sub(s.StartTime).Round(time.Second)/time.Second), 1)
	}
	if err := checkDuration(s.DurationSeconds); err != nil {
		return nil, err
	}
	s.EndTime = s.EndTime.Truncate(time.Second)
	s.StartTime = s.EndTime.Add(-time.Duration(s.DurationSeconds) * time.Second)
	s.UpdatedAt = time.Now().Truncate(time.Second)

	err = d.conn.QueryRow(`
		INSERT INTO sessions (uuid, project_name, mode, start_time, end_time, duration_seconds, completed, note, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(uuid) DO UPDATE SET
			project_name = excluded.project_name,
			mode = excluded.mode,
			start_time = excluded.start_time,
			end_time = excluded.end_time,
			duration_seconds = excluded.duration_seconds,
			completed = excluded.completed,
			note = excluded.note,
			updated_at = excluded.updated_at
		RETURNING id`,
		s.UUID, s.ProjectName, s.Mode, formatTime(s.StartTime), formatTime(s.EndTime),
		s.DurationSeconds, s.Completed, s.Note, formatTime(s.UpdatedAt),
	).Scan(&s.ID)
	if err != nil {
		return nil, fmt.Errorf("record session: %w", err)
	}
	s.StartTime = s.StartTime.Local()
	s.EndTime = s.EndTime.Local()
	return &s, nil
}

func checkDuration(seconds int) error {
	if seconds > int(MaxSessionDuration/time.Second) { // compared in seconds: no overflow
		return fmt.Errorf("%w: a session cannot last more than %v", ErrInvalidSession, MaxSessionDuration)
	}
	return nil
}

// UpdateSession changes the project and interval of an existing session.
func (d *DB) UpdateSession(id int64, projectName string, startTime, endTime time.Time) (*Session, error) {
	p, err := d.GetOrCreateProject(projectName)
	if err != nil {
		return nil, err
	}
	startTime = startTime.Truncate(time.Second)
	endTime = endTime.Truncate(time.Second)
	duration := max(int(endTime.Sub(startTime).Seconds()), 1)
	if err := checkDuration(duration); err != nil {
		return nil, err
	}
	endTime = startTime.Add(time.Duration(duration) * time.Second)

	res, err := d.conn.Exec(
		`UPDATE sessions SET project_name = ?, start_time = ?, end_time = ?, duration_seconds = ?, updated_at = ? WHERE id = ?`,
		p.Name, formatTime(startTime), formatTime(endTime), duration, formatTime(time.Now()), id,
	)
	if err != nil {
		return nil, fmt.Errorf("update session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return d.GetSession(id)
}

// DeleteSession deletes a session by numeric ID or UUID.
func (d *DB) DeleteSession(idOrUUID string) error {
	res, err := d.conn.Exec(`DELETE FROM sessions WHERE CAST(id AS TEXT) = ? OR uuid = ?`, idOrUUID, idOrUUID)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

const sessionColumns = `
	s.id, COALESCE(s.uuid, ''), s.project_name, COALESCE(p.color, ''), s.mode,
	s.start_time, s.end_time, s.duration_seconds, s.completed, COALESCE(s.note, ''),
	COALESCE(s.updated_at, '')
	FROM sessions s
	LEFT JOIN projects p ON p.name = s.project_name`

func scanSession(scan func(dest ...any) error) (Session, error) {
	var s Session
	var start, end, updated string
	err := scan(&s.ID, &s.UUID, &s.ProjectName, &s.ProjectColor, &s.Mode,
		&start, &end, &s.DurationSeconds, &s.Completed, &s.Note, &updated)
	if err != nil {
		return s, err
	}
	s.StartTime = parseTime(start)
	s.EndTime = parseTime(end)
	s.UpdatedAt = parseTime(updated)
	if s.ProjectColor == "" {
		s.ProjectColor = RosePinePalette[0]
	}
	return s, nil
}

func (d *DB) querySessions(where string, args ...any) ([]Session, error) {
	rows, err := d.conn.Query(`SELECT `+sessionColumns+` `+where+` ORDER BY s.start_time`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := []Session{}
	for rows.Next() {
		s, err := scanSession(rows.Scan)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// GetSession returns a single session by ID.
func (d *DB) GetSession(id int64) (*Session, error) {
	s, err := scanSession(d.conn.QueryRow(`SELECT `+sessionColumns+` WHERE s.id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// GetAllSessions returns every session, oldest first.
func (d *DB) GetAllSessions() ([]Session, error) {
	return d.querySessions(``)
}

// SessionsOverlapping returns the sessions intersecting [from, to), oldest first.
func (d *DB) SessionsOverlapping(from, to time.Time) ([]Session, error) {
	return d.querySessions(`WHERE s.start_time < ? AND s.end_time > ?`, formatTime(to), formatTime(from))
}

// GetTodaySessions returns the sessions intersecting the current local day.
func (d *DB) GetTodaySessions() ([]Session, error) {
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	return d.SessionsOverlapping(from, from.AddDate(0, 0, 1))
}

// RenameProjectForMonth moves the sessions of oldName intersecting the given
// month to newName, and returns the resulting project name.
func (d *DB) RenameProjectForMonth(year, month int, oldName, newName string) (string, error) {
	from, to := monthBounds(year, month)
	return d.renameProject(oldName, newName, from, to)
}

// RenameProject moves every session of oldName to newName, and returns the
// resulting project name. Renaming to an existing project merges both.
func (d *DB) RenameProject(oldName, newName string) (string, error) {
	return d.renameProject(oldName, newName, time.Time{}, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))
}

// renameProject moves the sessions of oldName intersecting [from, to) to
// newName. A change of case only renames the project itself (names are
// case-insensitive, so it applies everywhere). The old project is deleted
// once no session uses it anymore.
func (d *DB) renameProject(oldName, newName string, from, to time.Time) (string, error) {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" || newName == "" {
		return "", fmt.Errorf("project name cannot be empty")
	}

	tx, err := d.conn.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := formatTime(time.Now())

	if strings.EqualFold(oldName, newName) {
		if _, err := tx.Exec(`UPDATE projects SET name = ? WHERE name = ?`, newName, oldName); err != nil {
			return "", fmt.Errorf("rename project: %w", err)
		}
		if _, err := tx.Exec(`UPDATE sessions SET project_name = ?, updated_at = ? WHERE project_name = ?`,
			newName, now, oldName); err != nil {
			return "", fmt.Errorf("rename sessions: %w", err)
		}
		return newName, tx.Commit()
	}

	p, err := getOrCreateProject(tx, newName)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(`
		UPDATE sessions SET project_name = ?, updated_at = ?
		WHERE project_name = ? AND start_time < ? AND end_time > ?`,
		p.Name, now, oldName, formatTime(to), formatTime(from))
	if err != nil {
		return "", fmt.Errorf("rename sessions: %w", err)
	}
	_, err = tx.Exec(`DELETE FROM projects WHERE name = ?
		AND NOT EXISTS (SELECT 1 FROM sessions WHERE sessions.project_name = projects.name)`, oldName)
	if err != nil {
		return "", fmt.Errorf("delete unused project: %w", err)
	}
	return p.Name, tx.Commit()
}

// monthBounds returns the local [first day, first day of next month) interval.
func monthBounds(year, month int) (time.Time, time.Time) {
	from := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
	return from, from.AddDate(0, 1, 0)
}
