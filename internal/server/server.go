// Package server exposes the timer and the database over HTTP and serves the web UI.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"focus/internal/db"
	"focus/internal/timer"
)

const (
	sessionCookie = "focus_session"
	tokenTTL      = 365 * 24 * time.Hour
	maxJSONBody   = 1 << 20  // 1 MiB
	maxImportBody = 32 << 20 // 32 MiB
	sseHeartbeat  = 25 * time.Second
)

type Server struct {
	db         *db.DB
	engine     *timer.Engine
	assets     fs.FS
	mux        *http.ServeMux
	localToken string     // shared secret for the CLI on this host; "" disables it
	loginMu    sync.Mutex // serializes password checks to slow down brute force
}

func NewServer(database *db.DB, engine *timer.Engine, assets fs.FS, localToken string) *Server {
	s := &Server{
		db:         database,
		engine:     engine,
		assets:     assets,
		mux:        http.NewServeMux(),
		localToken: localToken,
	}
	s.registerRoutes()
	return s
}

func (s *Server) Handler() http.Handler {
	return securityHeaders(s.mux)
}

func (s *Server) registerRoutes() {
	public := map[string]http.HandlerFunc{
		"GET /api/auth/status":       s.handleAuthStatus,
		"POST /api/auth/setup":       s.handleAuthSetup,
		"POST /api/auth/login":       s.handleAuthLogin,
		"POST /api/auth/logout":      s.handleAuthLogout,
		"GET /api/theme":             s.handleTheme,
		"GET /api/audio/session.mp3": s.handleAudioSession,
	}
	protected := map[string]http.HandlerFunc{
		"POST /api/auth/change-password":  s.handleAuthChangePassword,
		"GET /api/state":                  s.handleState,
		"GET /api/events":                 s.handleEvents,
		"POST /api/action":                s.handleAction,
		"GET /api/projects":               s.handleListProjects,
		"POST /api/projects":              s.handleCreateProject,
		"GET /api/projects/month":         s.handleMonthProjects,
		"POST /api/projects/rename-month": s.handleRenameProjectMonth,
		"POST /api/projects/rename":       s.handleRenameProject,
		"GET /api/projects/last":          s.handleLastProject,
		"GET /api/sessions/today":         s.handleTodaySessions,
		"POST /api/sessions":              s.handleCreateSession,
		"PUT /api/sessions/{id}":          s.handleUpdateSession,
		"POST /api/sessions/{id}":         s.handleUpdateSession,
		"DELETE /api/sessions/{id}":       s.handleDeleteSession,
		"GET /api/month":                  s.handleMonth,
		"GET /api/stats":                  s.handleStats,
		"GET /api/stats/periods":          s.handleStatsPeriods,
		"GET /api/settings":               s.handleGetSettings,
		"POST /api/settings":              s.handleSetSettings,
		"GET /api/export":                 s.handleExport,
		"POST /api/import":                s.handleImport,
	}
	for pattern, h := range public {
		s.mux.HandleFunc(pattern, h)
	}
	for pattern, h := range protected {
		s.mux.HandleFunc(pattern, s.authed(h))
	}

	// The UI must pick up a new binary immediately (no stale cached assets).
	files := http.FileServerFS(s.assets)
	s.mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// ==================== Helpers ====================

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("server: write response: %v", err)
	}
}

// decodeJSON reads a size-limited JSON body; it answers 400 itself on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody)).Decode(dst); err != nil {
		http.Error(w, "Invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// serverError answers 400 for invalid input reported by the database layer,
// 500 otherwise.
func serverError(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrInvalidSession) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("server: %v", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// yearMonthQuery reads ?year=&month=, defaulting to the current month.
func yearMonthQuery(r *http.Request) (int, int) {
	now := time.Now()
	year, month := now.Year(), int(now.Month())
	if y, err := strconv.Atoi(r.URL.Query().Get("year")); err == nil && y > 1970 && y < 10000 {
		year = y
	}
	if m, err := strconv.Atoi(r.URL.Query().Get("month")); err == nil && m >= 1 && m <= 12 {
		month = m
	}
	return year, month
}

// ==================== Timer ====================

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.engine.Snapshot())
}

// handleEvents streams snapshots as Server-Sent Events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering (nginx)

	ch := s.engine.Subscribe()
	defer s.engine.Unsubscribe(ch)
	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			// Keeps idle connections alive through proxies.
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
		case snap, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(snap)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
		}
		flusher.Flush()
	}
}

type ActionPayload struct {
	Action        string `json:"action"` // start, pause, resume, toggle, stop, next, cancel, set_durations, set_project, set_mode
	Project       string `json:"project,omitempty"`
	Mode          string `json:"mode,omitempty"`
	WorkDuration  int    `json:"work_duration,omitempty"`
	BreakDuration int    `json:"break_duration,omitempty"`
	ClientID      string `json:"client_id,omitempty"`
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	var req ActionPayload
	if !decodeJSON(w, r, &req) {
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

// ==================== Projects ====================

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	list, err := s.db.ListProjects()
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, list)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	p, err := s.db.GetOrCreateProject(body.Name)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, p)
}

func (s *Server) handleMonthProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.db.GetMonthProjects(yearMonthQuery(r))
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, projects)
}

func (s *Server) handleLastProject(w http.ResponseWriter, r *http.Request) {
	name, err := s.db.GetLastProject()
	if err != nil {
		serverError(w, err)
		return
	}
	p, err := s.db.GetOrCreateProject(name)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, p)
}

func (s *Server) handleRenameProjectMonth(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Year    int    `json:"year"`
		Month   int    `json:"month"`
		OldName string `json:"old_name"`
		NewName string `json:"new_name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.OldName) == "" || strings.TrimSpace(req.NewName) == "" {
		http.Error(w, "Project name cannot be empty", http.StatusBadRequest)
		return
	}
	if req.Year <= 1970 || req.Month < 1 || req.Month > 12 {
		http.Error(w, "Invalid year or month", http.StatusBadRequest)
		return
	}
	name, err := s.db.RenameProjectForMonth(req.Year, req.Month, req.OldName, req.NewName)
	if err != nil {
		serverError(w, err)
		return
	}
	// The running session belongs to the current month.
	if now := time.Now(); req.Year == now.Year() && req.Month == int(now.Month()) {
		s.engine.ProjectRenamed(req.OldName, name)
	}
	s.engine.NotifyActivityRecorded()
	writeJSON(w, map[string]string{"status": "ok", "name": name})
}

// handleRenameProject renames (or merges) a project across all its sessions.
func (s *Server) handleRenameProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldName string `json:"old_name"`
		NewName string `json:"new_name"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.OldName) == "" || strings.TrimSpace(req.NewName) == "" {
		http.Error(w, "Project name cannot be empty", http.StatusBadRequest)
		return
	}
	name, err := s.db.RenameProject(req.OldName, req.NewName)
	if err != nil {
		serverError(w, err)
		return
	}
	s.engine.ProjectRenamed(req.OldName, name)
	s.engine.NotifyActivityRecorded()
	writeJSON(w, map[string]string{"status": "ok", "name": name})
}

// ==================== Sessions ====================

func (s *Server) handleTodaySessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.db.GetTodaySessions()
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, sessions)
}

// sessionInput is the normalized body of a session create/update request.
// Field names and time formats are accepted liberally for scripts.
type sessionInput struct {
	Project  string
	Mode     string
	UUID     string
	Note     string
	Start    time.Time
	End      time.Time
	Duration int
}

func payloadField(payload map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := payload[k]; ok && v != nil {
			return v
		}
	}
	return nil
}

func payloadString(payload map[string]any, keys ...string) string {
	if v := payloadField(payload, keys...); v != nil {
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return ""
}

func decodeSessionInput(w http.ResponseWriter, r *http.Request) (sessionInput, bool) {
	var payload map[string]any
	if !decodeJSON(w, r, &payload) {
		return sessionInput{}, false
	}
	in := sessionInput{
		Project: payloadString(payload, "project_name", "project", "ProjectName", "projectName"),
		Mode:    payloadString(payload, "mode", "Mode"),
		UUID:    payloadString(payload, "uuid", "UUID"),
		Note:    payloadString(payload, "note", "Note"),
	}
	startVal := payloadField(payload, "start_time", "startTime", "StartTime", "start")
	endVal := payloadField(payload, "end_time", "endTime", "EndTime", "end")
	if startVal == nil || endVal == nil {
		http.Error(w, "Both start_time and end_time are required", http.StatusBadRequest)
		return in, false
	}
	var err1, err2 error
	in.Start, err1 = parseFlexibleTime(startVal)
	in.End, err2 = parseFlexibleTime(endVal)
	if err := errors.Join(err1, err2); err != nil {
		http.Error(w, "Invalid date: "+err.Error(), http.StatusBadRequest)
		return in, false
	}
	switch d := payloadField(payload, "duration_seconds", "duration", "durationSeconds").(type) {
	case float64:
		in.Duration = int(d)
	case string:
		in.Duration, _ = strconv.Atoi(d)
	}

	// Inverted bounds are swapped; an empty interval becomes one minute.
	if in.End.Before(in.Start) {
		in.Start, in.End = in.End, in.Start
		in.Duration = 0
	}
	if !in.End.After(in.Start) {
		in.End = in.Start.Add(time.Minute)
		in.Duration = 0
	}
	return in, true
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeSessionInput(w, r)
	if !ok {
		return
	}
	sess, err := s.db.RecordSession(db.Session{
		UUID:            in.UUID,
		ProjectName:     in.Project,
		Mode:            in.Mode,
		StartTime:       in.Start,
		EndTime:         in.End,
		DurationSeconds: in.Duration,
		Completed:       true,
		Note:            in.Note,
	})
	if err != nil {
		serverError(w, err)
		return
	}
	s.engine.NotifyActivityRecorded()
	writeJSON(w, sess)
}

func (s *Server) handleUpdateSession(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "Invalid session id", http.StatusBadRequest)
		return
	}
	in, ok := decodeSessionInput(w, r)
	if !ok {
		return
	}
	sess, err := s.db.UpdateSession(id, in.Project, in.Start, in.End)
	if errors.Is(err, db.ErrNotFound) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	s.engine.NotifyActivityRecorded()
	writeJSON(w, sess)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	err := s.db.DeleteSession(r.PathValue("id"))
	if errors.Is(err, db.ErrNotFound) {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	s.engine.NotifyActivityRecorded()
	writeJSON(w, map[string]string{"status": "deleted"})
}

func parseNumericTime(val float64) time.Time {
	// Beyond 1e11 the value can only be milliseconds (1e11 s is year 5138).
	if val >= 1e11 || val <= -1e11 {
		return time.UnixMilli(int64(val))
	}
	sec := int64(val)
	return time.Unix(sec, int64((val-float64(sec))*1e9))
}

// parseFlexibleTime accepts Unix seconds or milliseconds (number or numeric
// string), RFC3339, and zoneless ISO/SQL layouts interpreted in local time.
func parseFlexibleTime(val any) (time.Time, error) {
	switch v := val.(type) {
	case float64:
		return parseNumericTime(v), nil
	case int64:
		return parseNumericTime(float64(v)), nil
	case int:
		return parseNumericTime(float64(v)), nil
	case string:
		str := strings.TrimSpace(v)
		if str == "" {
			return time.Time{}, fmt.Errorf("empty time string")
		}
		if num, err := strconv.ParseFloat(str, 64); err == nil {
			return parseNumericTime(num), nil
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05-0700", "2006-01-02T15:04:05.999-0700"} {
			if t, err := time.Parse(layout, str); err == nil {
				return t, nil
			}
		}
		localLayouts := []string{
			"2006-01-02T15:04:05.999999999",
			"2006-01-02T15:04",
			"2006-01-02 15:04:05.999999999",
			"2006-01-02 15:04",
			"2006-01-02",
		}
		for _, layout := range localLayouts {
			if t, err := time.ParseInLocation(layout, str, time.Local); err == nil {
				return t, nil
			}
		}
		return time.Time{}, fmt.Errorf("unable to parse time %q", str)
	default:
		return time.Time{}, fmt.Errorf("unsupported time value type %T", val)
	}
}

// ==================== Month & statistics ====================

func (s *Server) handleMonth(w http.ResponseWriter, r *http.Request) {
	data, err := s.db.GetMonthData(yearMonthQuery(r))
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, data)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	year, _ := strconv.Atoi(q.Get("year"))
	month, _ := strconv.Atoi(q.Get("month"))
	stats, err := s.db.GetStats(q.Get("period"), year, month)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, stats)
}

func (s *Server) handleStatsPeriods(w http.ResponseWriter, r *http.Request) {
	years, months, err := s.db.GetAvailablePeriods()
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"years": years, "months": months})
}

// ==================== Settings & theme ====================

func (s *Server) handleTheme(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"theme": s.engine.Snapshot().Theme})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.db.GetSettings()
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, settings)
}

func (s *Server) handleSetSettings(w http.ResponseWriter, r *http.Request) {
	var updates map[string]string
	if !decodeJSON(w, r, &updates) {
		return
	}
	for k, v := range updates {
		var err error
		switch k {
		// Durations go through the engine so the current timer follows.
		case db.SettingWorkDuration, db.SettingBreakDuration:
			var n int
			if n, err = strconv.Atoi(v); err == nil {
				if k == db.SettingWorkDuration {
					s.engine.SetDurations(n, 0)
				} else {
					s.engine.SetDurations(0, n)
				}
			}
		default:
			err = s.db.SetSetting(k, v)
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid setting %q: %v", k, err), http.StatusBadRequest)
			return
		}
	}
	s.handleGetSettings(w, r)
}

// ==================== Authentication ====================

func (s *Server) tokenValid(token string) bool {
	if token == "" {
		return false
	}
	if s.localToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.localToken)) == 1 {
		return true
	}
	return s.db.ValidateAuthToken(token)
}

// requestTokens lists the credentials a request may carry.
func requestTokens(r *http.Request) []string {
	var tokens []string
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		tokens = append(tokens, tok)
	}
	tokens = append(tokens, r.Header.Get("X-Focus-Token"), r.URL.Query().Get("token"))
	if c, err := r.Cookie(sessionCookie); err == nil {
		tokens = append(tokens, c.Value)
	}
	return tokens
}

// isAuthenticated is true when no password is configured yet, or when the
// request carries a valid token.
func (s *Server) isAuthenticated(r *http.Request) bool {
	hasPwd, err := s.db.HasPassword()
	if err != nil {
		log.Printf("server: read password: %v", err)
		return false
	}
	if !hasPwd {
		return true
	}
	for _, tok := range requestTokens(r) {
		if s.tokenValid(tok) {
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

// checkPassword verifies a password. Leading/trailing spaces are ignored, but
// a hash created from an untrimmed password still matches.
func checkPassword(password, hash string) bool {
	return db.CheckPassword(strings.TrimSpace(password), hash) || db.CheckPassword(password, hash)
}

// issueToken creates a login token and sets it as the session cookie.
func (s *Server) issueToken(w http.ResponseWriter, r *http.Request) (string, bool) {
	token, err := s.db.CreateAuthToken(tokenTTL)
	if err != nil {
		serverError(w, err)
		return "", false
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(tokenTTL / time.Second),
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
	return token, true
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	hasPwd, err := s.db.HasPassword()
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"has_password": hasPwd, "authenticated": s.isAuthenticated(r)})
}

func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	password := strings.TrimSpace(req.Password)
	if password == "" {
		http.Error(w, "Invalid password", http.StatusBadRequest)
		return
	}
	hash, err := db.HashPassword(password)
	if err != nil {
		serverError(w, err)
		return
	}
	created, err := s.db.SetPasswordIfUnset(hash)
	if err != nil {
		serverError(w, err)
		return
	}
	if !created {
		http.Error(w, "Password already configured", http.StatusConflict)
		return
	}
	if token, ok := s.issueToken(w, r); ok {
		writeJSON(w, map[string]any{"ok": true, "token": token})
	}
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	s.loginMu.Lock()
	hash, err := s.db.GetPasswordHash()
	ok := err == nil && hash != "" && checkPassword(req.Password, hash)
	if err == nil && hash != "" && !ok {
		time.Sleep(time.Second) // at most one failed attempt per second
	}
	s.loginMu.Unlock()

	switch {
	case err != nil:
		serverError(w, err)
		return
	case hash == "":
		http.Error(w, "No password configured", http.StatusBadRequest)
		return
	case !ok:
		http.Error(w, "Incorrect password", http.StatusUnauthorized)
		return
	}

	if db.IsLegacyHash(hash) {
		if upgraded, err := db.HashPassword(strings.TrimSpace(req.Password)); err == nil {
			if err := s.db.SetPasswordHash(upgraded); err != nil {
				log.Printf("server: upgrade password hash: %v", err)
			}
		}
	}
	if token, ok := s.issueToken(w, r); ok {
		writeJSON(w, map[string]any{"ok": true, "token": token})
	}
}

// handleAuthChangePassword replaces the password and revokes every other login.
func (s *Server) handleAuthChangePassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	newPassword := strings.TrimSpace(req.NewPassword)
	if newPassword == "" {
		http.Error(w, "New password cannot be empty", http.StatusBadRequest)
		return
	}

	s.loginMu.Lock()
	hash, err := s.db.GetPasswordHash()
	ok := err == nil && hash != "" && checkPassword(req.OldPassword, hash)
	if err == nil && !ok {
		time.Sleep(time.Second)
	}
	s.loginMu.Unlock()

	if err != nil {
		serverError(w, err)
		return
	}
	if !ok {
		http.Error(w, "Current password is incorrect", http.StatusUnauthorized)
		return
	}
	newHash, err := db.HashPassword(newPassword)
	if err != nil {
		serverError(w, err)
		return
	}
	if err := s.db.SetPasswordHash(newHash); err != nil {
		serverError(w, err)
		return
	}
	if err := s.db.DeleteAllAuthTokens(); err != nil {
		serverError(w, err)
		return
	}
	if token, ok := s.issueToken(w, r); ok {
		writeJSON(w, map[string]any{"ok": true, "token": token})
	}
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := s.db.DeleteAuthToken(c.Value); err != nil {
			log.Printf("server: logout: %v", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, map[string]any{"ok": true})
}

// ==================== Import / export ====================

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	format := db.NormalizeExportFormat(r.URL.Query().Get("format"))
	contentType := "application/json"
	if format == db.FormatCSV {
		contentType = "text/csv; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, db.ExportFileName(format, time.Now())))
	if err := s.db.WriteExport(w, format); err != nil {
		serverError(w, err)
	}
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBody)
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
		serverError(w, fmt.Errorf("import failed: %w", err))
		return
	}
	s.engine.NotifyActivityRecorded()
	writeJSON(w, map[string]any{"ok": true, "imported": imported, "skipped": skipped, "total": len(sessions)})
}

// ==================== Background audio (iOS lock screen alarm) ====================

// VirtualMP3Reader serves silence, then the alarm, then a short silence, as a
// seekable MP3 stream without materializing it in memory.
type VirtualMP3Reader struct {
	silentFrame    []byte
	silentTotalLen int64
	alarmData      []byte
	totalLen       int64
	offset         int64
}

func NewVirtualMP3Reader(silentFrame []byte, silentCount int, alarmData []byte, trailCount int) *VirtualMP3Reader {
	frameLen := int64(len(silentFrame))
	silentTotalLen := int64(silentCount) * frameLen
	return &VirtualMP3Reader{
		silentFrame:    silentFrame,
		silentTotalLen: silentTotalLen,
		alarmData:      alarmData,
		totalLen:       silentTotalLen + int64(len(alarmData)) + int64(trailCount)*frameLen,
	}
}

func (r *VirtualMP3Reader) Read(p []byte) (int, error) {
	if r.offset >= r.totalLen {
		return 0, io.EOF
	}
	toRead := min(int64(len(p)), r.totalLen-r.offset)
	frameLen := int64(len(r.silentFrame))
	alarmEnd := r.silentTotalLen + int64(len(r.alarmData))

	var n int64
	for n < toRead {
		cur := r.offset + n
		var chunk []byte
		switch {
		case cur < r.silentTotalLen: // leading silence
			chunk = r.silentFrame[cur%frameLen:]
			chunk = chunk[:min(int64(len(chunk)), r.silentTotalLen-cur)]
		case cur < alarmEnd:
			chunk = r.alarmData[cur-r.silentTotalLen:]
		default: // trailing silence
			chunk = r.silentFrame[(cur-alarmEnd)%frameLen:]
		}
		n += int64(copy(p[n:toRead], chunk))
	}
	r.offset += n
	return int(n), nil
}

func (r *VirtualMP3Reader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += r.offset
	case io.SeekEnd:
		offset += r.totalLen
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}
	if offset < 0 {
		return 0, fmt.Errorf("negative offset")
	}
	r.offset = offset
	return offset, nil
}

func (s *Server) handleAudioSession(w http.ResponseWriter, r *http.Request) {
	remaining, _ := strconv.Atoi(r.URL.Query().Get("remaining"))
	remaining = max(0, min(remaining, timer.MaxDurationMin*60))

	alarmData, err1 := fs.ReadFile(s.assets, "alarm.mp3")
	silentFrame, err2 := fs.ReadFile(s.assets, "silent_frame.bin")
	if err := errors.Join(err1, err2); err != nil || len(silentFrame) == 0 {
		serverError(w, fmt.Errorf("audio assets missing: %v", err))
		return
	}

	// One MPEG-1 Layer III frame = 1152 samples at 48 kHz = 24 ms. alarm.mp3
	// has ~11 frames of pre-roll before the audible chime.
	const framesPerSecond = 48000.0 / 1152.0
	const chimeOffsetFrames = 11
	const trailFrames = 42 // ~1 s so the output drains without cutting the chime
	silentCount := max(0, int(float64(remaining)*framesPerSecond)-chimeOffsetFrames)

	reader := NewVirtualMP3Reader(silentFrame, silentCount, alarmData, trailFrames)
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "session.mp3", time.Time{}, reader)
}
