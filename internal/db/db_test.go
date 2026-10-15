package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func setupTestDB(t *testing.T) *DB {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_focus.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to create test db: %v", err)
	}
	t.Cleanup(func() {
		database.Close()
	})
	return database
}

func TestSessionCRUD(t *testing.T) {
	database := setupTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	sess, err := database.RecordSession(Session{
		ProjectName:     "DeepWork",
		StartTime:       now.Add(-25 * time.Minute),
		EndTime:         now,
		DurationSeconds: 1500,
		Completed:       true,
		Mode:            "pomodoro",
	})
	if err != nil {
		t.Fatalf("RecordSession failed: %v", err)
	}

	if sess.ID == 0 {
		t.Fatalf("expected non-zero ID, got %d", sess.ID)
	}
	if sess.UUID == "" {
		t.Fatalf("expected non-empty UUID")
	}

	all, err := database.GetAllSessions()
	if err != nil {
		t.Fatalf("GetAllSessions failed: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 session, got %d", len(all))
	}
	if all[0].ProjectName != "DeepWork" {
		t.Errorf("expected project name DeepWork, got %s", all[0].ProjectName)
	}

	// Update session
	newStart := now.Add(-30 * time.Minute)
	updated, err := database.UpdateSession(sess.ID, "DeepWork Refined", newStart, now)
	if err != nil {
		t.Fatalf("UpdateSession failed: %v", err)
	}
	if updated.ProjectName != "DeepWork Refined" || updated.DurationSeconds != 1800 {
		t.Errorf("unexpected updated session: %+v", updated)
	}

	// Delete session
	if err := database.DeleteSession(sess.ID); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	all, err = database.GetAllSessions()
	if err != nil {
		t.Fatalf("GetAllSessions failed: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("expected 0 sessions after delete, got %d", len(all))
	}
}

func TestLegacySchemaMigration(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "legacy_focus.db")

	// Create legacy database with old schema missing uuid and updated_at
	legacyConn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to create legacy sqlite db: %v", err)
	}
	legacyTable := `CREATE TABLE sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		project_name TEXT NOT NULL,
		mode TEXT NOT NULL,
		start_time DATETIME NOT NULL,
		end_time DATETIME NOT NULL,
		duration_seconds INTEGER NOT NULL,
		completed INTEGER NOT NULL DEFAULT 1,
		note TEXT DEFAULT ''
	);`
	if _, err := legacyConn.Exec(legacyTable); err != nil {
		legacyConn.Close()
		t.Fatalf("failed to create legacy sessions table: %v", err)
	}
	// Insert a legacy session
	nowStr := time.Now().UTC().Format(time.RFC3339)
	if _, err := legacyConn.Exec(`INSERT INTO sessions (project_name, mode, start_time, end_time, duration_seconds, completed, note) VALUES ('LegacyProj', 'pomodoro', ?, ?, 1200, 1, 'Legacy test')`, nowStr, nowStr); err != nil {
		legacyConn.Close()
		t.Fatalf("failed to insert legacy session: %v", err)
	}
	legacyConn.Close()

	// Open with focus Open() which runs initSchema()
	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed on legacy database: %v", err)
	}
	defer database.Close()

	// Verify columns exist
	rows, err := database.conn.Query(`PRAGMA table_info(sessions)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info failed: %v", err)
	}
	hasUUID := false
	hasUpdatedAt := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notnull, pk int
		var dfltValue sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("Scan failed: %v", err)
		}
		if name == "uuid" {
			hasUUID = true
		} else if name == "updated_at" {
			hasUpdatedAt = true
		}
	}
	rows.Close()

	if !hasUUID {
		t.Errorf("expected uuid column to be added to legacy schema")
	}
	if !hasUpdatedAt {
		t.Errorf("expected updated_at column to be added to legacy schema")
	}

	// Verify existing row updated_at is not null
	var legacyUpdatedAt sql.NullString
	if err := database.conn.QueryRow(`SELECT updated_at FROM sessions WHERE project_name = 'LegacyProj'`).Scan(&legacyUpdatedAt); err != nil {
		t.Fatalf("failed to query legacy updated_at: %v", err)
	}
	if !legacyUpdatedAt.Valid || legacyUpdatedAt.String == "" {
		t.Errorf("expected legacy updated_at to be populated, got NULL/empty")
	}

	// Verify inserting new session works
	st := time.Now().UTC().Add(-10 * time.Minute)
	et := time.Now().UTC()
	sess, err := database.RecordSession(Session{
		ProjectName: "NewOnMigrated",
		StartTime:   st,
		EndTime:     et,
		Mode:        "pomodoro",
		Completed:   true,
	})
	if err != nil {
		t.Fatalf("RecordSession failed on migrated db: %v", err)
	}
	if sess.ID == 0 {
		t.Errorf("expected non-zero ID")
	}
}

func TestRecordSessionDurationHandling(t *testing.T) {
	database := setupTestDB(t)

	now := time.Now().UTC()
	// Case 1: Duration <= 0, calculate from EndTime.Sub(StartTime)
	sess1, err := database.RecordSession(Session{
		ProjectName:     "CalcDuration",
		StartTime:       now.Add(-60 * time.Second),
		EndTime:         now,
		DurationSeconds: 0,
		Mode:            "pomodoro",
	})
	if err != nil {
		t.Fatalf("RecordSession failed: %v", err)
	}
	if sess1.DurationSeconds != 60 {
		t.Errorf("expected duration to be calculated as 60, got %d", sess1.DurationSeconds)
	}
	if sess1.UpdatedAt.IsZero() {
		t.Errorf("expected UpdatedAt to be set")
	}

	// Case 2: Duration <= 0 and EndTime == StartTime, should default to at least 1 second
	sess2, err := database.RecordSession(Session{
		ProjectName:     "ZeroDuration",
		StartTime:       now,
		EndTime:         now,
		DurationSeconds: 0,
		Mode:            "pomodoro",
	})
	if err != nil {
		t.Fatalf("RecordSession failed: %v", err)
	}
	if sess2.DurationSeconds < 1 {
		t.Errorf("expected duration to be at least 1, got %d", sess2.DurationSeconds)
	}

	// Case 3: Duration <= 0 and EndTime before StartTime, should default to at least 1 second
	sess3, err := database.RecordSession(Session{
		ProjectName:     "NegativeDuration",
		StartTime:       now,
		EndTime:         now.Add(-10 * time.Second),
		DurationSeconds: -5,
		Mode:            "pomodoro",
	})
	if err != nil {
		t.Fatalf("RecordSession failed: %v", err)
	}
	if sess3.DurationSeconds < 1 {
		t.Errorf("expected duration to be at least 1, got %d", sess3.DurationSeconds)
	}

	// Case 4: UpdateSession with EndTime == StartTime, should default to at least 1 second
	updated, err := database.UpdateSession(sess1.ID, "CalcDuration", now, now)
	if err != nil {
		t.Fatalf("UpdateSession failed: %v", err)
	}
	if updated.DurationSeconds < 1 {
		t.Errorf("expected updated duration to be at least 1, got %d", updated.DurationSeconds)
	}
}

