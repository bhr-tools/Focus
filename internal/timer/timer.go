// Package timer holds the single Pomodoro / stopwatch state machine shared by
// every client (web UI, CLI, status bar).
package timer

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"focus/internal/db"
)

// Statuses and phases.
const (
	StatusIdle    = "idle"
	StatusRunning = "running"
	StatusPaused  = "paused"

	PhaseWork  = "work"
	PhaseBreak = "break"

	AlertWorkCompleted = "work_completed"
	AlertBreakEnded    = "break_ended"

	// MaxDurationMin bounds the configurable work and break durations.
	MaxDurationMin = 24 * 60

	breakColor = "#9ccfd8" // Rosé Pine foam
)

type StateSnapshot struct {
	State            string `json:"state"`  // "idle", "work-running", "break-paused", "free-running", ...
	Status           string `json:"status"` // "idle", "running", "paused"
	Mode             string `json:"mode"`   // "pomodoro", "free"
	Phase            string `json:"phase"`  // "work", "break"
	Project          string `json:"project"`
	ProjectColor     string `json:"project_color"` // project color, even during a break
	Color            string `json:"color"`         // project color, or foam during a break
	Text             string `json:"text"`          // status bar text, e.g. "Work: 58min"
	TimeStr          string `json:"time_str"`      // MM:SS or HH:MM:SS
	ElapsedSeconds   int    `json:"elapsed_seconds"`
	RemainingSeconds int    `json:"remaining_seconds"`
	TotalSeconds     int    `json:"total_seconds"`
	Percent          int    `json:"percent"` // 0-100
	Tooltip          string `json:"tooltip"`
	WorkDurationMin  int    `json:"work_duration_min"`
	BreakDurationMin int    `json:"break_duration_min"`
	Theme            string `json:"theme"`            // "dark", "light" or "" (from ~/.config/theme_state)
	LastRecordedAt   int64  `json:"last_recorded_at"` // Unix ms of the last saved or edited session
	AlertID          int64  `json:"alert_id,omitempty"`
	AlertType        string `json:"alert_type,omitempty"`
	OriginClientID   string `json:"origin_client_id,omitempty"` // client that started the session
}

// StateFilePath is where the daemon mirrors its state for the CLI.
func StateFilePath() string {
	return filepath.Join(os.TempDir(), "focus_state.json")
}

type Engine struct {
	mu        sync.Mutex
	db        *db.DB
	statePath string // "" disables the state file

	mode         string
	phase        string
	status       string
	project      string
	projectColor string
	workMin      int
	breakMin     int

	accumulated time.Duration // time spent in the current phase before activeSince
	activeSince time.Time     // zero unless running

	lastRecordedAt int64
	alertID        int64
	alertType      string
	originClientID string
	theme          string

	subscribers map[chan StateSnapshot]struct{}
	stop        chan struct{}
	stopOnce    sync.Once
}

// NewEngine restores the durations and last project from the database and
// starts the ticker. statePath may be empty.
func NewEngine(database *db.DB, statePath string) *Engine {
	project, err := database.GetLastProject()
	if err != nil {
		log.Printf("timer: read last project: %v", err)
		project = db.DefaultProjectName
	}
	color := db.RosePinePalette[0]
	if p, err := database.GetOrCreateProject(project); err == nil {
		project, color = p.Name, p.Color
	}

	e := &Engine{
		db:           database,
		statePath:    statePath,
		mode:         db.ModePomodoro,
		phase:        PhaseWork,
		status:       StatusIdle,
		project:      project,
		projectColor: color,
		workMin:      clampDuration(database.GetIntSetting(db.SettingWorkDuration, 60)),
		breakMin:     clampDuration(database.GetIntSetting(db.SettingBreakDuration, 10)),
		theme:        readSystemTheme(),
		subscribers:  make(map[chan StateSnapshot]struct{}),
		stop:         make(chan struct{}),
	}
	e.mu.Lock()
	e.publishLocked()
	e.mu.Unlock()

	go e.runLoop()
	return e
}

func clampDuration(minutes int) int {
	return max(1, min(minutes, MaxDurationMin))
}

// Close stops the ticker. It is safe to call more than once.
func (e *Engine) Close() {
	e.stopOnce.Do(func() { close(e.stop) })
}

// Subscribe returns a channel receiving every new snapshot, starting with the
// current one. Slow subscribers only miss intermediate snapshots, never the latest.
func (e *Engine) Subscribe() chan StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	ch := make(chan StateSnapshot, 1)
	ch <- e.snapshotLocked()
	e.subscribers[ch] = struct{}{}
	return ch
}

func (e *Engine) Unsubscribe(ch chan StateSnapshot) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.subscribers[ch]; ok {
		delete(e.subscribers, ch)
		close(ch)
	}
}

func (e *Engine) runLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-ticker.C:
			e.tick(readSystemTheme())
		}
	}
}

// tick advances a running timer and publishes theme changes.
func (e *Engine) tick(theme string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	changed := theme != e.theme
	e.theme = theme
	if e.status == StatusRunning {
		if e.mode == db.ModePomodoro && e.elapsedLocked() >= e.phaseTotalLocked() {
			e.completePhaseLocked()
		}
		changed = true
	}
	if changed {
		e.publishLocked()
	}
}

// completePhaseLocked handles a countdown reaching zero: a finished work phase
// is recorded, then the next phase waits, paused, for the user.
func (e *Engine) completePhaseLocked() {
	total := e.phaseTotalLocked()
	// End of the phase, even if the tick comes late (e.g. after a suspend).
	end := time.Now().Add(total - e.elapsedLocked())
	if e.phase == PhaseWork {
		e.recordLocked(total, end)
		e.alertType = AlertWorkCompleted
		e.enterPhaseLocked(PhaseBreak)
	} else {
		e.alertType = AlertBreakEnded
		e.enterPhaseLocked(PhaseWork)
	}
	e.alertID = time.Now().UnixMilli()
	go e.triggerAlarm(e.alertType, e.originClientID)
}

func (e *Engine) elapsedLocked() time.Duration {
	if e.status == StatusRunning {
		return e.accumulated + time.Since(e.activeSince)
	}
	return e.accumulated
}

func (e *Engine) phaseTotalLocked() time.Duration {
	switch {
	case e.mode != db.ModePomodoro:
		return 0
	case e.phase == PhaseBreak:
		return time.Duration(e.breakMin) * time.Minute
	default:
		return time.Duration(e.workMin) * time.Minute
	}
}

// recordLocked saves a work session of the given length ending at end.
func (e *Engine) recordLocked(length time.Duration, end time.Time) {
	secs := int(length.Round(time.Second) / time.Second)
	if secs <= 0 {
		return
	}
	_, err := e.db.RecordSession(db.Session{
		ProjectName:     e.project,
		Mode:            e.mode,
		EndTime:         end,
		DurationSeconds: secs,
		Completed:       true,
	})
	if err != nil {
		log.Printf("timer: record session: %v", err)
		return
	}
	e.lastRecordedAt = time.Now().UnixMilli()
}

// enterPhaseLocked switches to a fresh, paused phase.
func (e *Engine) enterPhaseLocked(phase string) {
	e.phase = phase
	e.status = StatusPaused
	e.accumulated = 0
	e.activeSince = time.Time{}
}

// resetLocked returns to the idle state, keeping mode, project and durations.
func (e *Engine) resetLocked() {
	e.phase = PhaseWork
	e.status = StatusIdle
	e.accumulated = 0
	e.activeSince = time.Time{}
	e.originClientID = ""
	e.clearAlertLocked()
}

func (e *Engine) clearAlertLocked() {
	e.alertID = 0
	e.alertType = ""
}

func (e *Engine) setProjectLocked(name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	p, err := e.db.GetOrCreateProject(name)
	if err != nil {
		log.Printf("timer: set project: %v", err)
		return
	}
	e.project, e.projectColor = p.Name, p.Color
}

func (e *Engine) setModeLocked(mode string) {
	if mode == db.ModePomodoro || mode == db.ModeFree {
		e.mode = mode
	}
}

func (e *Engine) startLocked(project, mode, origin string) {
	e.setProjectLocked(project)
	e.setModeLocked(mode)
	e.resetLocked()
	e.status = StatusRunning
	e.activeSince = time.Now()
	e.originClientID = origin
}

func (e *Engine) pauseLocked() {
	if e.status != StatusRunning {
		return
	}
	e.accumulated += time.Since(e.activeSince)
	e.activeSince = time.Time{}
	e.status = StatusPaused
}

func (e *Engine) resumeLocked(origin string) {
	switch e.status {
	case StatusIdle:
		e.startLocked("", "", origin)
	case StatusPaused:
		e.status = StatusRunning
		e.activeSince = time.Now()
		if origin != "" {
			e.originClientID = origin
		}
		e.clearAlertLocked()
	}
}

// ==================== Public commands ====================

// Start begins a new work phase (or stopwatch), discarding any current one.
func (e *Engine) Start(project, mode, origin string) StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.startLocked(project, mode, origin)
	return e.publishLocked()
}

func (e *Engine) Pause() StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pauseLocked()
	return e.publishLocked()
}

func (e *Engine) Resume(origin string) StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resumeLocked(origin)
	return e.publishLocked()
}

// Toggle starts, pauses or resumes depending on the current status.
func (e *Engine) Toggle(origin string) StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.status == StatusRunning {
		e.pauseLocked()
	} else {
		e.resumeLocked(origin)
	}
	return e.publishLocked()
}

// Stop records the current work (stopwatch or Pomodoro work phase) and goes idle.
func (e *Engine) Stop() StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.status != StatusIdle && (e.mode == db.ModeFree || e.phase == PhaseWork) {
		e.recordLocked(e.elapsedLocked(), time.Now())
	}
	e.resetLocked()
	return e.publishLocked()
}

// Next records the current work phase and switches phase, paused. In
// stopwatch mode it is equivalent to Stop.
func (e *Engine) Next() StateSnapshot {
	e.mu.Lock()
	if e.mode != db.ModePomodoro {
		e.mu.Unlock()
		return e.Stop()
	}
	defer e.mu.Unlock()
	if e.phase == PhaseWork {
		e.recordLocked(e.elapsedLocked(), time.Now())
		e.enterPhaseLocked(PhaseBreak)
	} else {
		e.enterPhaseLocked(PhaseWork)
	}
	e.clearAlertLocked()
	return e.publishLocked()
}

// Cancel goes idle without recording anything.
func (e *Engine) Cancel() StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resetLocked()
	return e.publishLocked()
}

// SetMode changes mode and project; ignored while a session is active.
func (e *Engine) SetMode(mode, project string) StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.status == StatusIdle {
		e.setProjectLocked(project)
		e.setModeLocked(mode)
		e.resetLocked()
	}
	return e.publishLocked()
}

// SetDurations persists the Pomodoro durations (minutes); non-positive values
// are ignored. They apply immediately unless the current phase has started.
func (e *Engine) SetDurations(workMin, breakMin int) StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	if workMin > 0 {
		e.workMin = clampDuration(workMin)
		if err := e.db.SetSetting(db.SettingWorkDuration, fmt.Sprint(e.workMin)); err != nil {
			log.Printf("timer: save work duration: %v", err)
		}
	}
	if breakMin > 0 {
		e.breakMin = clampDuration(breakMin)
		if err := e.db.SetSetting(db.SettingBreakDuration, fmt.Sprint(e.breakMin)); err != nil {
			log.Printf("timer: save break duration: %v", err)
		}
	}
	return e.publishLocked()
}

func (e *Engine) SetProject(name string) StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.setProjectLocked(name)
	return e.publishLocked()
}

// ProjectRenamed makes the timer follow a renamed project, so that the
// current or next session is not recorded under the old name.
func (e *Engine) ProjectRenamed(oldName, newName string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if strings.EqualFold(e.project, strings.TrimSpace(oldName)) {
		e.setProjectLocked(newName)
		e.publishLocked()
	}
}

// Snapshot returns the current state.
func (e *Engine) Snapshot() StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshotLocked()
}

// NotifyActivityRecorded tells clients that sessions changed (edit, import...).
func (e *Engine) NotifyActivityRecorded() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastRecordedAt = time.Now().UnixMilli()
	e.publishLocked()
}

// ==================== Snapshot & publication ====================

// publishLocked sends the current snapshot to every subscriber and to the
// state file. Holding the lock guarantees snapshots are delivered in order.
func (e *Engine) publishLocked() StateSnapshot {
	snap := e.snapshotLocked()
	for ch := range e.subscribers {
		select {
		case <-ch: // drop the stale, unread snapshot
		default:
		}
		ch <- snap
	}
	e.writeStateFile(snap)
	return snap
}

func (e *Engine) snapshotLocked() StateSnapshot {
	elapsed := int(e.elapsedLocked() / time.Second)
	total := int(e.phaseTotalLocked() / time.Second)
	if e.mode == db.ModePomodoro {
		elapsed = min(elapsed, total)
	}

	snap := StateSnapshot{
		Status:           e.status,
		Mode:             e.mode,
		Phase:            e.phase,
		Project:          e.project,
		ProjectColor:     e.projectColor,
		Color:            e.projectColor,
		ElapsedSeconds:   elapsed,
		TotalSeconds:     total,
		WorkDurationMin:  e.workMin,
		BreakDurationMin: e.breakMin,
		Theme:            e.theme,
		LastRecordedAt:   e.lastRecordedAt,
		AlertID:          e.alertID,
		AlertType:        e.alertType,
		OriginClientID:   e.originClientID,
	}

	switch {
	case e.status == StatusIdle:
		snap.State = StatusIdle
	case e.mode == db.ModeFree:
		snap.State = "free-" + e.status
	default:
		snap.State = e.phase + "-" + e.status
	}

	if e.mode == db.ModeFree {
		snap.TimeStr = formatClock(elapsed)
		snap.Text = fmt.Sprintf("Stopwatch: %dmin", elapsed/60)
		snap.Percent = 100
		snap.Tooltip = fmt.Sprintf("Project: %s\nMode: Stopwatch (%s)\nTime: %s", e.project, e.status, snap.TimeStr)
		if e.status == StatusIdle {
			snap.Percent = 0
		}
		return snap
	}

	remaining := total - elapsed
	snap.RemainingSeconds = remaining
	snap.TimeStr = fmt.Sprintf("%02d:%02d", remaining/60, remaining%60) // countdowns stay MM:SS
	if total > 0 {
		snap.Percent = elapsed * 100 / total
	}
	label := "Work"
	if e.phase == PhaseBreak {
		label = "Break"
		snap.Color = breakColor
	}
	snap.Text = fmt.Sprintf("%s: %dmin", label, (remaining+59)/60) // rounded up: 0min only at 0s
	snap.Tooltip = fmt.Sprintf("Project: %s\nPhase: %s (%s)\nRemaining: %s / %dm", e.project, label, e.status, snap.TimeStr, total/60)
	return snap
}

// formatClock renders seconds as MM:SS, or HH:MM:SS from one hour.
func formatClock(secs int) string {
	secs = max(secs, 0)
	if secs >= 3600 {
		return fmt.Sprintf("%02d:%02d:%02d", secs/3600, secs%3600/60, secs%60)
	}
	return fmt.Sprintf("%02d:%02d", secs/60, secs%60)
}

// writeStateFile atomically replaces the state file.
func (e *Engine) writeStateFile(snap StateSnapshot) {
	if e.statePath == "" {
		return
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return
	}
	tmp := e.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, e.statePath)
}

// ==================== Host integration ====================

// readSystemTheme reads ~/.config/theme_state ("dark" or "light"); "" if unset.
func readSystemTheme() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	content, err := os.ReadFile(filepath.Join(home, ".config", "theme_state"))
	if err != nil {
		return ""
	}
	if v := strings.TrimSpace(string(content)); v == "light" || v == "dark" {
		return v
	}
	return ""
}

// AlertMessage returns the notification title and body of an alert type.
func AlertMessage(alertType string) (string, string) {
	if alertType == AlertBreakEnded {
		return "Break over", "Prepare a new session for deepwork."
	}
	return "Deepwork session over", "Time for a break."
}

// triggerAlarm sends a desktop notification and, for sessions started from
// the host (CLI or unknown origin), plays the configured alarm sound.
func (e *Engine) triggerAlarm(alertType, origin string) {
	title, body := AlertMessage(alertType)
	runIfAvailable("notify-send", "--category=system_alert", title, body)

	settings, err := e.db.GetSettings()
	if err != nil || settings[db.SettingAlarmEnabled] != "true" || (origin != "cli" && origin != "") {
		return
	}
	if sound := settings[db.SettingAlarmSound]; sound != "" {
		if _, err := os.Stat(sound); err == nil {
			runIfAvailable("pw-play", sound)
			return
		}
	}
	runIfAvailable("canberra-gtk-play", "-i", "complete")
}

func runIfAvailable(name string, args ...string) {
	if _, err := exec.LookPath(name); err != nil {
		return
	}
	if err := exec.Command(name, args...).Run(); err != nil {
		log.Printf("timer: %s: %v", name, err)
	}
}
