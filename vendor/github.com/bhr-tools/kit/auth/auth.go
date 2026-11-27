// Package auth is the master password of an app: first-run setup, login,
// sessions in a cookie, change of password, logout, the signing key of file
// links, and app tokens for other programs (the QuickShell widget of Focus,
// a calendar subscription). The browser side is web/js/lock.js.
//
//	a, err := auth.New(db, "todo_session", cfg.NoAuth)
//	a.Routes(mux)                          // /api/auth/*
//	mux.Handle("GET /api/tasks", a.Require(h))
//
// Its tables (auth_settings, auth_sessions, kit_app_tokens) are created when
// missing, with the schema the apps already had, so existing databases keep
// their password and sessions.
package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bhr-tools/kit/httpx"
	"github.com/bhr-tools/kit/store"
)

// SessionLifetime is how long a login stays valid.
const SessionLifetime = 30 * 24 * time.Hour

// MinPasswordLength is the shortest accepted master password.
const MinPasswordLength = 4

// FailedLoginDelay slows down password guessing.
var FailedLoginDelay = time.Second

const (
	pbkdf2Iterations = 600_000
	legacyIterations = 100_000
	saltLength       = 16
	keyLength        = 32
)

const schema = `
CREATE TABLE IF NOT EXISTS auth_settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS auth_sessions (
	token TEXT PRIMARY KEY,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS kit_app_tokens (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	scope TEXT NOT NULL DEFAULT '',
	token_hash TEXT NOT NULL UNIQUE,
	created_at DATETIME NOT NULL,
	used_at DATETIME
);`

// Auth guards an app's API.
type Auth struct {
	db     *sql.DB
	cookie string
	noAuth bool
	// loginMu serializes password checks; a failed attempt holds it for a
	// moment, which caps brute-force attempts without any bookkeeping.
	loginMu sync.Mutex
}

// New creates the auth tables if needed. cookie is the session cookie's
// name (keep an app's existing one so nobody is signed out); with noAuth
// every request is accepted.
func New(db *sql.DB, cookie string, noAuth bool) (*Auth, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("auth tables: %w", err)
	}
	return &Auth{db: db, cookie: cookie, noAuth: noAuth}, nil
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

// Routes registers the /api/auth/* endpoints on mux.
func (a *Auth) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/status", a.status)
	mux.HandleFunc("POST /api/auth/setup", a.setup)
	mux.HandleFunc("POST /api/auth/login", a.login)
	mux.HandleFunc("POST /api/auth/logout", a.logout)
	mux.Handle("POST /api/auth/change-password", a.Require(http.HandlerFunc(a.changePassword)))
}

// Require answers 401 to requests that are not signed in.
func (a *Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Authenticated(r) {
			httpx.WriteJSON(w, http.StatusUnauthorized, map[string]any{"error": "Authentication required", "auth_required": true})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Authenticated reports whether r may use the API. Until a password is set,
// the instance is open (first-run setup).
func (a *Auth) Authenticated(r *http.Request) bool {
	if a.noAuth {
		return true
	}
	hasPassword, err := a.HasPassword()
	if err != nil {
		return false
	}
	if !hasPassword {
		return true
	}
	c, err := r.Cookie(a.cookie)
	return err == nil && a.ValidateSession(c.Value)
}

type passwordRequest struct {
	Password    string `json:"password"`
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

func (a *Auth) status(w http.ResponseWriter, r *http.Request) {
	if a.noAuth {
		httpx.WriteJSON(w, http.StatusOK, map[string]bool{"has_password": false, "authenticated": true, "no_auth": true})
		return
	}
	hasPassword, err := a.HasPassword()
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	authenticated := false
	if hasPassword {
		if c, err := r.Cookie(a.cookie); err == nil {
			authenticated = a.ValidateSession(c.Value)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"has_password": hasPassword, "authenticated": authenticated, "no_auth": false})
}

func (a *Auth) setup(w http.ResponseWriter, r *http.Request) {
	if a.noAuth {
		httpx.WriteError(w, http.StatusBadRequest, "Authentication is disabled on this instance")
		return
	}
	var req passwordRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	if err := a.SetInitialPassword(req.Password); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.startSession(w, r)
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	var req passwordRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	if !a.checkPassword(w, r, req.Password) {
		return
	}
	a.startSession(w, r)
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(a.cookie); err == nil {
		_ = a.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: a.cookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r)})
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (a *Auth) changePassword(w http.ResponseWriter, r *http.Request) {
	if a.noAuth {
		httpx.WriteError(w, http.StatusBadRequest, "Authentication is disabled on this instance")
		return
	}
	var req passwordRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	if hasPassword, err := a.HasPassword(); err != nil {
		httpx.Fail(w, r, err)
		return
	} else if hasPassword && !a.checkPassword(w, r, req.OldPassword) {
		return
	}
	if err := a.SetPassword(req.NewPassword); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// Every other session was signed out; keep this one signed in.
	a.startSession(w, r)
}

// checkPassword verifies the master password, answering 401 when it is wrong.
func (a *Auth) checkPassword(w http.ResponseWriter, r *http.Request, password string) bool {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	ok, err := a.VerifyPassword(password)
	if err != nil {
		httpx.Fail(w, r, err)
		return false
	}
	if !ok {
		time.Sleep(FailedLoginDelay)
		httpx.WriteError(w, http.StatusUnauthorized, "Incorrect password")
		return false
	}
	return true
}

func (a *Auth) startSession(w http.ResponseWriter, r *http.Request) {
	token, err := a.CreateSession()
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     a.cookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(SessionLifetime.Seconds()),
	})
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

// isHTTPS reports whether the browser reached us over HTTPS, directly or
// through a reverse proxy.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// ---------------------------------------------------------------------------
// Password
// ---------------------------------------------------------------------------

// Stored password formats:
//
//	v2$<iterations>$<salt hex>$<key hex>   standard PBKDF2-HMAC-SHA256
//	<salt hex>$<key hex>                   GetDone's first release (non-standard PBKDF2 variant)
//	s1$<salt hex>$<sha256 hex>             Focus's first release: SHA-256(salt + password)
//
// Outdated hashes are verified as before and replaced by a v2 hash with
// today's iterations on the next successful login.

func hashPassword(password string) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, keyLength)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("v2$%d$%s$%s", pbkdf2Iterations, hex.EncodeToString(salt), hex.EncodeToString(key)), nil
}

// checkPassword reports whether password matches stored, and whether stored
// uses an outdated format that should be rehashed.
func checkPassword(password, stored string) (ok, outdated bool) {
	parts := strings.Split(stored, "$")
	switch {
	case len(parts) == 4 && parts[0] == "v2":
		iterations, err := strconv.Atoi(parts[1])
		salt, errSalt := hex.DecodeString(parts[2])
		want, errKey := hex.DecodeString(parts[3])
		if err != nil || errSalt != nil || errKey != nil || iterations <= 0 {
			return false, false
		}
		got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
		if err != nil {
			return false, false
		}
		return subtle.ConstantTimeCompare(got, want) == 1, iterations < pbkdf2Iterations
	case len(parts) == 3 && parts[0] == "s1":
		salt, errSalt := hex.DecodeString(parts[1])
		want, errKey := hex.DecodeString(parts[2])
		if errSalt != nil || errKey != nil {
			return false, false
		}
		h := sha256.New()
		h.Write(salt)
		h.Write([]byte(password))
		return subtle.ConstantTimeCompare(h.Sum(nil), want) == 1, true
	case len(parts) == 2:
		salt, errSalt := hex.DecodeString(parts[0])
		want, errKey := hex.DecodeString(parts[1])
		if errSalt != nil || errKey != nil {
			return false, false
		}
		return subtle.ConstantTimeCompare(legacyKey(password, salt), want) == 1, true
	}
	return false, false
}

// legacyKey reproduces GetDone's first key derivation (PBKDF2 without the
// block index in the first round).
func legacyKey(password string, salt []byte) []byte {
	h := hmac.New(sha256.New, []byte(password))
	h.Write(salt)
	u := h.Sum(nil)
	t := make([]byte, len(u))
	copy(t, u)
	for i := 1; i < legacyIterations; i++ {
		h.Reset()
		h.Write(u)
		u = h.Sum(nil)
		for j := range t {
			t[j] ^= u[j]
		}
	}
	return t
}

func validatePassword(password string) error {
	if strings.TrimSpace(password) == "" || len([]rune(password)) < MinPasswordLength {
		return httpx.Invalid("The password must be at least %d characters long", MinPasswordLength)
	}
	return nil
}

// HasPassword reports whether a master password has been set.
func (a *Auth) HasPassword() (bool, error) {
	return store.Exists(a.db, `SELECT 1 FROM auth_settings WHERE key = 'password_hash'`)
}

// SetPassword sets the master password and signs out every session.
func (a *Auth) SetPassword(password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	hashed, err := hashPassword(password)
	if err != nil {
		return err
	}
	return store.Tx(a.db, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO auth_settings (key, value) VALUES ('password_hash', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, hashed); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM auth_sessions`)
		return err
	})
}

// SetInitialPassword sets the master password only if none exists yet, so two
// concurrent first-run setups cannot overwrite each other.
func (a *Auth) SetInitialPassword(password string) error {
	if err := validatePassword(password); err != nil {
		return err
	}
	hashed, err := hashPassword(password)
	if err != nil {
		return err
	}
	res, err := a.db.Exec(`INSERT OR IGNORE INTO auth_settings (key, value) VALUES ('password_hash', ?)`, hashed)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.Invalid("A password is already configured")
	}
	return nil
}

// VerifyPassword checks the master password, upgrading an outdated hash.
func (a *Auth) VerifyPassword(password string) (bool, error) {
	var stored string
	err := a.db.QueryRow(`SELECT value FROM auth_settings WHERE key = 'password_hash'`).Scan(&stored)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ok, outdated := checkPassword(password, stored)
	if ok && outdated {
		if hashed, err := hashPassword(password); err == nil {
			_, _ = a.db.Exec(`UPDATE auth_settings SET value = ? WHERE key = 'password_hash'`, hashed)
		}
	}
	return ok, nil
}

// PasswordStamp returns an opaque value that changes whenever the master
// password does ("" when none is set), so a caller checking the password on
// every request (CalDAV's Basic authentication) can cache a success until
// then.
func (a *Auth) PasswordStamp() (string, error) {
	var stored string
	err := a.db.QueryRow(`SELECT value FROM auth_settings WHERE key = 'password_hash'`).Scan(&stored)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return tokenHash(stored), err
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

// Tokens are random; only their SHA-256 is stored, so a copy of the
// database does not hand out valid sessions.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// CreateSession starts a session and returns its token.
func (a *Auth) CreateSession() (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	if _, err := a.db.Exec(`DELETE FROM auth_sessions WHERE created_at <= ?`, sessionCutoff()); err != nil {
		return "", err
	}
	if _, err := a.db.Exec(`INSERT INTO auth_sessions (token, created_at) VALUES (?, ?)`,
		tokenHash(token), time.Now().UTC().Format(time.DateTime)); err != nil {
		return "", err
	}
	return token, nil
}

// ValidateSession reports whether token belongs to an unexpired session.
func (a *Auth) ValidateSession(token string) bool {
	if token == "" {
		return false
	}
	ok, err := store.Exists(a.db, `SELECT 1 FROM auth_sessions WHERE token = ? AND created_at > ?`, tokenHash(token), sessionCutoff())
	return err == nil && ok
}

// DeleteSession ends a session.
func (a *Auth) DeleteSession(token string) error {
	_, err := a.db.Exec(`DELETE FROM auth_sessions WHERE token = ?`, tokenHash(token))
	return err
}

// Sessions are stored as UTC "YYYY-MM-DD HH:MM:SS" text, which compares in
// chronological order.
func sessionCutoff() string {
	return time.Now().UTC().Add(-SessionLifetime).Format(time.DateTime)
}

// LinkSecret returns the key that signs file links, creating it on first use.
// Links signed with it work without the session cookie, which a home screen
// app on iOS does not share with the viewer it opens files in.
func (a *Auth) LinkSecret() ([]byte, error) {
	return LinkSecret(a.db)
}

// LinkSecret is Auth.LinkSecret for a store that has no Auth at hand (it
// builds its files.Store before the API exists). The auth tables must exist.
func LinkSecret(db *sql.DB) ([]byte, error) {
	var stored string
	err := db.QueryRow(`SELECT value FROM auth_settings WHERE key = 'link_secret'`).Scan(&stored)
	if err == sql.ErrNoRows {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO auth_settings (key, value) VALUES ('link_secret', ?)`, hex.EncodeToString(b)); err != nil {
			return nil, err
		}
		err = db.QueryRow(`SELECT value FROM auth_settings WHERE key = 'link_secret'`).Scan(&stored)
	}
	if err != nil {
		return nil, err
	}
	return hex.DecodeString(stored)
}

// ---------------------------------------------------------------------------
// App tokens
// ---------------------------------------------------------------------------

// AppToken lets another program call part of the API, sent as
// "Authorization: Bearer <token>". Scope is the app's to define and check.
type AppToken struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Scope     string     `json:"scope"`
	CreatedAt time.Time  `json:"created_at"`
	UsedAt    *time.Time `json:"used_at"`
}

// CreateAppToken returns a new token, shown once; only its hash is kept.
func (a *Auth) CreateAppToken(name, scope string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", httpx.Invalid("A token needs a name")
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	_, err = a.db.Exec(`INSERT INTO kit_app_tokens (name, scope, token_hash, created_at) VALUES (?, ?, ?, ?)`,
		name, scope, tokenHash(token), time.Now().UTC().Format(time.DateTime))
	return token, err
}

// RequestAppToken returns the app token of r's Authorization header, or nil,
// and records its use.
func (a *Auth) RequestAppToken(r *http.Request) *AppToken {
	header := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || token == "" {
		return nil
	}
	var t AppToken
	var created string
	err := a.db.QueryRow(`SELECT id, name, scope, created_at FROM kit_app_tokens WHERE token_hash = ?`, tokenHash(token)).
		Scan(&t.ID, &t.Name, &t.Scope, &created)
	if err != nil {
		return nil
	}
	t.CreatedAt, _ = parseTime(created)
	now := time.Now().UTC()
	t.UsedAt = &now
	_, _ = a.db.Exec(`UPDATE kit_app_tokens SET used_at = ? WHERE id = ?`, now.Format(time.DateTime), t.ID)
	return &t
}

// parseTime reads a stored time: the SQLite driver hands DATETIME columns
// back as RFC 3339, whatever text was written.
func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.DateTime} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unreadable time %q", s)
}

// AppTokens lists the tokens, newest first.
func (a *Auth) AppTokens() ([]AppToken, error) {
	rows, err := a.db.Query(`SELECT id, name, scope, created_at, used_at FROM kit_app_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := []AppToken{}
	for rows.Next() {
		var t AppToken
		var created string
		var used sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &t.Scope, &created, &used); err != nil {
			return nil, err
		}
		t.CreatedAt, _ = parseTime(created)
		if used.Valid {
			u, _ := parseTime(used.String)
			t.UsedAt = &u
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

// DeleteAppToken revokes a token.
func (a *Auth) DeleteAppToken(id int64) error {
	res, err := a.db.Exec(`DELETE FROM kit_app_tokens WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return httpx.ErrNotFound
	}
	return nil
}
