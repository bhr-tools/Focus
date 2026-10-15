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

type StateSnapshot struct {
	State            string `json:"state"`            // "work-running", "work-paused", "break-running", "break-paused", "free-running", "free-paused", "idle"
	Status           string `json:"status"`           // "running", "paused", "idle"
	Mode             string `json:"mode"`             // "pomodoro", "free"
	Phase            string `json:"phase"`            // "work", "break"
	Project          string `json:"project"`          // Project name
	ProjectColor     string `json:"project_color"`    // Real Project Rosé Pine color (not altered during break)
	Color            string `json:"color"`            // Project Rosé Pine color or break foam color
	Text             string `json:"text"`             // Formatted text for bars e.g. "Work: 58m"
	TimeStr          string `json:"time_str"`         // MM:SS or HH:MM:SS
	ElapsedSeconds   int    `json:"elapsed_seconds"`  // Elapsed in current phase
	RemainingSeconds int    `json:"remaining_seconds"`// Remaining in countdown
	TotalSeconds     int    `json:"total_seconds"`    // Phase duration (pomodoro)
	Percent          int    `json:"percent"`          // 0 - 100
	Tooltip          string `json:"tooltip"`          // Detailed tooltip for waybar
	Theme            string `json:"theme"`            // "dark" or "light" (synced from ~/.config/theme_state)
	LastRecordedAt   int64  `json:"last_recorded_at"` // Unix timestamp in ms of last saved activity
	AlertID          int64  `json:"alert_id,omitempty"`       // Unix timestamp in ms of session completion alert
	AlertType        string `json:"alert_type,omitempty"`     // "work_completed" or "break_ended"
	OriginClientID   string `json:"origin_client_id,omitempty"` // ID of the client that started this session
}

type Engine struct {
	mu sync.RWMutex
	db *db.DB

	mode             string // "pomodoro", "free"
	phase            string // "work", "break"
	status           string // "idle", "running", "paused"
	project          string
	projectColor     string

	workDurationMin  int
	breakDurationMin int
	totalSeconds     int
	elapsedSeconds   int
	remainingSeconds int

	sessionStartTime time.Time
	activeWorkStart  time.Time
	accumulatedWorkSec int

	lastRecordedAt int64
	lastAlertID    int64
	alertType      string
	originClientID string
	lastTheme   string
	subscribers map[chan StateSnapshot]struct{}
	stopChan    chan struct{}
}

func NewEngine(database *db.DB) *Engine {
	// Read defaults from DB settings
	workDur := 60
	breakDur := 10
	if settings, err := database.GetSettings(); err == nil {
		if w, ok := settings["work_duration"]; ok && w != "" {
			var val int
			if _, err := fmt.Sscanf(w, "%d", &val); err == nil && val > 0 {
				workDur = val
			}
		}
		if b, ok := settings["break_duration"]; ok && b != "" {
			var val int
			if _, err := fmt.Sscanf(b, "%d", &val); err == nil && val > 0 {
				breakDur = val
			}
		}
	}

	// Default project: pick the last project tracked
	defProj, _ := database.GetLastProject()
	if defProj == "" {
		defProj = "Work"
	}
	defColor := db.RosePinePalette[0]
	if p, err := database.GetOrCreateProject(defProj); err == nil {
		defColor = p.Color
	}

	e := &Engine{
		db:               database,
		mode:             "pomodoro",
		phase:            "work",
		status:           "idle",
		project:          defProj,
		projectColor:     defColor,
		workDurationMin:  workDur,
		breakDurationMin: breakDur,
		totalSeconds:     workDur * 60,
		elapsedSeconds:   0,
		remainingSeconds: workDur * 60,
		lastTheme:        getSystemTheme(),
		subscribers:      make(map[chan StateSnapshot]struct{}),
		stopChan:         make(chan struct{}),
	}

	// Write initial state to file
	e.writeStateFile(e.Snapshot())

	// Start ticker loop
	go e.runLoop()

	return e
}

func (e *Engine) Close() {
	close(e.stopChan)
}

func (e *Engine) Subscribe() chan StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	ch := make(chan StateSnapshot, 10)
	e.subscribers[ch] = struct{}{}
	ch <- e.snapshotLocked()
	return ch
}

func (e *Engine) Unsubscribe(ch chan StateSnapshot) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.subscribers, ch)
	close(ch)
}

func (e *Engine) runLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-e.stopChan:
			return
		case <-ticker.C:
			// Check theme sync continuously, even when timer is idle
			curTheme := getSystemTheme()
			e.mu.Lock()
			themeChanged := false
			if curTheme != e.lastTheme {
				e.lastTheme = curTheme
				themeChanged = true
			}
			isRunning := (e.status == "running")
			e.mu.Unlock()

			if themeChanged {
				snap := e.Snapshot()
				e.broadcast(snap)
				e.writeStateFile(snap)
			}

			if isRunning {
				e.tick()
			}
		}
	}
}

func (e *Engine) tick() {
	e.mu.Lock()
	if e.status != "running" {
		e.mu.Unlock()
		return
	}

	var sessionCommitted bool
	if e.mode == "pomodoro" {
		e.elapsedSeconds++
		e.remainingSeconds = e.totalSeconds - e.elapsedSeconds

		if e.phase == "work" {
			e.accumulatedWorkSec++
		}

		if e.remainingSeconds <= 0 {
			// Phase finished!
			if e.phase == "work" {
				// Work session over
				workStart := e.sessionStartTime
				workEnd := time.Now()
				dur := e.elapsedSeconds
				proj := e.project

				if _, err := e.db.RecordSession(db.Session{
					ProjectName:     proj,
					Mode:            "pomodoro",
					StartTime:       workStart,
					EndTime:         workEnd,
					DurationSeconds: dur,
					Completed:       true,
				}); err != nil {
					log.Printf("Error recording session: %v", err)
				} else {
					sessionCommitted = true
				}
				e.lastRecordedAt = time.Now().UnixMilli()
				e.lastAlertID = time.Now().UnixMilli()
				e.alertType = "work_completed"
				go e.triggerAlarm("Deepwork session over", "Time for a break.")

				// Transition to break (paused, wait for user to start)
				e.phase = "break"
				e.totalSeconds = e.breakDurationMin * 60
				e.elapsedSeconds = 0
				e.remainingSeconds = e.totalSeconds
				e.status = "paused"
				e.sessionStartTime = time.Now()
				e.accumulatedWorkSec = 0
			} else {
				// Break over
				e.lastAlertID = time.Now().UnixMilli()
				e.alertType = "break_ended"
				go func() {
					e.triggerAlarm("Break over", "Prepare a new session for deepwork.")
				}()

				// Transition to work (paused, wait for user to start)
				e.phase = "work"
				e.totalSeconds = e.workDurationMin * 60
				e.elapsedSeconds = 0
				e.remainingSeconds = e.totalSeconds
				e.status = "paused"
				e.sessionStartTime = time.Now()
				e.accumulatedWorkSec = 0
			}
		}
	} else if e.mode == "free" {
		e.elapsedSeconds++
		e.accumulatedWorkSec++
		e.remainingSeconds = 0
	}

	snap := e.snapshotLocked()
	e.mu.Unlock()

	if sessionCommitted {
		e.NotifyActivityRecorded()
	} else {
		e.broadcast(snap)
		e.writeStateFile(snap)
	}
}

func (e *Engine) Snapshot() StateSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.snapshotLocked()
}

func (e *Engine) snapshotLocked() StateSnapshot {
	// Determine composite state string
	var stateStr string
	if e.status == "idle" {
		stateStr = "idle"
	} else {
		if e.mode == "free" {
			stateStr = fmt.Sprintf("free-%s", e.status)
		} else {
			stateStr = fmt.Sprintf("%s-%s", e.phase, e.status)
		}
	}

	// Format text & timeStr
	var timeStr, text, tooltip string
	var percent int

	if e.status == "idle" {
		if e.mode == "pomodoro" {
			remMin := e.totalSeconds / 60
			text = fmt.Sprintf("Work: %dmin", remMin)
			timeStr = fmt.Sprintf("%02d:00", remMin)
		} else {
			text = "Stopwatch: 0min"
			timeStr = "00:00"
		}
		tooltip = fmt.Sprintf("Project: %s\nMode: %s (idle)", e.project, e.mode)
		percent = 0
	} else if e.mode == "pomodoro" {
		remSec := e.remainingSeconds
		if remSec < 0 {
			remSec = 0
		}
		mins := remSec / 60
		secs := remSec % 60
		timeStr = fmt.Sprintf("%02d:%02d", mins, secs)

		// Use ceil for countdown text so 0min only shows when 0s remain
		displayMins := (remSec + 59) / 60
		if remSec == 0 {
			displayMins = 0
		}

		if e.phase == "break" {
			text = fmt.Sprintf("Break: %dmin", displayMins)
		} else {
			text = fmt.Sprintf("Work: %dmin", displayMins)
		}

		if e.totalSeconds > 0 {
			percent = int((float64(e.elapsedSeconds) / float64(e.totalSeconds)) * 100)
			if percent > 100 {
				percent = 100
			}
		}

		tooltip = fmt.Sprintf("Project: %s\nPhase: %s (%s)\nRemaining: %s / %dm",
			e.project, capitalize(e.phase), e.status, timeStr, e.totalSeconds/60)
	} else { // free stopwatch timer
		totalSec := e.elapsedSeconds
		mins := totalSec / 60
		secs := totalSec % 60
		hrs := totalSec / 3600

		if hrs > 0 {
			timeStr = fmt.Sprintf("%02d:%02d:%02d", hrs, (totalSec%3600)/60, secs)
		} else {
			timeStr = fmt.Sprintf("%02d:%02d", mins, secs)
		}
		text = fmt.Sprintf("Stopwatch: %dmin", mins)
		percent = 100
		tooltip = fmt.Sprintf("Project: %s\nMode: Stopwatch (%s)\nTime: %s",
			e.project, e.status, timeStr)
	}

	color := e.projectColor
	if e.phase == "break" && e.mode == "pomodoro" {
		color = "#9ccfd8" // Foam accent for break
	}

	return StateSnapshot{
		State:            stateStr,
		Status:           e.status,
		Mode:             e.mode,
		Phase:            e.phase,
		Project:          e.project,
		ProjectColor:     e.projectColor,
		Color:            color,
		Text:             text,
		TimeStr:          timeStr,
		ElapsedSeconds:   e.elapsedSeconds,
		RemainingSeconds: e.remainingSeconds,
		TotalSeconds:     e.totalSeconds,
		Percent:          percent,
		Tooltip:          tooltip,
		Theme:            getSystemTheme(),
		LastRecordedAt:   e.lastRecordedAt,
		AlertID:          e.lastAlertID,
		AlertType:        e.alertType,
		OriginClientID:   e.originClientID,
	}
}

func (e *Engine) broadcast(snap StateSnapshot) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for ch := range e.subscribers {
		select {
		case ch <- snap:
		default:
		}
	}
}

func (e *Engine) writeStateFile(snap StateSnapshot) {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return
	}

	statePath := "/tmp/focus_state.json"
	tmpPath := statePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err == nil {
		_ = os.Rename(tmpPath, statePath)
	}
	// Also maintain legacy path for smooth transition
	_ = os.WriteFile("/tmp/timetracker_state.json", data, 0644)
}

func (e *Engine) triggerAlarm(title, message string) {
	// 1. Play sound on host hardware ONLY if explicitly enabled in settings AND started from CLI/host
	settings, _ := e.db.GetSettings()
	alarmEnabled := settings != nil && (settings["alarm_enabled"] == "true" || settings["server_sound"] == "true")
	if alarmEnabled && (e.originClientID == "cli" || e.originClientID == "") {
		home, _ := os.UserHomeDir()
		alarmPath := filepath.Join(home, ".config", "deepwork-alarm.mp3")
		go func() {
			if _, err := os.Stat(alarmPath); err == nil {
				_ = exec.Command("pw-play", alarmPath).Run()
			} else {
				// Fallback system beep or paplay
				_ = exec.Command("canberra-gtk-play", "-i", "complete").Run()
			}
		}()
	}

	// 2. Desktop notification
	go func() {
		_ = exec.Command("notify-send", "--category=system_alert", title, message).Run()
	}()
}

// Public commands

func (e *Engine) Start(project, mode, originClientID string) StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()

	if project != "" {
		e.project = project
		if p, err := e.db.GetOrCreateProject(project); err == nil {
			e.projectColor = p.Color
		}
	}

	if mode == "free" || mode == "pomodoro" {
		e.mode = mode
	}

	e.originClientID = originClientID
	e.status = "running"
	e.lastAlertID = 0
	e.alertType = ""
	e.sessionStartTime = time.Now()
	e.activeWorkStart = time.Now()

	if e.mode == "pomodoro" {
		e.phase = "work"
		e.totalSeconds = e.workDurationMin * 60
		e.elapsedSeconds = 0
		e.remainingSeconds = e.totalSeconds
	} else {
		e.phase = "work"
		e.totalSeconds = 0
		e.elapsedSeconds = 0
		e.remainingSeconds = 0
	}

	snap := e.snapshotLocked()
	go e.broadcast(snap)
	go e.writeStateFile(snap)
	return snap
}

func (e *Engine) Pause() StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.status == "running" {
		e.status = "paused"
	}

	snap := e.snapshotLocked()
	go e.broadcast(snap)
	go e.writeStateFile(snap)
	return snap
}

func (e *Engine) Resume(originClientID string) StateSnapshot {
	e.mu.Lock()
	if e.status == "idle" {
		e.mu.Unlock()
		return e.Start(e.project, e.mode, originClientID)
	}

	if e.status == "paused" {
		e.status = "running"
		if originClientID != "" {
			e.originClientID = originClientID
		}
		e.lastAlertID = 0
		e.alertType = ""
		e.activeWorkStart = time.Now()
	}

	snap := e.snapshotLocked()
	e.mu.Unlock()

	go e.broadcast(snap)
	go e.writeStateFile(snap)
	return snap
}

func (e *Engine) Toggle(originClientID string) StateSnapshot {
	e.mu.RLock()
	st := e.status
	e.mu.RUnlock()

	if st == "running" {
		return e.Pause()
	} else if st == "paused" {
		return e.Resume(originClientID)
	} else {
		return e.Start(e.project, e.mode, originClientID)
	}
}

func (e *Engine) Stop() StateSnapshot {
	e.mu.Lock()

	var sessionCommitted bool
	if (e.mode == "free" || (e.mode == "pomodoro" && e.phase == "work")) && e.elapsedSeconds > 0 {
		workStart := e.sessionStartTime
		workEnd := time.Now()
		dur := e.elapsedSeconds
		proj := e.project
		mod := e.mode

		if _, err := e.db.RecordSession(db.Session{
			ProjectName:     proj,
			Mode:            mod,
			StartTime:       workStart,
			EndTime:         workEnd,
			DurationSeconds: dur,
			Completed:       true,
		}); err != nil {
			log.Printf("Error recording session: %v", err)
		} else {
			sessionCommitted = true
		}
		e.lastRecordedAt = time.Now().UnixMilli()
	}

	e.status = "idle"
	e.lastAlertID = 0
	e.alertType = ""
	e.originClientID = ""
	e.elapsedSeconds = 0
	if e.mode == "pomodoro" {
		e.phase = "work"
		e.totalSeconds = e.workDurationMin * 60
		e.remainingSeconds = e.totalSeconds
	} else {
		e.remainingSeconds = 0
		e.totalSeconds = 0
	}

	snap := e.snapshotLocked()
	e.mu.Unlock()

	if sessionCommitted {
		e.NotifyActivityRecorded()
	} else {
		go e.broadcast(snap)
		go e.writeStateFile(snap)
	}
	return snap
}

func (e *Engine) Next() StateSnapshot {
	e.mu.Lock()
	if e.mode != "pomodoro" {
		e.mu.Unlock()
		return e.Stop()
	}

	var sessionCommitted bool
	if e.phase == "work" {
		// Save current work session if any time elapsed
		if e.elapsedSeconds > 0 {
			workStart := e.sessionStartTime
			workEnd := time.Now()
			dur := e.elapsedSeconds
			proj := e.project
			if _, err := e.db.RecordSession(db.Session{
				ProjectName:     proj,
				Mode:            "pomodoro",
				StartTime:       workStart,
				EndTime:         workEnd,
				DurationSeconds: dur,
				Completed:       true,
			}); err != nil {
				log.Printf("Error recording session: %v", err)
			} else {
				sessionCommitted = true
			}
			e.lastRecordedAt = time.Now().UnixMilli()
		}
		e.phase = "break"
		e.totalSeconds = e.breakDurationMin * 60
		e.elapsedSeconds = 0
		e.remainingSeconds = e.totalSeconds
		e.status = "paused" // Do not auto-start
		e.lastAlertID = 0
		e.alertType = ""
		e.sessionStartTime = time.Now()
	} else {
		// From break to work
		e.phase = "work"
		e.totalSeconds = e.workDurationMin * 60
		e.elapsedSeconds = 0
		e.remainingSeconds = e.totalSeconds
		e.status = "paused" // Do not auto-start
		e.lastAlertID = 0
		e.alertType = ""
		e.sessionStartTime = time.Now()
	}

	snap := e.snapshotLocked()
	e.mu.Unlock()

	if sessionCommitted {
		e.NotifyActivityRecorded()
	} else {
		go e.broadcast(snap)
		go e.writeStateFile(snap)
	}
	return snap
}

func (e *Engine) Cancel() StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.status = "idle"
	e.lastAlertID = 0
	e.alertType = ""
	e.originClientID = ""
	e.elapsedSeconds = 0
	if e.mode == "pomodoro" {
		e.phase = "work"
		e.totalSeconds = e.workDurationMin * 60
		e.remainingSeconds = e.totalSeconds
	} else {
		e.totalSeconds = 0
		e.remainingSeconds = 0
	}

	snap := e.snapshotLocked()
	go e.broadcast(snap)
	go e.writeStateFile(snap)
	return snap
}

func (e *Engine) SetMode(mode, project string) StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()

	// If timer is active (running or paused), prevent changing mode
	if e.status != "idle" {
		return e.snapshotLocked()
	}

	if project != "" {
		e.project = project
		if p, err := e.db.GetOrCreateProject(project); err == nil {
			e.projectColor = p.Color
		}
	}

	if mode == "free" || mode == "pomodoro" {
		e.mode = mode
	}

	e.status = "idle"
	e.originClientID = ""
	e.elapsedSeconds = 0
	if e.mode == "pomodoro" {
		e.phase = "work"
		e.totalSeconds = e.workDurationMin * 60
		e.remainingSeconds = e.totalSeconds
	} else {
		e.phase = "work"
		e.totalSeconds = 0
		e.remainingSeconds = 0
	}

	snap := e.snapshotLocked()
	go e.broadcast(snap)
	go e.writeStateFile(snap)
	return snap
}

func (e *Engine) SetDurations(workMin, breakMin int) StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()

	if workMin > 0 {
		e.workDurationMin = workMin
		_ = e.db.SetSetting("work_duration", fmt.Sprintf("%d", workMin))
	}
	if breakMin > 0 {
		e.breakDurationMin = breakMin
		_ = e.db.SetSetting("break_duration", fmt.Sprintf("%d", breakMin))
	}

	if (e.status == "idle" || (e.status == "paused" && e.elapsedSeconds == 0)) && e.mode == "pomodoro" {
		if e.phase == "work" {
			e.totalSeconds = e.workDurationMin * 60
			e.remainingSeconds = e.totalSeconds
		} else {
			e.totalSeconds = e.breakDurationMin * 60
			e.remainingSeconds = e.totalSeconds
		}
	}

	snap := e.snapshotLocked()
	go e.broadcast(snap)
	go e.writeStateFile(snap)
	return snap
}

func (e *Engine) SetProject(name string) StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()

	if name != "" {
		e.project = name
		if p, err := e.db.GetOrCreateProject(name); err == nil {
			e.projectColor = p.Color
		}
	}

	snap := e.snapshotLocked()
	go e.broadcast(snap)
	go e.writeStateFile(snap)
	return snap
}

func capitalize(s string) string {
	if len(s) == 0 {
		return ""
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] = r[0] - ('a' - 'A')
	}
	return string(r)
}

func getSystemTheme() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "dark"
	}
	content, err := os.ReadFile(filepath.Join(home, ".config", "theme_state"))
	if err == nil {
		val := strings.TrimSpace(string(content))
		if val == "light" || val == "dark" {
			return val
		}
	}
	return "dark"
}

func (e *Engine) NotifyActivityRecorded() {
	e.mu.Lock()
	e.lastRecordedAt = time.Now().UnixMilli()
	snap := e.snapshotLocked()
	e.mu.Unlock()
	e.broadcast(snap)
	e.writeStateFile(snap)
}
