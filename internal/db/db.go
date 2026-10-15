package db

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Rose Pine palette colors
var RosePinePalette = []string{
	"#eb6f92", // Love
	"#f6c177", // Gold
	"#ebbcba", // Rose
	"#31748f", // Pine
	"#9ccfd8", // Foam
	"#c4a7e7", // Iris
	"#ea9a97", // Accent Love 2
	"#56949f", // Accent Foam 2
	"#b4637a", // Accent Rose 2
	"#ea9d34", // Accent Gold 2
}

type Project struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Color      string    `json:"color"`
	ColorIndex int       `json:"color_index"`
	CreatedAt  time.Time `json:"created_at"`
}

type Session struct {
	ID              int64     `json:"id"`
	UUID            string    `json:"uuid"`
	ProjectName     string    `json:"project_name"`
	ProjectColor    string    `json:"project_color"`
	Mode            string    `json:"mode"` // "pomodoro", "free"
	StartTime       time.Time `json:"start_time"`
	EndTime         time.Time `json:"end_time"`
	DurationSeconds int       `json:"duration_seconds"`
	Completed       bool      `json:"completed"`
	Note            string    `json:"note"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}


type DaySummary struct {
	Date            string    `json:"date"` // YYYY-MM-DD
	DayOfWeek       int       `json:"day_of_week"`
	DayName         string    `json:"day_name"`
	DayNumber       int       `json:"day_number"`
	WeekNumber      int       `json:"week_number"`
	TotalSeconds    int       `json:"total_seconds"`
	Sessions        []Session `json:"sessions"`
}

type MonthData struct {
	Year         int          `json:"year"`
	Month        int          `json:"month"`
	MonthName    string       `json:"month_name"`
	TotalSeconds int          `json:"total_seconds"`
	Days         []DaySummary `json:"days"`
	Projects     []Project    `json:"projects"`
}

type ProjectStat struct {
	ProjectName     string  `json:"project_name"`
	Color           string  `json:"color"`
	TotalSeconds    int     `json:"total_seconds"`
	SessionCount    int     `json:"session_count"`
	Percentage      float64 `json:"percentage"`
	AverageDuration int     `json:"average_duration_seconds"`
}

type Stats struct {
	TotalSeconds        int           `json:"total_seconds"`
	SessionCount        int           `json:"session_count"`
	ActiveDaysCount     int           `json:"active_days_count"`
	AveragePerDay       int           `json:"average_per_day_seconds"`
	PomodoroCount       int           `json:"pomodoro_count"`
	FreeCount           int           `json:"free_count"`
	Projects            []ProjectStat `json:"projects"`
	YearlyBreakdown     []YearlyBar   `json:"yearly_breakdown"`
	MonthlyBreakdown    []MonthlyBar  `json:"monthly_breakdown"`
	WeekdayDistribution []int         `json:"weekday_distribution"` // 7 days (Mon-Sun), total seconds
	HourlyDistribution  []int         `json:"hourly_distribution"`  // 24 hours (0-23), total seconds
}

type YearlyBar struct {
	Year         int    `json:"year"`
	Label        string `json:"label"` // e.g. "2026"
	TotalSeconds int    `json:"total_seconds"`
}

type MonthlyBar struct {
	Label        string `json:"label"` // e.g. "2026-10" or "Oct 2026"
	TotalSeconds int    `json:"total_seconds"`
}

type DB struct {
	conn *sql.DB
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func Open(dbPath string) (*DB, error) {
	var legacyDb string
	home, _ := os.UserHomeDir()

	if dbPath == "" {
		if home == "" {
			return nil, fmt.Errorf("user home directory not found")
		}
		dataDir := filepath.Join(home, ".local", "share", "focus")
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			return nil, err
		}
		dbPath = filepath.Join(dataDir, "focus.db")
		legacyDb = filepath.Join(home, ".local", "share", "timetracker", "timetracker.db")
	} else {
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
		// If dbPath is .../focus.db, check for timetracker.db in the same directory
		// If dbPath is .../focus.db, check for timetracker.db in the same directory
		if filepath.Base(dbPath) == "focus.db" {
			legacyDb = filepath.Join(dir, "timetracker.db")
		}
	}

	// 1. If target dbPath does not exist and legacyDb exists, copy it over!
	if legacyDb != "" && dbPath != legacyDb {
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			if _, errLegacy := os.Stat(legacyDb); errLegacy == nil {
				_ = copyFile(legacyDb, dbPath)
				_ = copyFile(legacyDb+"-wal", dbPath+"-wal")
				_ = copyFile(legacyDb+"-shm", dbPath+"-shm")
			}
		}
	}

	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// Optimize SQLite performance & concurrency
	if _, err := conn.Exec(`
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = NORMAL;
		PRAGMA busy_timeout = 5000;
	`); err != nil {
		conn.Close()
		return nil, err
	}

	database := &DB{conn: conn}
	if err := database.initSchema(); err != nil {
		conn.Close()
		return nil, err
	}

	// 2. If focus.db was created empty (0 sessions) but legacyDb exists and has sessions, auto-restore!
	if legacyDb != "" && dbPath != legacyDb {
		var sessionCount int
		_ = database.conn.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessionCount)
		if sessionCount == 0 {
			if legacyConn, errLeg := sql.Open("sqlite", legacyDb); errLeg == nil {
				var legacyCount int
				_ = legacyConn.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&legacyCount)
				legacyConn.Close()
				if legacyCount > 0 {
					_ = database.Close()
					_ = copyFile(legacyDb, dbPath)
					_ = copyFile(legacyDb+"-wal", dbPath+"-wal")
					_ = copyFile(legacyDb+"-shm", dbPath+"-shm")

					conn, err = sql.Open("sqlite", dbPath)
					if err != nil {
						return nil, err
					}
					database = &DB{conn: conn}
					_ = database.initSchema()
				}
			}
		}
	}

	return database, nil
}

func (d *DB) Close() error {
	return d.conn.Close()
}

func GenerateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (d *DB) initSchema() error {
	tableQueries := []string{
		`CREATE TABLE IF NOT EXISTS projects (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL COLLATE NOCASE,
			color TEXT NOT NULL,
			color_index INTEGER NOT NULL,
			created_at DATETIME NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uuid TEXT UNIQUE,
			project_name TEXT NOT NULL COLLATE NOCASE,
			mode TEXT NOT NULL,
			start_time DATETIME NOT NULL,
			end_time DATETIME NOT NULL,
			duration_seconds INTEGER NOT NULL,
			completed INTEGER NOT NULL DEFAULT 1,
			note TEXT DEFAULT '',
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS auth_sessions (
			token TEXT PRIMARY KEY,
			created_at DATETIME NOT NULL,
			expires_at DATETIME NOT NULL
		);`,
	}

	for _, q := range tableQueries {
		if _, err := d.conn.Exec(q); err != nil {
			return fmt.Errorf("init schema error on query %q: %w", q, err)
		}
	}

	// Schema migrations for pre-existing databases
	rows, err := d.conn.Query(`PRAGMA table_info(sessions)`)
	if err != nil {
		return fmt.Errorf("failed to inspect sessions table: %w", err)
	}
	hasUUID := false
	hasUpdatedAt := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notnull, &dfltValue, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan session column info: %w", err)
		}
		if strings.EqualFold(name, "uuid") {
			hasUUID = true
		} else if strings.EqualFold(name, "updated_at") {
			hasUpdatedAt = true
		}
	}
	rows.Close()

	if !hasUUID {
		if _, err := d.conn.Exec(`ALTER TABLE sessions ADD COLUMN uuid TEXT;`); err != nil {
			return fmt.Errorf("failed to add uuid column: %w", err)
		}
	}
	if !hasUpdatedAt {
		if _, err := d.conn.Exec(`ALTER TABLE sessions ADD COLUMN updated_at DATETIME;`); err != nil {
			return fmt.Errorf("failed to add updated_at column: %w", err)
		}
	}

	if _, err := d.conn.Exec(`UPDATE sessions SET updated_at = CURRENT_TIMESTAMP WHERE updated_at IS NULL;`); err != nil {
		return fmt.Errorf("failed to update null updated_at: %w", err)
	}

	indexQueries := []string{
		`CREATE INDEX IF NOT EXISTS idx_sessions_start_time ON sessions(start_time);`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_project_name ON sessions(project_name);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_uuid ON sessions(uuid);`,
		`CREATE INDEX IF NOT EXISTS idx_auth_sessions_expires ON auth_sessions(expires_at);`,
	}

	for _, q := range indexQueries {
		if _, err := d.conn.Exec(q); err != nil {
			return fmt.Errorf("init index error on query %q: %w", q, err)
		}
	}

	// Backfill UUIDs for legacy rows without a UUID
	if rows, err := d.conn.Query(`SELECT id FROM sessions WHERE uuid IS NULL OR uuid = ''`); err == nil {
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			newUUID := GenerateUUID()
			_, _ = d.conn.Exec(`UPDATE sessions SET uuid = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, newUUID, id)
		}
	}

	// Default settings
	defaultSettings := map[string]string{
		"work_duration":         "60",
		"break_duration":        "10",
		"alarm_sound":           os.Getenv("HOME") + "/.config/deepwork-alarm.mp3",
		"alarm_enabled":         "true",
		"notifications_enabled": "true",
		"theme":                 "dark",
	}

	for k, v := range defaultSettings {
		_, _ = d.conn.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)`, k, v)
	}

	return nil
}

func (d *DB) GetOrCreateProject(name string) (*Project, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		trimmed = "Default"
	}

	var p Project
	var createdAtStr string
	err := d.conn.QueryRow(
		`SELECT id, name, color, color_index, created_at FROM projects WHERE name = ? COLLATE NOCASE`,
		trimmed,
	).Scan(&p.ID, &p.Name, &p.Color, &p.ColorIndex, &createdAtStr)

	if err == nil {
		p.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		return &p, nil
	} else if err != sql.ErrNoRows {
		return nil, err
	}

	// Count existing projects to pick next color
	var count int
	if err := d.conn.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&count); err != nil {
		count = 0
	}

	colorIndex := count
	color := RosePinePalette[colorIndex%len(RosePinePalette)]
	now := time.Now().UTC()

	res, err := d.conn.Exec(
		`INSERT INTO projects (name, color, color_index, created_at) VALUES (?, ?, ?, ?)`,
		trimmed, color, colorIndex, now.Format(time.RFC3339),
	)
	if err != nil {
		// Concurrent insert could fail UNIQUE check, re-query
		return d.GetOrCreateProject(trimmed)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	return &Project{
		ID:         id,
		Name:       trimmed,
		Color:      color,
		ColorIndex: colorIndex,
		CreatedAt:  now,
	}, nil
}

func (d *DB) ListProjects() ([]Project, error) {
	rows, err := d.conn.Query(`SELECT id, name, color, color_index, created_at FROM projects ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		var createdAtStr string
		if err := rows.Scan(&p.ID, &p.Name, &p.Color, &p.ColorIndex, &createdAtStr); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (d *DB) RecordSession(s Session) (*Session, error) {
	if s.UUID == "" {
		s.UUID = GenerateUUID()
	}

	p, err := d.GetOrCreateProject(s.ProjectName)
	if err != nil {
		return nil, err
	}
	s.ProjectColor = p.Color

	if s.DurationSeconds <= 0 {
		s.DurationSeconds = int(s.EndTime.Sub(s.StartTime).Seconds())
	}
	if s.DurationSeconds <= 0 {
		s.DurationSeconds = 1
	}

	completedVal := 0
	if s.Completed {
		completedVal = 1
	}

	nowUTC := time.Now().UTC()
	nowStr := nowUTC.Format(time.RFC3339)
	s.UpdatedAt = nowUTC

	// Check if a session with this UUID already exists (idempotent upsert)
	var existingID int64
	err = d.conn.QueryRow(`SELECT id FROM sessions WHERE uuid = ?`, s.UUID).Scan(&existingID)
	if err == nil && existingID > 0 {
		_, err = d.conn.Exec(
			`UPDATE sessions SET project_name = ?, mode = ?, start_time = ?, end_time = ?, duration_seconds = ?, completed = ?, note = ?, updated_at = ? WHERE id = ?`,
			s.ProjectName, s.Mode, s.StartTime.UTC().Format(time.RFC3339), s.EndTime.UTC().Format(time.RFC3339),
			s.DurationSeconds, completedVal, s.Note, nowStr, existingID,
		)
		if err != nil {
			return nil, err
		}
		s.ID = existingID
		return &s, nil
	}

	res, err := d.conn.Exec(
		`INSERT INTO sessions (uuid, project_name, mode, start_time, end_time, duration_seconds, completed, note, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.UUID, s.ProjectName, s.Mode, s.StartTime.UTC().Format(time.RFC3339), s.EndTime.UTC().Format(time.RFC3339),
		s.DurationSeconds, completedVal, s.Note, nowStr,
	)
	if err != nil {
		return nil, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	s.ID = id
	return &s, nil
}

func (d *DB) DeleteSession(id int64) error {
	_, err := d.conn.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

func (d *DB) DeleteSessionByUUID(uuid string) error {
	if uuid == "" {
		return nil
	}
	_, err := d.conn.Exec(`DELETE FROM sessions WHERE uuid = ?`, uuid)
	return err
}

func (d *DB) UpdateSession(id int64, projectName string, startTime, endTime time.Time) (*Session, error) {
	var uuidStr sql.NullString
	_ = d.conn.QueryRow(`SELECT uuid FROM sessions WHERE id = ?`, id).Scan(&uuidStr)

	p, err := d.GetOrCreateProject(projectName)
	if err != nil {
		return nil, err
	}

	durSec := int(endTime.Sub(startTime).Seconds())
	if durSec <= 0 {
		durSec = 1
	}

	nowUTC := time.Now().UTC()
	nowStr := nowUTC.Format(time.RFC3339)

	_, err = d.conn.Exec(
		`UPDATE sessions SET project_name = ?, start_time = ?, end_time = ?, duration_seconds = ?, updated_at = ? WHERE id = ?`,
		p.Name, startTime.UTC().Format(time.RFC3339), endTime.UTC().Format(time.RFC3339), durSec, nowStr, id,
	)
	if err != nil {
		return nil, err
	}

	actualUUID := ""
	if uuidStr.Valid {
		actualUUID = uuidStr.String
	}

	return &Session{
		ID:              id,
		UUID:            actualUUID,
		ProjectName:     p.Name,
		ProjectColor:    p.Color,
		StartTime:       startTime.Local(),
		EndTime:         endTime.Local(),
		DurationSeconds: durSec,
		Completed:       true,
		UpdatedAt:       nowUTC,
	}, nil
}

func (d *DB) GetAllSessions() ([]Session, error) {
	rows, err := d.conn.Query(`
		SELECT s.id, COALESCE(s.uuid, '') as uuid, s.project_name, COALESCE(p.color, '#ea6f91') as color, s.mode, s.start_time, s.end_time, s.duration_seconds, s.completed, COALESCE(s.note, '')
		FROM sessions s
		LEFT JOIN projects p ON lower(p.name) = lower(s.project_name)
		ORDER BY s.start_time ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var s Session
		var startStr, endStr string
		var completedInt int
		if err := rows.Scan(&s.ID, &s.UUID, &s.ProjectName, &s.ProjectColor, &s.Mode, &startStr, &endStr, &s.DurationSeconds, &completedInt, &s.Note); err != nil {
			return nil, err
		}
		s.StartTime, _ = time.Parse(time.RFC3339, startStr)
		s.EndTime, _ = time.Parse(time.RFC3339, endStr)
		s.Completed = (completedInt == 1)
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}


func (d *DB) ImportSessions(sessions []Session) (int, int, error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	checkStmt, err := tx.Prepare(`
		SELECT COUNT(*) FROM sessions 
		WHERE lower(project_name) = lower(?) 
		  AND start_time = ? 
		  AND duration_seconds = ?
	`)
	if err != nil {
		return 0, 0, err
	}
	defer checkStmt.Close()

	insertStmt, err := tx.Prepare(`
		INSERT INTO sessions (uuid, project_name, mode, start_time, end_time, duration_seconds, completed, note, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return 0, 0, err
	}
	defer insertStmt.Close()

	imported := 0
	skipped := 0
	nowStr := time.Now().UTC().Format(time.RFC3339)

	for _, s := range sessions {
		projName := strings.TrimSpace(s.ProjectName)
		if projName == "" {
			projName = "General"
		}
		mode := strings.TrimSpace(s.Mode)
		if mode == "" {
			mode = "pomodoro"
		}
		if s.StartTime.IsZero() {
			skipped++
			continue
		}
		if s.DurationSeconds <= 0 && !s.EndTime.IsZero() && s.EndTime.After(s.StartTime) {
			s.DurationSeconds = int(s.EndTime.Sub(s.StartTime).Seconds())
		}
		if s.EndTime.IsZero() || s.EndTime.Before(s.StartTime) {
			s.EndTime = s.StartTime.Add(time.Duration(s.DurationSeconds) * time.Second)
		}

		startStr := s.StartTime.UTC().Format(time.RFC3339)
		endStr := s.EndTime.UTC().Format(time.RFC3339)

		var count int
		if err := checkStmt.QueryRow(projName, startStr, s.DurationSeconds).Scan(&count); err != nil {
			return imported, skipped, err
		}
		if count > 0 {
			skipped++
			continue
		}

		completedVal := 0
		if s.Completed {
			completedVal = 1
		}

		u := s.UUID
		if u == "" {
			u = GenerateUUID()
		}

		if _, err := insertStmt.Exec(u, projName, mode, startStr, endStr, s.DurationSeconds, completedVal, s.Note, nowStr); err != nil {
			return imported, skipped, err
		}
		imported++
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}

	// Ensure project entries exist for all imported projects
	for _, s := range sessions {
		projName := strings.TrimSpace(s.ProjectName)
		if projName != "" {
			_, _ = d.GetOrCreateProject(projName)
		}
	}

	return imported, skipped, nil
}

// ParseImportSessions parses session data in Timewarrior JSON, Focus JSON, or CSV formats.
func ParseImportSessions(data []byte) ([]Session, error) {
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty import data")
	}

	var sessions []Session

	if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
		// First try Timewarrior JSON format
		type timewImportItem struct {
			Start string   `json:"start"`
			End   string   `json:"end"`
			Tags  []string `json:"tags"`
		}
		var timewItems []timewImportItem
		if errTimew := json.Unmarshal(data, &timewItems); errTimew == nil && len(timewItems) > 0 && (len(timewItems[0].Tags) > 0 || len(timewItems[0].Start) > 0) {
			for _, item := range timewItems {
				var st, et time.Time
				formats := []string{"20060102T150405Z", time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05", "2006-01-02T15:04:05"}
				for _, f := range formats {
					if t, err := time.Parse(f, item.Start); err == nil {
						st = t
						break
					}
				}
				for _, f := range formats {
					if t, err := time.Parse(f, item.End); err == nil {
						et = t
						break
					}
				}
				tag := "General"
				if len(item.Tags) > 0 {
					tag = item.Tags[0]
				}
				dur := int(et.Sub(st).Seconds())
				if dur < 0 {
					dur = 0
				}
				sessions = append(sessions, Session{
					ProjectName:     tag,
					Mode:            "pomodoro",
					StartTime:       st,
					EndTime:         et,
					DurationSeconds: dur,
					Completed:       true,
				})
			}
			return sessions, nil
		}

		if strings.HasPrefix(trimmed, "[") {
			if err := json.Unmarshal(data, &sessions); err != nil {
				return nil, fmt.Errorf("invalid JSON: %w", err)
			}
			return sessions, nil
		}

		var wrapper struct {
			Sessions []Session `json:"sessions"`
		}
		if err := json.Unmarshal(data, &wrapper); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		return wrapper.Sessions, nil
	}

	// CSV format
	rdr := csv.NewReader(strings.NewReader(trimmed))
	records, err := rdr.ReadAll()
	if err != nil || len(records) < 2 {
		return nil, fmt.Errorf("invalid CSV or missing records: %w", err)
	}

	headers := records[0]
	headerMap := make(map[string]int)
	for idx, h := range headers {
		norm := strings.ToLower(strings.TrimSpace(h))
		headerMap[norm] = idx
	}

	for i := 1; i < len(records); i++ {
		row := records[i]
		if len(row) == 0 {
			continue
		}

		getVal := func(keys ...string) string {
			for _, k := range keys {
				if idx, ok := headerMap[k]; ok && idx < len(row) {
					return strings.TrimSpace(row[idx])
				}
			}
			return ""
		}

		proj := getVal("project", "projet", "client", "task")
		mode := getVal("mode", "type")
		startStr := getVal("start time", "start_time", "start", "debut")
		endStr := getVal("end time", "end_time", "end", "fin")
		durStr := getVal("duration (seconds)", "duration_seconds", "duration")
		note := getVal("note", "description", "comment")

		var startTime, endTime time.Time
		var durSec int

		formats := []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04"}
		for _, f := range formats {
			if t, err := time.Parse(f, startStr); err == nil {
				startTime = t
				break
			}
		}

		for _, f := range formats {
			if t, err := time.Parse(f, endStr); err == nil {
				endTime = t
				break
			}
		}

		if d, err := strconv.Atoi(durStr); err == nil {
			durSec = d
		}

		sessions = append(sessions, Session{
			ProjectName:     proj,
			Mode:            mode,
			StartTime:       startTime,
			EndTime:         endTime,
			DurationSeconds: durSec,
			Completed:       true,
			Note:            note,
		})
	}

	return sessions, nil
}

func (d *DB) GetLastProject() (string, error) {
	var proj string
	err := d.conn.QueryRow(`SELECT project_name FROM sessions ORDER BY start_time DESC LIMIT 1`).Scan(&proj)
	if err == nil && proj != "" {
		return proj, nil
	}
	err = d.conn.QueryRow(`SELECT name FROM projects ORDER BY id DESC LIMIT 1`).Scan(&proj)
	if err == nil && proj != "" {
		return proj, nil
	}
	return "Work", nil
}

func (d *DB) GetMonthProjects(year, month int) ([]Project, error) {
	firstDay := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local).UTC()
	lastDay := firstDay.AddDate(0, 1, 0)

	query := `
		SELECT DISTINCT p.id, p.name, p.color, p.color_index, p.created_at
		FROM sessions s
		JOIN projects p ON lower(p.name) = lower(s.project_name)
		WHERE s.start_time >= ? AND s.start_time < ?
		ORDER BY p.name ASC
	`
	rows, err := d.conn.Query(query, firstDay.Format(time.RFC3339), lastDay.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		var createdAtStr string
		if err := rows.Scan(&p.ID, &p.Name, &p.Color, &p.ColorIndex, &createdAtStr); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339, createdAtStr)
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

type PeriodOption struct {
	Year      int    `json:"year"`
	Month     int    `json:"month,omitempty"`
	Label     string `json:"label"`
}

func (d *DB) GetAvailablePeriods() (years []int, months []PeriodOption, err error) {
	now := time.Now()
	rows, err := d.conn.Query(`SELECT DISTINCT strftime('%Y-%m', start_time) FROM sessions ORDER BY start_time DESC`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	monthNamesEn := []string{
		"", "January", "February", "March", "April", "May", "June",
		"July", "August", "September", "October", "November", "December",
	}

	seenYears := make(map[int]bool)
	seenMonths := make(map[string]bool)

	// Ensure current month is always present
	curMKey := fmt.Sprintf("%04d-%02d", now.Year(), int(now.Month()))
	seenMonths[curMKey] = true
	months = append(months, PeriodOption{
		Year:  now.Year(),
		Month: int(now.Month()),
		Label: fmt.Sprintf("%s %d", monthNamesEn[now.Month()], now.Year()),
	})
	seenYears[now.Year()] = true
	years = append(years, now.Year())

	for rows.Next() {
		var ym string
		if err := rows.Scan(&ym); err != nil {
			continue
		}
		var y, m int
		if _, err := fmt.Sscanf(ym, "%d-%d", &y, &m); err == nil && y > 2000 && m >= 1 && m <= 12 {
			if !seenMonths[ym] {
				seenMonths[ym] = true
				months = append(months, PeriodOption{
					Year:  y,
					Month: m,
					Label: fmt.Sprintf("%s %d", monthNamesEn[m], y),
				})
			}
			if !seenYears[y] {
				seenYears[y] = true
				years = append(years, y)
			}
		}
	}

	return years, months, nil
}

func (d *DB) GetTodaySessions() ([]Session, error) {
	now := time.Now().Local()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UTC()
	endOfDay := startOfDay.Add(24 * time.Hour)

	return d.getSessionsBetween(startOfDay, endOfDay)
}

func (d *DB) getSessionsBetween(start, end time.Time) ([]Session, error) {
	query := `
		SELECT s.id, COALESCE(s.uuid, '') as uuid, s.project_name, p.color, s.mode, s.start_time, s.end_time, s.duration_seconds, s.completed, s.note
		FROM sessions s
		LEFT JOIN projects p ON lower(p.name) = lower(s.project_name)
		WHERE s.start_time >= ? AND s.start_time < ?
		ORDER BY s.start_time ASC
	`
	rows, err := d.conn.Query(query, start.Format(time.RFC3339), end.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var s Session
		var color sql.NullString
		var startStr, endStr string
		var compInt int
		if err := rows.Scan(&s.ID, &s.UUID, &s.ProjectName, &color, &s.Mode, &startStr, &endStr, &s.DurationSeconds, &compInt, &s.Note); err != nil {
			return nil, err
		}
		s.StartTime, _ = time.Parse(time.RFC3339, startStr)
		s.EndTime, _ = time.Parse(time.RFC3339, endStr)
		s.StartTime = s.StartTime.Local()
		s.EndTime = s.EndTime.Local()
		s.Completed = (compInt == 1)
		if color.Valid && color.String != "" {
			s.ProjectColor = color.String
		} else {
			s.ProjectColor = RosePinePalette[0]
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// GetMonthData generates the Timewarrior-style monthly grid data for year & month
func (d *DB) GetMonthData(year int, month int) (*MonthData, error) {
	firstDay := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
	lastDay := firstDay.AddDate(0, 1, 0)

	sessions, err := d.getSessionsBetween(firstDay.UTC(), lastDay.UTC())
	if err != nil {
		return nil, err
	}

	monthNamesEn := []string{
		"", "January", "February", "March", "April", "May", "June",
		"July", "August", "September", "October", "November", "December",
	}
	dayNamesEn := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

	totalMonthSeconds := 0
	var days []DaySummary

	// Group sessions by day
	sessionsByDay := make(map[int][]Session)
	for _, s := range sessions {
		day := s.StartTime.Day()
		sessionsByDay[day] = append(sessionsByDay[day], s)
		totalMonthSeconds += s.DurationSeconds
	}

	// Generate day summaries for each day of the month
	daysInMonth := lastDay.AddDate(0, 0, -1).Day()
	for dayNum := 1; dayNum <= daysInMonth; dayNum++ {
		date := time.Date(year, time.Month(month), dayNum, 0, 0, 0, 0, time.Local)
		dayOfWeek := int(date.Weekday())
		_, weekNum := date.ISOWeek()

		daySessions := sessionsByDay[dayNum]
		dayTotal := 0
		for _, s := range daySessions {
			dayTotal += s.DurationSeconds
		}

		days = append(days, DaySummary{
			Date:         date.Format("2006-01-02"),
			DayOfWeek:    dayOfWeek,
			DayName:      dayNamesEn[dayOfWeek],
			DayNumber:    dayNum,
			WeekNumber:   weekNum,
			TotalSeconds: dayTotal,
			Sessions:     daySessions,
		})
	}

	projects, _ := d.GetMonthProjects(year, month)
	if projects == nil {
		projects = []Project{}
	}

	return &MonthData{
		Year:         year,
		Month:        month,
		MonthName:    fmt.Sprintf("%s %d", monthNamesEn[month], year),
		TotalSeconds: totalMonthSeconds,
		Days:         days,
		Projects:     projects,
	}, nil
}

// GetStats returns statistics for a period (e.g. "month", "year", "all") with optional year & month
func (d *DB) GetStats(period string, year int, month int) (*Stats, error) {
	now := time.Now().Local()
	if year <= 0 {
		year = now.Year()
	}
	if month <= 0 {
		month = int(now.Month())
	}

	var startTime time.Time
	var endTime time.Time

	switch period {
	case "month":
		startTime = time.Date(year, time.Month(month), 1, 0, 0, 0, 0, now.Location())
		endTime = startTime.AddDate(0, 1, 0)
	case "year":
		startTime = time.Date(year, 1, 1, 0, 0, 0, 0, now.Location())
		endTime = startTime.AddDate(1, 0, 0)
	case "all":
		startTime = time.Date(2020, 1, 1, 0, 0, 0, 0, now.Location())
		endTime = now.Add(24 * time.Hour)
	default:
		// default to last 30 days
		startTime = now.AddDate(0, 0, -30)
		endTime = now.Add(24 * time.Hour)
	}

	query := `
		SELECT s.id, s.project_name, p.color, s.mode, s.start_time, s.end_time, s.duration_seconds
		FROM sessions s
		LEFT JOIN projects p ON lower(p.name) = lower(s.project_name)
		WHERE s.start_time >= ? AND s.start_time < ?
		ORDER BY s.start_time ASC
	`
	rows, err := d.conn.Query(query, startTime.UTC().Format(time.RFC3339), endTime.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	totalSec := 0
	sessionCount := 0
	pomodoroCount := 0
	freeCount := 0
	activeDays := make(map[string]bool)

	type projAccum struct {
		color string
		sec   int
		count int
	}
	projectsMap := make(map[string]*projAccum)
	monthlyMap := make(map[string]int)
	weekdayDist := make([]int, 7) // 0: Mon, 1: Tue ... 6: Sun
	hourlyDist := make([]int, 24)

	for rows.Next() {
		var id int64
		var proj, mode, startStr, endStr string
		var color sql.NullString
		var dur int
		if err := rows.Scan(&id, &proj, &color, &mode, &startStr, &endStr, &dur); err != nil {
			return nil, err
		}

		st, _ := time.Parse(time.RFC3339, startStr)
		st = st.Local()
		totalSec += dur
		sessionCount++

		if mode == "pomodoro" {
			pomodoroCount++
		} else {
			freeCount++
		}

		dayKey := st.Format("2006-01-02")
		activeDays[dayKey] = true

		// Project accumulation
		projCol := RosePinePalette[0]
		if color.Valid && color.String != "" {
			projCol = color.String
		}
		if _, ok := projectsMap[proj]; !ok {
			projectsMap[proj] = &projAccum{color: projCol}
		}
		projectsMap[proj].sec += dur
		projectsMap[proj].count++

		// Monthly accumulation (e.g. "2026-10")
		mKey := st.Format("2006-01")
		monthlyMap[mKey] += dur

		// Weekday accumulation: ISO Weekday (1=Mon ... 7=Sun) -> 0..6
		wd := int(st.Weekday())
		isoIndex := (wd + 6) % 7 // Monday is 0, Sunday is 6
		weekdayDist[isoIndex] += dur

		// Hourly distribution
		hr := st.Hour()
		if hr >= 0 && hr < 24 {
			hourlyDist[hr] += dur
		}
	}

	var projectStats []ProjectStat
	for name, acc := range projectsMap {
		pct := 0.0
		if totalSec > 0 {
			pct = (float64(acc.sec) / float64(totalSec)) * 100.0
		}
		avgDur := 0
		if acc.count > 0 {
			avgDur = acc.sec / acc.count
		}
		projectStats = append(projectStats, ProjectStat{
			ProjectName:     name,
			Color:           acc.color,
			TotalSeconds:    acc.sec,
			SessionCount:    acc.count,
			Percentage:      pct,
			AverageDuration: avgDur,
		})
	}

	// Sort projects by total seconds descending
	for i := 0; i < len(projectStats)-1; i++ {
		for j := i + 1; j < len(projectStats); j++ {
			if projectStats[j].TotalSeconds > projectStats[i].TotalSeconds {
				projectStats[i], projectStats[j] = projectStats[j], projectStats[i]
			}
		}
	}

	// Query monthlyBreakdown independently to show full 12 months of activity
	monthlyStatsMap := make(map[string]int)
	var mbStart, mbEnd time.Time
	if period == "year" {
		mbStart = time.Date(year, 1, 1, 0, 0, 0, 0, now.Location())
		mbEnd = mbStart.AddDate(1, 0, 0)
	} else {
		targetMonth := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, now.Location())
		mbStart = targetMonth.AddDate(0, -11, 0)
		mbEnd = targetMonth.AddDate(0, 1, 0)
	}

	mbRows, mbErr := d.conn.Query(`
		SELECT strftime('%Y-%m', start_time) as ym, SUM(duration_seconds)
		FROM sessions
		WHERE start_time >= ? AND start_time < ?
		GROUP BY ym
	`, mbStart.UTC().Format(time.RFC3339), mbEnd.UTC().Format(time.RFC3339))
	if mbErr == nil {
		defer mbRows.Close()
		for mbRows.Next() {
			var ym string
			var sumSec int
			if err := mbRows.Scan(&ym, &sumSec); err == nil {
				monthlyStatsMap[ym] = sumSec
			}
		}
	}

	var monthlyBreakdown []MonthlyBar
	if period == "year" {
		for m := 1; m <= 12; m++ {
			key := fmt.Sprintf("%04d-%02d", year, m)
			monthlyBreakdown = append(monthlyBreakdown, MonthlyBar{
				Label:        key,
				TotalSeconds: monthlyStatsMap[key],
			})
		}
	} else {
		// Build monthly breakdown for the 12 months leading up to target month
		targetMonth := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, now.Location())
		for i := 11; i >= 0; i-- {
			m := targetMonth.AddDate(0, -i, 0)
			key := m.Format("2006-01")
			monthlyBreakdown = append(monthlyBreakdown, MonthlyBar{
				Label:        key,
				TotalSeconds: monthlyStatsMap[key],
			})
		}
	}

	// Query yearlyBreakdown for all recorded years (at least 5 years ending in current year)
	yearlyStatsMap := make(map[int]int)
	minYear := now.Year()
	maxYear := now.Year()

	yRows, yErr := d.conn.Query(`
		SELECT strftime('%Y', start_time) as yr, SUM(duration_seconds)
		FROM sessions
		WHERE duration_seconds > 0
		GROUP BY yr
	`)
	if yErr == nil {
		defer yRows.Close()
		for yRows.Next() {
			var yrStr string
			var sumSec int
			if err := yRows.Scan(&yrStr, &sumSec); err == nil {
				yrInt, _ := strconv.Atoi(yrStr)
				if yrInt > 0 {
					yearlyStatsMap[yrInt] = sumSec
					if yrInt < minYear {
						minYear = yrInt
					}
					if yrInt > maxYear {
						maxYear = yrInt
					}
				}
			}
		}
	}

	startYear := minYear
	if startYear > now.Year()-4 {
		startYear = now.Year() - 4
	}
	if maxYear < now.Year() {
		maxYear = now.Year()
	}

	var yearlyBreakdown []YearlyBar
	for y := startYear; y <= maxYear; y++ {
		yearlyBreakdown = append(yearlyBreakdown, YearlyBar{
			Year:         y,
			Label:        strconv.Itoa(y),
			TotalSeconds: yearlyStatsMap[y],
		})
	}

	activeDaysCount := len(activeDays)
	avgPerDay := 0
	if activeDaysCount > 0 {
		avgPerDay = totalSec / activeDaysCount
	}

	return &Stats{
		TotalSeconds:        totalSec,
		SessionCount:        sessionCount,
		ActiveDaysCount:     activeDaysCount,
		AveragePerDay:       avgPerDay,
		PomodoroCount:       pomodoroCount,
		FreeCount:           freeCount,
		Projects:            projectStats,
		YearlyBreakdown:     yearlyBreakdown,
		MonthlyBreakdown:    monthlyBreakdown,
		WeekdayDistribution: weekdayDist,
		HourlyDistribution:  hourlyDist,
	}, nil
}

// RenameProjectForMonth renames all session occurrences of oldName to newName within the given year and month
func (d *DB) RenameProjectForMonth(year int, month int, oldName string, newName string) error {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" || newName == "" {
		return fmt.Errorf("project name cannot be empty")
	}

	firstDay := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
	lastDay := firstDay.AddDate(0, 1, 0)
	startStr := firstDay.UTC().Format(time.RFC3339)
	endStr := lastDay.UTC().Format(time.RFC3339)

	// Ensure new project exists with a color in projects table
	if _, err := d.GetOrCreateProject(newName); err != nil {
		return err
	}

	_, err := d.conn.Exec(`
		UPDATE sessions
		SET project_name = ?
		WHERE lower(project_name) = lower(?) AND start_time >= ? AND start_time < ?
	`, newName, oldName, startStr, endStr)
	return err
}

func (d *DB) GetSettings() (map[string]string, error) {
	rows, err := d.conn.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		res[k] = v
	}
	return res, rows.Err()
}

func (d *DB) SetSetting(key, val string) error {
	_, err := d.conn.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = ?`, key, val, val)
	return err
}

// Password hashing & verification

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(password))
	digest := h.Sum(nil)
	return hex.EncodeToString(salt) + "$" + hex.EncodeToString(digest), nil
}

func CheckPassword(password, storedHash string) bool {
	parts := strings.Split(storedHash, "$")
	if len(parts) != 2 {
		return false
	}
	salt, err := hex.DecodeString(parts[0])
	if err != nil {
		return false
	}
	expectedDigest, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(password))
	digest := h.Sum(nil)
	return subtle.ConstantTimeCompare(digest, expectedDigest) == 1
}

func (d *DB) GetPasswordHash() (string, error) {
	var val string
	err := d.conn.QueryRow(`SELECT value FROM settings WHERE key = 'password_hash'`).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

func (d *DB) SetPasswordHash(hash string) error {
	_, err := d.conn.Exec(`INSERT INTO settings (key, value) VALUES ('password_hash', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, hash)
	return err
}

func (d *DB) HasPassword() (bool, error) {
	h, err := d.GetPasswordHash()
	if err != nil {
		return false, err
	}
	return h != "", nil
}

func (d *DB) CreateSession(token string, expiresAt time.Time) error {
	_, err := d.conn.Exec(`INSERT INTO auth_sessions (token, created_at, expires_at) VALUES (?, ?, ?)`, token, time.Now(), expiresAt)
	return err
}

func (d *DB) ValidateSession(token string) bool {
	if token == "" {
		return false
	}
	var count int
	err := d.conn.QueryRow(`SELECT COUNT(*) FROM auth_sessions WHERE token = ? AND expires_at > ?`, token, time.Now()).Scan(&count)
	return err == nil && count > 0
}

func (d *DB) DeleteAuthSession(token string) error {
	_, err := d.conn.Exec(`DELETE FROM auth_sessions WHERE token = ?`, token)
	return err
}

