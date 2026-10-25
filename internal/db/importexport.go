package db

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Export formats.
const (
	FormatJSON        = "json"
	FormatCSV         = "csv"
	FormatTimewarrior = "timewarrior"
)

const timewLayout = "20060102T150405Z"

// NormalizeExportFormat maps user input to a supported format (JSON by default).
func NormalizeExportFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "csv":
		return FormatCSV
	case "timew", "timewarrior":
		return FormatTimewarrior
	default:
		return FormatJSON
	}
}

// ExportFileName returns the suggested file name of an export.
func ExportFileName(format string, now time.Time) string {
	ext := map[string]string{FormatCSV: "csv", FormatTimewarrior: "timew.json"}[format]
	if ext == "" {
		ext = "json"
	}
	return fmt.Sprintf("focus-export-%s.%s", now.Format("20060102-150405"), ext)
}

// WriteExport writes every session in the given format.
func (d *DB) WriteExport(w io.Writer, format string) error {
	sessions, err := d.GetAllSessions()
	if err != nil {
		return err
	}

	switch format {
	case FormatCSV:
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"Project", "Mode", "Start Time", "End Time", "Duration (seconds)", "Duration", "Completed", "Note"})
		for _, s := range sessions {
			d := s.DurationSeconds
			_ = cw.Write([]string{
				s.ProjectName,
				s.Mode,
				s.StartTime.UTC().Format(time.RFC3339),
				s.EndTime.UTC().Format(time.RFC3339),
				strconv.Itoa(d),
				fmt.Sprintf("%02d:%02d:%02d", d/3600, d%3600/60, d%60),
				strconv.FormatBool(s.Completed),
				s.Note,
			})
		}
		cw.Flush()
		return cw.Error()

	case FormatTimewarrior:
		type timewEntry struct {
			ID    int64    `json:"id,omitempty"`
			Start string   `json:"start"`
			End   string   `json:"end"`
			Tags  []string `json:"tags"`
		}
		entries := []timewEntry{}
		for _, s := range sessions {
			entries = append(entries, timewEntry{
				ID:    s.ID,
				Start: s.StartTime.UTC().Format(timewLayout),
				End:   s.EndTime.UTC().Format(timewLayout),
				Tags:  []string{s.ProjectName},
			})
		}
		return writeIndentedJSON(w, entries)

	default:
		projects, err := d.ListProjects()
		if err != nil {
			return err
		}
		return writeIndentedJSON(w, struct {
			Version    int       `json:"version"`
			ExportedAt time.Time `json:"exported_at"`
			Projects   []Project `json:"projects"`
			Sessions   []Session `json:"sessions"`
		}{1, time.Now().UTC(), projects, sessions})
	}
}

func writeIndentedJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// ImportSessions inserts sessions in a single transaction. Sessions whose UUID
// already exists, or matching an existing (project, start, duration), are
// skipped, as are sessions without a valid interval.
func (d *DB) ImportSessions(sessions []Session) (imported, skipped int, err error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	now := formatTime(time.Now())
	for _, s := range sessions {
		if s.StartTime.IsZero() {
			skipped++
			continue
		}
		if s.EndTime.IsZero() || !s.EndTime.After(s.StartTime) {
			s.EndTime = s.StartTime.Add(time.Duration(s.DurationSeconds) * time.Second)
		}
		if s.DurationSeconds <= 0 {
			s.DurationSeconds = int(s.EndTime.Sub(s.StartTime).Seconds())
		}
		if s.DurationSeconds <= 0 {
			skipped++
			continue
		}
		// Keep the invariant end - start = duration relied on by the timeline.
		s.EndTime = s.StartTime.Add(time.Duration(s.DurationSeconds) * time.Second)
		if s.UUID == "" {
			s.UUID = GenerateUUID()
		}
		if s.Mode != ModeFree {
			s.Mode = ModePomodoro
		}
		p, err := getOrCreateProject(tx, s.ProjectName)
		if err != nil {
			return 0, 0, err
		}

		start := formatTime(s.StartTime)
		var exists int
		err = tx.QueryRow(`SELECT COUNT(*) FROM sessions
			WHERE uuid = ? OR (project_name = ? AND start_time = ? AND duration_seconds = ?)`,
			s.UUID, p.Name, start, s.DurationSeconds).Scan(&exists)
		if err != nil {
			return 0, 0, err
		}
		if exists > 0 {
			skipped++
			continue
		}

		_, err = tx.Exec(`INSERT INTO sessions (uuid, project_name, mode, start_time, end_time, duration_seconds, completed, note, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.UUID, p.Name, s.Mode, start, formatTime(s.EndTime), s.DurationSeconds, s.Completed, s.Note, now)
		if err != nil {
			return 0, 0, err
		}
		imported++
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return imported, skipped, nil
}

// parseImportTime accepts RFC3339, Timewarrior and zoneless layouts (the
// latter are interpreted in local time).
func parseImportTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, timewLayout} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ParseImportSessions parses a Timewarrior JSON export, a Focus JSON export
// (full object or bare session array) or a CSV file.
func ParseImportSessions(data []byte) ([]Session, error) {
	trimmed := strings.TrimSpace(string(data))
	switch {
	case trimmed == "":
		return nil, fmt.Errorf("empty import data")
	case strings.HasPrefix(trimmed, "{"):
		var wrapper struct {
			Sessions []Session `json:"sessions"`
		}
		if err := json.Unmarshal(data, &wrapper); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		return wrapper.Sessions, nil
	case strings.HasPrefix(trimmed, "["):
		return parseJSONArray(data)
	default:
		return parseCSV(trimmed)
	}
}

// parseJSONArray distinguishes Timewarrior entries ("start"/"end"/"tags")
// from Focus sessions ("start_time"/"end_time"/"project_name").
func parseJSONArray(data []byte) ([]Session, error) {
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if len(items) == 0 {
		return []Session{}, nil
	}
	if _, isFocus := items[0]["start_time"]; isFocus {
		var sessions []Session
		if err := json.Unmarshal(data, &sessions); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		return sessions, nil
	}

	var entries []struct {
		Start string   `json:"start"`
		End   string   `json:"end"`
		Tags  []string `json:"tags"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("invalid Timewarrior JSON: %w", err)
	}
	sessions := make([]Session, 0, len(entries))
	for _, e := range entries {
		project := DefaultProjectName
		if len(e.Tags) > 0 {
			project = e.Tags[0]
		}
		// Open intervals (no end) are skipped by ImportSessions.
		sessions = append(sessions, Session{
			ProjectName: project,
			Mode:        ModePomodoro,
			StartTime:   parseImportTime(e.Start),
			EndTime:     parseImportTime(e.End),
			Completed:   true,
		})
	}
	return sessions, nil
}

func parseCSV(text string) ([]Session, error) {
	records, err := csv.NewReader(strings.NewReader(text)).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("invalid CSV: %w", err)
	}
	if len(records) < 2 {
		return nil, fmt.Errorf("CSV has no data rows")
	}

	columns := make(map[string]int)
	for i, h := range records[0] {
		columns[strings.ToLower(strings.TrimSpace(h))] = i
	}
	sessions := make([]Session, 0, len(records)-1)
	for _, row := range records[1:] {
		get := func(keys ...string) string {
			for _, k := range keys {
				if i, ok := columns[k]; ok && i < len(row) {
					return strings.TrimSpace(row[i])
				}
			}
			return ""
		}
		duration, _ := strconv.Atoi(get("duration (seconds)", "duration_seconds", "duration"))
		sessions = append(sessions, Session{
			ProjectName:     get("project", "projet", "client", "task"),
			Mode:            get("mode", "type"),
			StartTime:       parseImportTime(get("start time", "start_time", "start", "debut")),
			EndTime:         parseImportTime(get("end time", "end_time", "end", "fin")),
			DurationSeconds: duration,
			Completed:       true,
			Note:            get("note", "description", "comment"),
		})
	}
	return sessions, nil
}
