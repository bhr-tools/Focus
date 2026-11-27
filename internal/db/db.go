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

	"github.com/bhr-tools/kit/store"
)

// ErrNotFound is returned when the targeted row does not exist.
var ErrNotFound = errors.New("not found")

// ErrInvalidSession is returned for a session longer than MaxSessionDuration.
var ErrInvalidSession = errors.New("invalid session")

// MaxSessionDuration bounds a session, which also bounds the per-hour
// statistics work done for any stored row.
const MaxSessionDuration = 7 * 24 * time.Hour

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

// migrations are applied in order (PRAGMA user_version counts them). Never
// edit a released one: append a new one.
var migrations = []string{
	// 1. The schema of the releases before numbered migrations; on their
	// databases it changes nothing.
	`CREATE TABLE IF NOT EXISTS projects (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT UNIQUE NOT NULL COLLATE NOCASE,
		color TEXT NOT NULL,
		color_index INTEGER NOT NULL,
		created_at DATETIME NOT NULL
	);
	CREATE TABLE IF NOT EXISTS sessions (
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
	);
	CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS auth_tokens (
		token_hash TEXT PRIMARY KEY,
		expires_at INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS app_tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		token_hash TEXT UNIQUE NOT NULL,
		created_at INTEGER NOT NULL,
		used_at INTEGER
	);
	UPDATE sessions SET updated_at = CURRENT_TIMESTAMP WHERE updated_at IS NULL;
	UPDATE sessions SET note = '' WHERE note IS NULL;
	CREATE INDEX IF NOT EXISTS idx_sessions_start_time ON sessions(start_time);
	CREATE INDEX IF NOT EXISTS idx_sessions_project_name ON sessions(project_name);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_uuid ON sessions(uuid);`,

	// 2. The password, logins and connected apps move to the kit's tables
	// (kit/auth): nobody is signed out, connected apps keep their token.
	// Hashes keep their algorithm under the kit's names (v2$, s1$).
	`CREATE TABLE IF NOT EXISTS auth_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS auth_sessions (token TEXT PRIMARY KEY, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);
	CREATE TABLE IF NOT EXISTS kit_app_tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		scope TEXT NOT NULL DEFAULT '',
		token_hash TEXT NOT NULL UNIQUE,
		created_at DATETIME NOT NULL,
		used_at DATETIME
	);
	INSERT OR IGNORE INTO auth_settings (key, value)
		SELECT 'password_hash', CASE WHEN value LIKE 'pbkdf2-sha256$%' THEN 'v2' || substr(value, 14) ELSE 's1$' || value END
		FROM settings WHERE key = 'password_hash' AND value <> '';
	DELETE FROM settings WHERE key = 'password_hash';
	INSERT OR IGNORE INTO auth_sessions (token, created_at)
		SELECT token_hash, strftime('%Y-%m-%d %H:%M:%S', 'now') FROM auth_tokens WHERE expires_at > CAST(strftime('%s', 'now') AS INTEGER);
	INSERT OR IGNORE INTO kit_app_tokens (name, scope, token_hash, created_at, used_at)
		SELECT name, 'read', token_hash, datetime(created_at, 'unixepoch'), CASE WHEN used_at THEN datetime(used_at, 'unixepoch') END
		FROM app_tokens ORDER BY id;
	DROP TABLE auth_tokens;
	DROP TABLE app_tokens;`,
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
	if err := upgradeUnversioned(dbPath); err != nil {
		return nil, err
	}
	conn, err := store.Open(dbPath, migrations)
	if err != nil {
		return nil, err
	}
	database := &DB{conn: conn}
	if err := database.tidy(); err != nil {
		conn.Close()
		return nil, err
	}
	return database, nil
}

// upgradeUnversioned brings a database of the releases before numbered
// migrations (user_version 0) to the shape migration 1 expects: the columns
// added over time, and without the first login table, whose name the kit
// now uses.
func upgradeUnversioned(path string) error {
	if _, err := os.Stat(path); err != nil {
		return nil // a new database
	}
	conn, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer conn.Close()
	var version int
	if err := conn.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version > 0 {
		return err
	}
	columns := func(table string) (map[string]bool, error) {
		rows, err := conn.Query(`SELECT lower(name) FROM pragma_table_info(?)`, table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		names := map[string]bool{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, err
			}
			names[name] = true
		}
		return names, rows.Err()
	}
	sessions, err := columns("sessions")
	if err != nil {
		return err
	}
	if len(sessions) > 0 {
		for _, c := range []string{"uuid TEXT", "updated_at DATETIME"} {
			if !sessions[strings.Fields(c)[0]] {
				if _, err := conn.Exec(`ALTER TABLE sessions ADD COLUMN ` + c); err != nil {
					return fmt.Errorf("add column %s: %w", c, err)
				}
			}
		}
	}
	if old, err := columns("auth_sessions"); err != nil {
		return err
	} else if old["expires_at"] {
		if _, err := conn.Exec(`DROP TABLE auth_sessions`); err != nil {
			return err
		}
	}
	return nil
}

// SQL is the connection, for the kit's auth tables.
func (d *DB) SQL() *sql.DB {
	return d.conn
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

// tidy fixes rows written by older releases (missing UUIDs, project names
// with capitals) and sets the default settings; it changes nothing twice.
func (d *DB) tidy() error {
	if err := d.backfillUUIDs(); err != nil {
		return err
	}
	if err := d.lowercaseProjects(); err != nil {
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

// lowercaseProjects converts project names written before names were
// lowercased. Two projects that only differ by case (possible for non-ASCII
// letters, which SQLite's NOCASE ignores) are merged into the lowercase one.
func (d *DB) lowercaseProjects() error {
	names, err := d.distinctNames(`SELECT name COLLATE BINARY FROM projects
		UNION SELECT project_name COLLATE BINARY FROM sessions`)
	if err != nil {
		return err
	}
	var renames []string
	for _, name := range names {
		if name != projectName(name) {
			renames = append(renames, name)
		}
	}
	if len(renames) == 0 {
		return nil
	}

	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := formatTime(time.Now())
	for _, old := range renames {
		lower := projectName(old)
		var existing int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM projects WHERE name = ? COLLATE BINARY`, lower).Scan(&existing); err != nil {
			return fmt.Errorf("lowercase projects: %w", err)
		}
		if existing > 0 {
			_, err = tx.Exec(`DELETE FROM projects WHERE name = ? COLLATE BINARY`, old)
		} else {
			_, err = tx.Exec(`UPDATE projects SET name = ? WHERE name = ? COLLATE BINARY`, lower, old)
		}
		if err != nil {
			return fmt.Errorf("lowercase project %q: %w", old, err)
		}
		if _, err := tx.Exec(`UPDATE sessions SET project_name = ?, updated_at = ? WHERE project_name = ? COLLATE BINARY`,
			lower, now, old); err != nil {
			return fmt.Errorf("lowercase sessions of %q: %w", old, err)
		}
	}
	return tx.Commit()
}

func (d *DB) distinctNames(query string) ([]string, error) {
	rows, err := d.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
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
