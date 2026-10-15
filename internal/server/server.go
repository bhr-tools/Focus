package server

import (
	"crypto/rand"
	"embed"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"focus/internal/db"
	"focus/internal/timer"
)

type Server struct {
	db         *db.DB
	engine     *timer.Engine
	assets     embed.FS
	mux        *http.ServeMux
	localToken string
}

func NewServer(database *db.DB, engine *timer.Engine, assets embed.FS) *Server {
	// Generate or load a local token for CLI / Waybar access
	localTok := os.Getenv("FOCUS_TOKEN")
	if localTok == "" {
		tokBytes := make([]byte, 16)
		_, _ = rand.Read(tokBytes)
		localTok = hex.EncodeToString(tokBytes)
	}

	// Save local token in /tmp/focus_local_token for CLI/desktop use
	_ = os.WriteFile("/tmp/focus_local_token", []byte(localTok), 0600)

	s := &Server{
		db:         database,
		engine:     engine,
		assets:     assets,
		mux:        http.NewServeMux(),
		localToken: localTok,
	}
	s.registerRoutes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) registerRoutes() {
	// Public auth & theme routes
	s.mux.HandleFunc("/api/auth/status", s.handleAuthStatus)
	s.mux.HandleFunc("/api/auth/setup", s.handleAuthSetup)
	s.mux.HandleFunc("/api/auth/login", s.handleAuthLogin)
	s.mux.HandleFunc("/api/theme", s.handleTheme)
	s.mux.HandleFunc("/api/audio/session.mp3", s.handleAudioSession)

	// Protected auth routes
	s.mux.HandleFunc("/api/auth/change-password", s.authed(s.handleAuthChangePassword))
	s.mux.HandleFunc("/api/auth/logout", s.authed(s.handleAuthLogout))

	// Protected API routes
	s.mux.HandleFunc("/api/state", s.authed(s.handleState))
	s.mux.HandleFunc("/api/events", s.authed(s.handleEvents))
	s.mux.HandleFunc("/api/action", s.authed(s.handleAction))
	s.mux.HandleFunc("/api/projects", s.authed(s.handleProjects))
	s.mux.HandleFunc("/api/projects/month", s.authed(s.handleMonthProjects))
	s.mux.HandleFunc("/api/projects/rename-month", s.authed(s.handleRenameProjectMonth))
	s.mux.HandleFunc("/api/projects/last", s.authed(s.handleLastProject))
	s.mux.HandleFunc("/api/sessions/today", s.authed(s.handleTodaySessions))
	s.mux.HandleFunc("/api/sessions", s.authed(s.handleSessionDetail))
	s.mux.HandleFunc("/api/sessions/", s.authed(s.handleSessionDetail))
	s.mux.HandleFunc("/api/month", s.authed(s.handleMonth))
	s.mux.HandleFunc("/api/stats", s.authed(s.handleStats))
	s.mux.HandleFunc("/api/stats/periods", s.authed(s.handleStatsPeriods))
	s.mux.HandleFunc("/api/settings", s.authed(s.handleSettings))
	s.mux.HandleFunc("/api/export", s.authed(s.handleExport))
	s.mux.HandleFunc("/api/import", s.authed(s.handleImport))

	// Embedded static files with strict no-cache headers for instantaneous mobile updates
	fileServer := http.FileServer(http.FS(s.assets))
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		fileServer.ServeHTTP(w, r)
	})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snap := s.engine.Snapshot()
	writeJSON(w, snap)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	// SSE (Server-Sent Events)
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := s.engine.Subscribe()
	defer s.engine.Unsubscribe(ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case snap, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(snap)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

type ActionPayload struct {
	Action        string `json:"action"` // start, pause, resume, toggle, stop, next, cancel, set_durations, set_project
	Project       string `json:"project,omitempty"`
	Mode          string `json:"mode,omitempty"`
	WorkDuration  int    `json:"work_duration,omitempty"`
	BreakDuration int    `json:"break_duration,omitempty"`
	ClientID      string `json:"client_id,omitempty"`
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ActionPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var snap timer.StateSnapshot
	switch req.Action {
	case "start":
		snap = s.engine.Start(req.Project, req.Mode, req.ClientID)
	case "pause":
		snap = s.engine.Pause()
	case "resume":
		snap = s.engine.Resume(req.ClientID)
	case "toggle":
		snap = s.engine.Toggle(req.ClientID)
	case "stop":
		snap = s.engine.Stop()
	case "next":
		snap = s.engine.Next()
	case "cancel":
		snap = s.engine.Cancel()
	case "set_durations":
		snap = s.engine.SetDurations(req.WorkDuration, req.BreakDuration)
	case "set_project":
		snap = s.engine.SetProject(req.Project)
	case "set_mode":
		snap = s.engine.SetMode(req.Mode, req.Project)
	default:
		http.Error(w, "Unknown action", http.StatusBadRequest)
		return
	}

	writeJSON(w, snap)
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		list, err := s.db.ListProjects()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, list)
	} else if r.Method == http.MethodPost {
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		p, err := s.db.GetOrCreateProject(body.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, p)
	} else {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRenameProjectMonth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Year    int    `json:"year"`
		Month   int    `json:"month"`
		OldName string `json:"old_name"`
		NewName string `json:"new_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.OldName) == "" || strings.TrimSpace(req.NewName) == "" {
		http.Error(w, "Project name cannot be empty", http.StatusBadRequest)
		return
	}
	if req.Year <= 0 || req.Month <= 0 || req.Month > 12 {
		http.Error(w, "Invalid year or month", http.StatusBadRequest)
		return
	}
	if err := s.db.RenameProjectForMonth(req.Year, req.Month, req.OldName, req.NewName); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (s *Server) handleTodaySessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessions, err := s.db.GetTodaySessions()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if sessions == nil {
		sessions = []db.Session{}
	}
	writeJSON(w, sessions)
}


func getPayloadField(payload map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := payload[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

func parseNumericTime(val float64) time.Time {
	// If val >= 1e11 or val <= -1e11 (100 billion), treat as milliseconds
	if val >= 1e11 || val <= -1e11 {
		msec := int64(val)
		return time.UnixMilli(msec)
	}
	sec := int64(val)
	nsec := int64((val - float64(sec)) * 1e9)
	return time.Unix(sec, nsec)
}

func parseFlexibleTime(val any) (time.Time, error) {
	if val == nil {
		return time.Time{}, fmt.Errorf("nil time value")
	}

	switch v := val.(type) {
	case time.Time:
		return v, nil
	case int64:
		return parseNumericTime(float64(v)), nil
	case int:
		return parseNumericTime(float64(v)), nil
	case int32:
		return parseNumericTime(float64(v)), nil
	case uint64:
		return parseNumericTime(float64(v)), nil
	case uint:
		return parseNumericTime(float64(v)), nil
	case float64:
		return parseNumericTime(v), nil
	case float32:
		return parseNumericTime(float64(v)), nil
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return parseNumericTime(f), nil
		}
		return time.Time{}, fmt.Errorf("invalid json number: %v", v)
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return time.Time{}, fmt.Errorf("empty time string")
		}

		// Check if it's a numeric unix timestamp string
		if num, err := strconv.ParseFloat(s, 64); err == nil && !strings.ContainsAny(s, "-: Tt") {
			return parseNumericTime(num), nil
		}

		// 1. RFC3339 formats (with timezone offset)
		rfcFormats := []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02T15:04:05-0700",
			"2006-01-02T15:04:05.999-0700",
		}
		for _, layout := range rfcFormats {
			if t, err := time.Parse(layout, s); err == nil {
				return t, nil
			}
		}

		// 2. ISO local formats from <input type="datetime-local"> & SQL formats (in time.Local)
		localFormats := []string{
			"2006-01-02T15:04:05.999999999",
			"2006-01-02T15:04:05.999",
			"2006-01-02T15:04:05",
			"2006-01-02T15:04",
			"2006-01-02 15:04:05.999999999",
			"2006-01-02 15:04:05.999",
			"2006-01-02 15:04:05",
			"2006-01-02 15:04",
			"2006-01-02",
		}
		for _, layout := range localFormats {
			if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
				return t, nil
			}
		}

		return time.Time{}, fmt.Errorf("unable to parse time string: %q", s)
	default:
		return time.Time{}, fmt.Errorf("unsupported time value type: %T", val)
	}
}

func (s *Server) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	idStr = strings.TrimPrefix(idStr, "/api/sessions")
	idStr = strings.Trim(idStr, "/")

	if r.Method == http.MethodPost && idStr == "" {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// 1. Accept both project_name and project
		projName := ""
		if val := getPayloadField(payload, "project_name", "project", "ProjectName", "projectName"); val != nil {
			projName = fmt.Sprint(val)
		}
		projName = strings.TrimSpace(projName)
		if projName == "" {
			projName = "Focus"
		}

		mode := "pomodoro"
		if val := getPayloadField(payload, "mode", "Mode"); val != nil {
			if mStr := strings.TrimSpace(fmt.Sprint(val)); mStr != "" {
				mode = mStr
			}
		}

		uuidStr := ""
		if val := getPayloadField(payload, "uuid", "UUID"); val != nil {
			uuidStr = strings.TrimSpace(fmt.Sprint(val))
		}

		noteStr := ""
		if val := getPayloadField(payload, "note", "Note"); val != nil {
			noteStr = strings.TrimSpace(fmt.Sprint(val))
		}

		// 2. Accept both start_time and startTime, end_time and endTime
		startVal := getPayloadField(payload, "start_time", "startTime", "StartTime", "start")
		endVal := getPayloadField(payload, "end_time", "endTime", "EndTime", "end")

		if startVal == nil || endVal == nil {
			http.Error(w, "Both start_time and end_time are required", http.StatusBadRequest)
			return
		}

		st, err1 := parseFlexibleTime(startVal)
		et, err2 := parseFlexibleTime(endVal)
		if err1 != nil || err2 != nil {
			http.Error(w, fmt.Sprintf("Invalid date format: start_err=%v, end_err=%v", err1, err2), http.StatusBadRequest)
			return
		}

		// Duration
		durationSec := 0
		if dVal := getPayloadField(payload, "duration_seconds", "duration", "durationSeconds"); dVal != nil {
			switch dv := dVal.(type) {
			case float64:
				durationSec = int(dv)
			case int:
				durationSec = dv
			case int64:
				durationSec = int(dv)
			case string:
				durationSec, _ = strconv.Atoi(dv)
			}
		}

		// 4. If end_time is before start_time, or if duration_seconds <= 0, calculate duration cleanly as int(et.Sub(st).Seconds()) (fallback to 60s minimum if zero)
		if et.Before(st) {
			st, et = et, st
			durationSec = int(et.Sub(st).Seconds())
		} else if durationSec <= 0 {
			durationSec = int(et.Sub(st).Seconds())
		}

		if durationSec <= 0 {
			durationSec = 60
			et = st.Add(60 * time.Second)
		}

		sess, err := s.db.RecordSession(db.Session{
			UUID:            uuidStr,
			ProjectName:     projName,
			Mode:            mode,
			StartTime:       st,
			EndTime:         et,
			DurationSeconds: durationSec,
			Completed:       true,
			Note:            noteStr,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		s.engine.NotifyActivityRecorded()
		writeJSON(w, sess)
		return
	}

	if r.Method == http.MethodDelete {
		if id, err := strconv.ParseInt(idStr, 10, 64); err == nil && id > 0 {
			if err := s.db.DeleteSession(id); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else if idStr != "" {
			if err := s.db.DeleteSessionByUUID(idStr); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else {
			http.Error(w, "Invalid session id", http.StatusBadRequest)
			return
		}
		s.engine.NotifyActivityRecorded()
		writeJSON(w, map[string]string{"status": "deleted"})
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid session id", http.StatusBadRequest)
		return
	}

	if r.Method == http.MethodPut || r.Method == http.MethodPost {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		projName := ""
		if val := getPayloadField(payload, "project_name", "project", "ProjectName", "projectName"); val != nil {
			projName = fmt.Sprint(val)
		}
		projName = strings.TrimSpace(projName)
		if projName == "" {
			projName = "Focus"
		}

		startVal := getPayloadField(payload, "start_time", "startTime", "StartTime", "start")
		endVal := getPayloadField(payload, "end_time", "endTime", "EndTime", "end")

		if startVal == nil || endVal == nil {
			http.Error(w, "Both start_time and end_time are required", http.StatusBadRequest)
			return
		}

		st, err1 := parseFlexibleTime(startVal)
		et, err2 := parseFlexibleTime(endVal)
		if err1 != nil || err2 != nil {
			http.Error(w, fmt.Sprintf("Invalid date format: start_err=%v, end_err=%v", err1, err2), http.StatusBadRequest)
			return
		}

		if et.Before(st) {
			st, et = et, st
		}
		if et.Equal(st) {
			et = st.Add(60 * time.Second)
		}

		sess, err := s.db.UpdateSession(id, projName, st, et)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.engine.NotifyActivityRecorded()
		writeJSON(w, sess)
	} else {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMonthProjects(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	year := now.Year()
	month := int(now.Month())

	if yStr := r.URL.Query().Get("year"); yStr != "" {
		if y, err := strconv.Atoi(yStr); err == nil && y > 2000 {
			year = y
		}
	}
	if mStr := r.URL.Query().Get("month"); mStr != "" {
		if m, err := strconv.Atoi(mStr); err == nil && m >= 1 && m <= 12 {
			month = m
		}
	}

	projects, err := s.db.GetMonthProjects(year, month)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if projects == nil {
		projects = []db.Project{}
	}
	writeJSON(w, projects)
}

func (s *Server) handleLastProject(w http.ResponseWriter, r *http.Request) {
	proj, err := s.db.GetLastProject()
	if err != nil {
		proj = "Work"
	}
	p, err := s.db.GetOrCreateProject(proj)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, p)
}

func (s *Server) handleMonth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	now := time.Now()
	year := now.Year()
	month := int(now.Month())

	if yStr := r.URL.Query().Get("year"); yStr != "" {
		if y, err := strconv.Atoi(yStr); err == nil && y > 2000 {
			year = y
		}
	}
	if mStr := r.URL.Query().Get("month"); mStr != "" {
		if m, err := strconv.Atoi(mStr); err == nil && m >= 1 && m <= 12 {
			month = m
		}
	}

	data, err := s.db.GetMonthData(year, month)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, data)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	period := r.URL.Query().Get("period") // "month", "year", "all"
	year := 0
	month := 0
	if yStr := r.URL.Query().Get("year"); yStr != "" {
		year, _ = strconv.Atoi(yStr)
	}
	if mStr := r.URL.Query().Get("month"); mStr != "" {
		month, _ = strconv.Atoi(mStr)
	}

	stats, err := s.db.GetStats(period, year, month)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, stats)
}

func (s *Server) handleStatsPeriods(w http.ResponseWriter, r *http.Request) {
	years, months, err := s.db.GetAvailablePeriods()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"years":  years,
		"months": months,
	})
}

func (s *Server) handleTheme(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	theme := "dark"
	if b, err := os.ReadFile(filepath.Join(home, ".config", "theme_state")); err == nil {
		t := strings.TrimSpace(string(b))
		if t == "light" || t == "dark" {
			theme = t
		}
	}
	writeJSON(w, map[string]string{"theme": theme})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		settings, err := s.db.GetSettings()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, settings)
	} else if r.Method == http.MethodPost {
		var updates map[string]string
		if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for k, v := range updates {
			_ = s.db.SetSetting(k, v)
		}
		settings, _ := s.db.GetSettings()
		writeJSON(w, settings)
	} else {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

// Authentication & Session Protection

func (s *Server) isAuthenticated(r *http.Request) bool {
	hasPwd, err := s.db.HasPassword()
	if err != nil || !hasPwd {
		return true // No password configured yet
	}

	// 1. Authorization: Bearer <tok>
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		tok := strings.TrimPrefix(authHeader, "Bearer ")
		if tok == s.localToken || s.db.ValidateSession(tok) {
			return true
		}
	}

	// 2. X-Focus-Token header
	if customTok := r.Header.Get("X-Focus-Token"); customTok != "" {
		if customTok == s.localToken || s.db.ValidateSession(customTok) {
			return true
		}
	}

	// 3. Query param token
	if qTok := r.URL.Query().Get("token"); qTok != "" {
		if qTok == s.localToken || s.db.ValidateSession(qTok) {
			return true
		}
	}

	// 4. Session Cookie
	if cookie, err := r.Cookie("focus_session"); err == nil && cookie.Value != "" {
		if cookie.Value == s.localToken || s.db.ValidateSession(cookie.Value) {
			return true
		}
	}

	return false
}

func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isAuthenticated(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	hasPwd, _ := s.db.HasPassword()
	authed := s.isAuthenticated(r)
	writeJSON(w, map[string]any{
		"has_password":  hasPwd,
		"authenticated": authed,
	})
}

func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hasPwd, _ := s.db.HasPassword()
	if hasPwd {
		http.Error(w, "Password already configured", http.StatusBadRequest)
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(strings.TrimSpace(req.Password)) == 0 {
		http.Error(w, "Invalid password", http.StatusBadRequest)
		return
	}

	hash, err := db.HashPassword(req.Password)
	if err != nil {
		http.Error(w, "Error hashing password", http.StatusInternalServerError)
		return
	}

	if err := s.db.SetPasswordHash(hash); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Generate session token valid for 1 year
	tokBytes := make([]byte, 24)
	_, _ = rand.Read(tokBytes)
	sessionTok := hex.EncodeToString(tokBytes)
	_ = s.db.CreateSession(sessionTok, time.Now().AddDate(1, 0, 0))

	http.SetCookie(w, &http.Cookie{
		Name:     "focus_session",
		Value:    sessionTok,
		Path:     "/",
		MaxAge:   31536000,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	writeJSON(w, map[string]any{"ok": true, "token": sessionTok})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	hash, err := s.db.GetPasswordHash()
	if err != nil || hash == "" {
		http.Error(w, "No password configured", http.StatusBadRequest)
		return
	}

	if !db.CheckPassword(req.Password, hash) {
		http.Error(w, "Incorrect password", http.StatusUnauthorized)
		return
	}

	tokBytes := make([]byte, 24)
	_, _ = rand.Read(tokBytes)
	sessionTok := hex.EncodeToString(tokBytes)
	_ = s.db.CreateSession(sessionTok, time.Now().AddDate(1, 0, 0))

	http.SetCookie(w, &http.Cookie{
		Name:     "focus_session",
		Value:    sessionTok,
		Path:     "/",
		MaxAge:   31536000,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	writeJSON(w, map[string]any{"ok": true, "token": sessionTok})
}

func (s *Server) handleAuthChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(strings.TrimSpace(req.NewPassword)) == 0 {
		http.Error(w, "Invalid input", http.StatusBadRequest)
		return
	}

	hash, err := s.db.GetPasswordHash()
	if err != nil || hash == "" {
		http.Error(w, "No password configured", http.StatusBadRequest)
		return
	}

	if !db.CheckPassword(req.OldPassword, hash) {
		http.Error(w, "Current password is incorrect", http.StatusUnauthorized)
		return
	}

	newHash, err := db.HashPassword(req.NewPassword)
	if err != nil {
		http.Error(w, "Error hashing password", http.StatusInternalServerError)
		return
	}

	if err := s.db.SetPasswordHash(newHash); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("focus_session"); err == nil && cookie.Value != "" {
		_ = s.db.DeleteAuthSession(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "focus_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	writeJSON(w, map[string]any{"ok": true})
}

// ==================== AUDIO SESSION STREAM (iOS Lock Screen & Background Alarm) ====================

type VirtualMP3Reader struct {
	silentFrame    []byte
	silentTotalLen int64
	alarmData      []byte
	alarmTotalLen  int64
	trailTotalLen  int64
	totalLen       int64
	offset         int64
}

func NewVirtualMP3Reader(silentFrame []byte, silentCount int, alarmData []byte, trailCount int) *VirtualMP3Reader {
	frameLen := int64(len(silentFrame))
	silentTotalLen := int64(silentCount) * frameLen
	alarmTotalLen := int64(len(alarmData))
	trailTotalLen := int64(trailCount) * frameLen
	return &VirtualMP3Reader{
		silentFrame:    silentFrame,
		silentTotalLen: silentTotalLen,
		alarmData:      alarmData,
		alarmTotalLen:  alarmTotalLen,
		trailTotalLen:  trailTotalLen,
		totalLen:       silentTotalLen + alarmTotalLen + trailTotalLen,
		offset:         0,
	}
}

func (r *VirtualMP3Reader) Read(p []byte) (n int, err error) {
	if r.offset >= r.totalLen {
		return 0, io.EOF
	}

	available := r.totalLen - r.offset
	toRead := int64(len(p))
	if toRead > available {
		toRead = available
	}

	frameLen := int64(len(r.silentFrame))
	alarmEnd := r.silentTotalLen + r.alarmTotalLen

	for int64(n) < toRead {
		curOffset := r.offset + int64(n)
		needed := toRead - int64(n)

		if curOffset < r.silentTotalLen {
			// In leading silence
			frameOffset := curOffset % frameLen
			chunk := frameLen - frameOffset
			availInSec := r.silentTotalLen - curOffset
			if chunk > availInSec {
				chunk = availInSec
			}
			if chunk > needed {
				chunk = needed
			}
			copy(p[n:n+int(chunk)], r.silentFrame[frameOffset:frameOffset+chunk])
			n += int(chunk)
		} else if curOffset < alarmEnd {
			// In alarm
			alarmOffset := curOffset - r.silentTotalLen
			chunk := r.alarmTotalLen - alarmOffset
			if chunk > needed {
				chunk = needed
			}
			copy(p[n:n+int(chunk)], r.alarmData[alarmOffset:alarmOffset+chunk])
			n += int(chunk)
		} else {
			// In trailing silence
			trailOffset := curOffset - alarmEnd
			frameOffset := trailOffset % frameLen
			chunk := frameLen - frameOffset
			availInSec := r.totalLen - curOffset
			if chunk > availInSec {
				chunk = availInSec
			}
			if chunk > needed {
				chunk = needed
			}
			copy(p[n:n+int(chunk)], r.silentFrame[frameOffset:frameOffset+chunk])
			n += int(chunk)
		}
	}

	r.offset += int64(n)
	return n, nil
}

func (r *VirtualMP3Reader) Seek(offset int64, whence int) (int64, error) {
	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = r.offset + offset
	case io.SeekEnd:
		newOffset = r.totalLen + offset
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}

	if newOffset < 0 {
		return 0, fmt.Errorf("negative offset")
	}
	r.offset = newOffset
	return r.offset, nil
}

func (s *Server) handleAudioSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	remainingStr := r.URL.Query().Get("remaining")
	remaining, _ := strconv.Atoi(remainingStr)

	alarmData, err := s.assets.ReadFile("alarm.mp3")
	if err != nil {
		http.Error(w, "Alarm asset missing", http.StatusInternalServerError)
		return
	}

	silentFrame, err := s.assets.ReadFile("silent_frame.bin")
	if err != nil {
		http.Error(w, "Silent frame asset missing", http.StatusInternalServerError)
		return
	}

	// 1 MPEG1 Layer 3 frame = 1152 samples / 48000 Hz = 24 ms => 41.666667 frames/sec
	// alarm.mp3 is 86 frames (24768 bytes, ~2.064s) with ~0.27s (11 frames) pre-roll before audible chime strike.
	chimeOffsetFrames := 11
	silentCount := int(float64(remaining)*(48000.0/1152.0)) - chimeOffsetFrames
	if silentCount < 0 {
		silentCount = 0
	}
	trailCount := 42 // ~1.0s of clean trailing silence to let audio output drain without cut-off

	reader := NewVirtualMP3Reader(silentFrame, silentCount, alarmData, trailCount)
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	http.ServeContent(w, r, "session.mp3", time.Now(), reader)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sessions, err := s.db.GetAllSessions()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	format := strings.ToLower(r.URL.Query().Get("format"))
	timestamp := time.Now().Format("20060102-150405")

	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="focus-export-%s.csv"`, timestamp))

		csvWriter := csv.NewWriter(w)
		_ = csvWriter.Write([]string{"Project", "Mode", "Start Time", "End Time", "Duration (seconds)", "Duration", "Completed", "Note"})

		for _, sess := range sessions {
			hours := sess.DurationSeconds / 3600
			mins := (sess.DurationSeconds % 3600) / 60
			secs := sess.DurationSeconds % 60
			durHuman := fmt.Sprintf("%02d:%02d:%02d", hours, mins, secs)

			compStr := "true"
			if !sess.Completed {
				compStr = "false"
			}

			_ = csvWriter.Write([]string{
				sess.ProjectName,
				sess.Mode,
				sess.StartTime.UTC().Format(time.RFC3339),
				sess.EndTime.UTC().Format(time.RFC3339),
				strconv.Itoa(sess.DurationSeconds),
				durHuman,
				compStr,
				sess.Note,
			})
		}
		csvWriter.Flush()
		return
	}

	if format == "timewarrior" || format == "timew" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="focus-timewarrior-%s.json"`, timestamp))

		type timewExportItem struct {
			ID    int64    `json:"id,omitempty"`
			Start string   `json:"start"`
			End   string   `json:"end"`
			Tags  []string `json:"tags"`
		}

		var timewEntries []timewExportItem
		for _, sess := range sessions {
			tag := sess.ProjectName
			if tag == "" {
				tag = "Focus"
			}
			timewEntries = append(timewEntries, timewExportItem{
				ID:    sess.ID,
				Start: sess.StartTime.UTC().Format("20060102T150405Z"),
				End:   sess.EndTime.UTC().Format("20060102T150405Z"),
				Tags:  []string{tag},
			})
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(timewEntries)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="focus-export-%s.json"`, timestamp))

	projects, _ := s.db.ListProjects()

	exportData := struct {
		Version    int          `json:"version"`
		ExportedAt time.Time    `json:"exported_at"`
		Projects   []db.Project `json:"projects,omitempty"`
		Sessions   []db.Session `json:"sessions"`
	}{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		Projects:   projects,
		Sessions:   sessions,
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(exportData)
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var data []byte
	var err error

	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		file, _, formErr := r.FormFile("file")
		if formErr != nil {
			http.Error(w, "Missing file in multipart form", http.StatusBadRequest)
			return
		}
		defer file.Close()
		data, err = io.ReadAll(file)
	} else {
		data, err = io.ReadAll(r.Body)
	}

	if err != nil || len(data) == 0 {
		http.Error(w, "Empty or unreadable payload", http.StatusBadRequest)
		return
	}

	sessions, err := db.ParseImportSessions(data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	imported, skipped, err := s.db.ImportSessions(sessions)
	if err != nil {
		http.Error(w, "Import failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.engine.NotifyActivityRecorded()

	writeJSON(w, map[string]interface{}{
		"ok":       true,
		"imported": imported,
		"skipped":  skipped,
		"total":    len(sessions),
	})
}

