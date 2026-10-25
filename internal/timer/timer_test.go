package timer

import (
	"path/filepath"
	"testing"
	"time"

	"focus/internal/db"
)

func setupTestEngine(t *testing.T) (*Engine, *db.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test_timer.db"))
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	engine := NewEngine(database, "")
	t.Cleanup(func() {
		engine.Close()
		database.Close()
	})
	return engine, database
}

// rewind pretends the current phase has been running for d.
func rewind(e *Engine, d time.Duration) {
	e.mu.Lock()
	e.activeSince = e.activeSince.Add(-d)
	e.mu.Unlock()
}

func TestEngineStopRecordsSession(t *testing.T) {
	engine, database := setupTestEngine(t)

	engine.Start("Deepwork Systems", db.ModeFree, "client-1")
	rewind(engine, 120*time.Second)

	snap := engine.Stop()
	if snap.Status != StatusIdle {
		t.Errorf("expected status idle, got %s", snap.Status)
	}
	if snap.LastRecordedAt == 0 {
		t.Errorf("expected LastRecordedAt to be updated")
	}

	sessions, err := database.GetAllSessions()
	if err != nil {
		t.Fatalf("failed to get sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session recorded, got %d", len(sessions))
	}
	if sessions[0].ProjectName != "Deepwork Systems" || sessions[0].Mode != db.ModeFree {
		t.Errorf("unexpected session %+v", sessions[0])
	}
	if sessions[0].DurationSeconds != 120 {
		t.Errorf("expected duration 120s, got %d", sessions[0].DurationSeconds)
	}
}

func TestEngineTickRecordsPomodoroSession(t *testing.T) {
	engine, database := setupTestEngine(t)
	engine.SetDurations(25, 5)
	engine.Start("API Architecture", db.ModePomodoro, "client-1")
	rewind(engine, 26*time.Minute) // completed one minute ago

	engine.tick("")

	snap := engine.Snapshot()
	if snap.Phase != PhaseBreak || snap.Status != StatusPaused {
		t.Errorf("expected paused break, got %s-%s", snap.Phase, snap.Status)
	}
	if snap.AlertType != AlertWorkCompleted || snap.AlertID == 0 {
		t.Errorf("expected a work_completed alert, got %q (%d)", snap.AlertType, snap.AlertID)
	}
	if snap.RemainingSeconds != 5*60 {
		t.Errorf("expected a fresh 5 min break, got %ds", snap.RemainingSeconds)
	}

	sessions, err := database.GetAllSessions()
	if err != nil {
		t.Fatalf("failed to get sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session recorded, got %d", len(sessions))
	}
	if sessions[0].DurationSeconds != 25*60 {
		t.Errorf("expected duration 1500s, got %d", sessions[0].DurationSeconds)
	}
	// The session ends when the countdown reached zero, not at the late tick.
	if late := time.Since(sessions[0].EndTime); late < 55*time.Second || late > 65*time.Second {
		t.Errorf("expected the session to end ~1 min ago, got %v ago", late)
	}
}

func TestEnginePauseKeepsSubSecondPrecision(t *testing.T) {
	engine, _ := setupTestEngine(t)
	engine.Start("Precise", db.ModeFree, "")
	for range 4 {
		rewind(engine, 1500*time.Millisecond)
		engine.Pause()
		engine.Resume("")
	}
	if got := engine.Snapshot().ElapsedSeconds; got != 6 {
		t.Errorf("expected 6s elapsed over four 1.5s runs, got %d", got)
	}
}

func TestEngineNextAndBreakNotRecorded(t *testing.T) {
	engine, database := setupTestEngine(t)
	engine.Start("Cycle", db.ModePomodoro, "")
	rewind(engine, 10*time.Minute)

	snap := engine.Next()
	if snap.Phase != PhaseBreak || snap.Status != StatusPaused {
		t.Fatalf("expected paused break after next, got %s", snap.State)
	}
	engine.Resume("")
	rewind(engine, 3*time.Minute)
	if snap := engine.Stop(); snap.Status != StatusIdle || snap.Phase != PhaseWork {
		t.Fatalf("expected idle work after stop, got %s", snap.State)
	}

	sessions, _ := database.GetAllSessions()
	if len(sessions) != 1 || sessions[0].DurationSeconds != 600 {
		t.Fatalf("expected only the 10 min work phase recorded, got %+v", sessions)
	}
}

func TestEngineToggleAndCancel(t *testing.T) {
	engine, database := setupTestEngine(t)
	if s := engine.Toggle("a"); s.Status != StatusRunning || s.OriginClientID != "a" {
		t.Fatalf("toggle from idle should start, got %+v", s)
	}
	if s := engine.Toggle("a"); s.Status != StatusPaused {
		t.Fatalf("toggle while running should pause, got %s", s.Status)
	}
	if s := engine.Toggle("b"); s.Status != StatusRunning || s.OriginClientID != "b" {
		t.Fatalf("toggle while paused should resume, got %+v", s)
	}
	rewind(engine, time.Minute)
	if s := engine.Cancel(); s.Status != StatusIdle {
		t.Fatalf("cancel should go idle, got %s", s.Status)
	}
	if sessions, _ := database.GetAllSessions(); len(sessions) != 0 {
		t.Fatalf("cancel must not record, got %d sessions", len(sessions))
	}
}

func TestEngineSetModeIgnoredWhileActive(t *testing.T) {
	engine, _ := setupTestEngine(t)
	engine.Start("", db.ModePomodoro, "")
	if s := engine.SetMode(db.ModeFree, ""); s.Mode != db.ModePomodoro {
		t.Fatalf("mode changed during an active session")
	}
	engine.Cancel()
	if s := engine.SetMode(db.ModeFree, "Other"); s.Mode != db.ModeFree || s.Project != "Other" {
		t.Fatalf("expected free mode on Other, got %s on %s", s.Mode, s.Project)
	}
}

func TestEngineDurationsPersistAndClamp(t *testing.T) {
	engine, database := setupTestEngine(t)
	snap := engine.SetDurations(45, 100000)
	if snap.WorkDurationMin != 45 || snap.BreakDurationMin != MaxDurationMin {
		t.Fatalf("unexpected durations %d/%d", snap.WorkDurationMin, snap.BreakDurationMin)
	}
	if snap.TotalSeconds != 45*60 || snap.TimeStr != "45:00" {
		t.Fatalf("idle countdown should follow the work duration, got %d (%s)", snap.TotalSeconds, snap.TimeStr)
	}
	restarted := NewEngine(database, "")
	defer restarted.Close()
	if s := restarted.Snapshot(); s.WorkDurationMin != 45 {
		t.Fatalf("work duration not persisted, got %d", s.WorkDurationMin)
	}
}

func TestSubscribersAlwaysGetLatestSnapshot(t *testing.T) {
	engine, _ := setupTestEngine(t)
	ch := engine.Subscribe()
	defer engine.Unsubscribe(ch)

	// Nobody reads while several changes happen: only the latest is kept.
	engine.Start("A", db.ModePomodoro, "")
	engine.Pause()
	engine.SetProject("B")

	select {
	case snap := <-ch:
		if snap.Project != "B" || snap.Status != StatusPaused {
			t.Fatalf("expected latest snapshot (B, paused), got %s %s", snap.Project, snap.Status)
		}
	case <-time.After(time.Second):
		t.Fatal("no snapshot received")
	}
}
