// Package db is the SQLite persistence layer of Focus.
//
// Every timestamp is stored as an RFC3339 string in UTC (second precision),
// so lexical ordering in SQL matches chronological ordering. Values returned
// to callers are converted to the local time zone.
package db

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when the targeted row does not exist.
var ErrNotFound = errors.New("not found")

// RosePinePalette is the ordered list of colors assigned to new projects.
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

// Session modes.
const (
	ModePomodoro = "pomodoro"
	ModeFree     = "free"
)

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
	Mode            string    `json:"mode"` // "pomodoro" or "free"
	StartTime       time.Time `json:"start_time"`
	EndTime         time.Time `json:"end_time"`
	DurationSeconds int       `json:"duration_seconds"`
	Completed       bool      `json:"completed"`
	Note            string    `json:"note"`
	UpdatedAt       time.Time `json:"updated_at,omitzero"`
}

type DB struct {
	conn *sql.DB
}

// DefaultPath returns ~/.local/share/focus/focus.db.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("user home directory not found")
	}
	return filepath.Join(home, ".local", "share", "focus", "focus.db"), nil
}

// Open opens (and creates or migrates) the database. An empty path means DefaultPath.
func Open(dbPath string) (*DB, error) {
	if dbPath == "" {
		p, err := DefaultPath()
		if err != nil {
			return nil, err
		}
		dbPath = p
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}

	// Pragmas go in the DSN so that they apply to every pooled connection.
	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	// SQLite allows a single writer anyway: one connection removes every
	// "database is locked" race for this single-user application.
	conn.SetMaxOpenConns(1)

	database := &DB{conn: conn}
	if err := database.initSchema(); err != nil {
		conn.Close()
		return nil, err
	}
	return database, nil
}

func (d *DB) Close() error {
	return d.conn.Close()
}

// GenerateUUID returns a random RFC 4122 version 4 UUID.
func GenerateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func formatTime(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// parseTime reads a stored timestamp. Legacy rows may use the SQLite
// CURRENT_TIMESTAMP layout, which is UTC as well.
func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Local()
		}
	}
	return time.Time{}
}

func (d *DB) initSchema() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS projects (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT UNIQUE NOT NULL COLLATE NOCASE,
			color TEXT NOT NULL,
			color_index INTEGER NOT NULL,
			created_at DATETIME NOT NULL
		)`,
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
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		// Login tokens are stored hashed; expires_at is a Unix timestamp.
		`CREATE TABLE IF NOT EXISTS auth_tokens (
			token_hash TEXT PRIMARY KEY,
			expires_at INTEGER NOT NULL
		)`,
		// Replaced by auth_tokens (plaintext tokens, driver-dependent date format).
		`DROP TABLE IF EXISTS auth_sessions`,
	}
	for _, q := range statements {
		if _, err := d.conn.Exec(q); err != nil {
			return fmt.Errorf("init schema: %w", err)
		}
	}

	// Columns added after the first release.
	columns, err := d.columnNames("sessions")
	if err != nil {
		return err
	}
	if !columns["uuid"] {
		if _, err := d.conn.Exec(`ALTER TABLE sessions ADD COLUMN uuid TEXT`); err != nil {
			return fmt.Errorf("add uuid column: %w", err)
		}
	}
	if !columns["updated_at"] {
		if _, err := d.conn.Exec(`ALTER TABLE sessions ADD COLUMN updated_at DATETIME`); err != nil {
			return fmt.Errorf("add updated_at column: %w", err)
		}
	}

	migrations := []string{
		`UPDATE sessions SET updated_at = CURRENT_TIMESTAMP WHERE updated_at IS NULL`,
		`UPDATE sessions SET note = '' WHERE note IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_start_time ON sessions(start_time)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_project_name ON sessions(project_name)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_uuid ON sessions(uuid)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_tokens_expires ON auth_tokens(expires_at)`,
	}
	for _, q := range migrations {
		if _, err := d.conn.Exec(q); err != nil {
			return fmt.Errorf("migrate schema: %w", err)
		}
	}

	if err := d.backfillUUIDs(); err != nil {
		return err
	}

	defaults := map[string]string{
		SettingWorkDuration:  "60",
		SettingBreakDuration: "10",
		SettingAlarmEnabled:  "true",
	}
	if home, err := os.UserHomeDir(); err == nil {
		defaults[SettingAlarmSound] = filepath.Join(home, ".config", "deepwork-alarm.mp3")
	}
	for k, v := range defaults {
		if _, err := d.conn.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)`, k, v); err != nil {
			return fmt.Errorf("insert default setting %s: %w", k, err)
		}
	}
	return nil
}

func (d *DB) columnNames(table string) (map[string]bool, error) {
	rows, err := d.conn.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("inspect %s table: %w", table, err)
	}
	defer rows.Close()
	names := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names[strings.ToLower(name)] = true
	}
	return names, rows.Err()
}

func (d *DB) backfillUUIDs() error {
	rows, err := d.conn.Query(`SELECT id FROM sessions WHERE uuid IS NULL OR uuid = ''`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := d.conn.Exec(`UPDATE sessions SET uuid = ? WHERE id = ?`, GenerateUUID(), id); err != nil {
			return fmt.Errorf("backfill uuid: %w", err)
		}
	}
	return nil
}
