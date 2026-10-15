package timer

import (
	"path/filepath"
	"testing"
	"time"

	"focus/internal/db"
)

func setupTestEngine(t *testing.T) (*Engine, *db.DB) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_timer.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() {
		database.Close()
	})

	engine := NewEngine(database)
	return engine, database
}

func TestEngineStopRecordsSession(t *testing.T) {
	engine, database := setupTestEngine(t)

	// Start a stopwatch (free) session
	engine.Start("Deepwork Systems", "free", "client-1")

	// Simulate elapsed time
	engine.mu.Lock()
	engine.elapsedSeconds = 120
	engine.sessionStartTime = time.Now().Add(-120 * time.Second)
	engine.mu.Unlock()

	// Stop engine (simulating clicking "Finish" on stopwatch)
	snap := engine.Stop()
	if snap.Status != "idle" {
		t.Errorf("expected status idle, got %s", snap.Status)
	}
	if snap.LastRecordedAt == 0 {
		t.Errorf("expected LastRecordedAt to be updated, got 0")
	}

	// Verify session recorded in database
	sessions, err := database.GetAllSessions()
	if err != nil {
		t.Fatalf("failed to get sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session recorded, got %d", len(sessions))
	}
	if sessions[0].ProjectName != "Deepwork Systems" {
		t.Errorf("expected project 'Deepwork Systems', got '%s'", sessions[0].ProjectName)
	}
	if sessions[0].DurationSeconds != 120 {
		t.Errorf("expected duration 120s, got %d", sessions[0].DurationSeconds)
	}
}

func TestEngineTickRecordsPomodoroSession(t *testing.T) {
	engine, database := setupTestEngine(t)

	// Start a pomodoro session
	engine.Start("API Architecture", "pomodoro", "client-1")

	// Fast forward timer to 1 second remaining
	engine.mu.Lock()
	engine.remainingSeconds = 1
	engine.elapsedSeconds = 1500
	engine.totalSeconds = 1500
	engine.sessionStartTime = time.Now().Add(-1500 * time.Second)
	engine.mu.Unlock()

	// Tick should complete the phase
	engine.tick()

	snap := engine.Snapshot()
	if snap.Phase != "break" {
		t.Errorf("expected phase break, got %s", snap.Phase)
	}
	if snap.Status != "paused" {
		t.Errorf("expected status paused, got %s", snap.Status)
	}
	if snap.LastRecordedAt == 0 {
		t.Errorf("expected LastRecordedAt to be updated, got 0")
	}

	// Verify session recorded in database
	sessions, err := database.GetAllSessions()
	if err != nil {
		t.Fatalf("failed to get sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session recorded, got %d", len(sessions))
	}
	if sessions[0].ProjectName != "API Architecture" {
		t.Errorf("expected project 'API Architecture', got '%s'", sessions[0].ProjectName)
	}
	if sessions[0].DurationSeconds != 1501 {
		t.Errorf("expected duration 1501s, got %d", sessions[0].DurationSeconds)
	}
}
