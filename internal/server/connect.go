package server

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Connecting an app (the Assistant…) to Focus, in the manner of OAuth:
//
//  1. the app sends the user to /?connect=<name>&redirect_uri=<url>&state=<s>;
//  2. Focus asks the user, logged in, whether that app may read the hours;
//  3. on Allow, the page gets a one-time code (POST /api/connect) and goes
//     back to redirect_uri?code=…&state=…;
//  4. the app's server trades the code for a lasting token
//     (POST /api/connect/token), sent to it only, never to the browser.
//
// An app token reads the month view and nothing else; the user disconnects
// the app from Password & security, or the app leaves (DELETE /api/connect).

const codeTTL = 5 * time.Minute

type connectCode struct {
	app, redirect string
	expires       time.Time
}

type connectCodes struct {
	mu    sync.Mutex
	codes map[string]connectCode
}

// appReadable are the routes an app token may call.
var appReadable = map[string]bool{"/api/month": true}

// validRedirect accepts an absolute http(s) address.
func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.Fragment == ""
}

// handleConnect gives the logged-in user's page a one-time code for an app.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		App         string `json:"app"`
		RedirectURI string `json:"redirect_uri"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	req.App = strings.TrimSpace(req.App)
	if req.App == "" || len(req.App) > 60 {
		http.Error(w, "The app needs a name of at most 60 characters", http.StatusBadRequest)
		return
	}
	if !validRedirect(req.RedirectURI) {
		http.Error(w, "The app gave no valid address to come back to", http.StatusBadRequest)
		return
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		serverError(w, err)
		return
	}
	code := hex.EncodeToString(b)
	now := time.Now()
	s.connect.mu.Lock()
	if s.connect.codes == nil {
		s.connect.codes = map[string]connectCode{}
	}
	for c, p := range s.connect.codes {
		if now.After(p.expires) {
			delete(s.connect.codes, c)
		}
	}
	s.connect.codes[code] = connectCode{app: req.App, redirect: req.RedirectURI, expires: now.Add(codeTTL)}
	s.connect.mu.Unlock()
	writeJSON(w, map[string]string{"code": code})
}

// handleConnectToken trades a one-time code for an app token.
func (s *Server) handleConnectToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code        string `json:"code"`
		RedirectURI string `json:"redirect_uri"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	s.connect.mu.Lock()
	p, ok := s.connect.codes[req.Code]
	delete(s.connect.codes, req.Code) // one use, right or wrong
	s.connect.mu.Unlock()
	if !ok || time.Now().After(p.expires) || p.redirect != req.RedirectURI {
		http.Error(w, "Unknown or expired code: connect again", http.StatusBadRequest)
		return
	}
	token, err := s.db.CreateAppToken(p.app)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]string{"token": token})
}

// handleDisconnectSelf lets an app give its token back.
func (s *Server) handleDisconnectSelf(w http.ResponseWriter, r *http.Request) {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if err := s.db.DeleteAppToken(tok); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.db.ListApps()
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, apps)
}

func (s *Server) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Unknown app", http.StatusBadRequest)
		return
	}
	if err := s.db.DeleteApp(id); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// appAuthed lets a connected app's token through on the routes it may read,
// and the user's own credentials everywhere.
func (s *Server) appAuthed(next http.HandlerFunc) http.HandlerFunc {
	user := s.authed(next)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && appReadable[r.URL.Path] {
			if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && s.db.UseAppToken(tok) {
				next(w, r)
				return
			}
		}
		user(w, r)
	}
}
