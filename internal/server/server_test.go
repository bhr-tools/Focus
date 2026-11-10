package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"focus/internal/db"
	"focus/internal/timer"
	"focus/web"
)

func setupTestServer(t *testing.T) (http.Handler, *db.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test_server.db"))
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	engine := timer.NewEngine(database, "")
	t.Cleanup(func() {
		engine.Close()
		database.Close()
	})
	return NewServer(database, engine, web.Files, "local-secret").Handler(), database
}

// call performs a request and returns the recorder; body may be nil, a string or any JSON value.
func call(t *testing.T, h http.Handler, method, path string, body any, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	default:
		data, _ := json.Marshal(b)
		reader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, reader)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("invalid JSON response %q: %v", w.Body.String(), err)
	}
	return v
}

func TestAPISessionsCRUD(t *testing.T) {
	h, database := setupTestServer(t)
	now := time.Now().UTC().Truncate(time.Second)

	w := call(t, h, http.MethodPost, "/api/sessions", map[string]any{
		"project_name": "Frontend Engineering",
		"start_time":   now.Add(-45 * time.Minute).Format(time.RFC3339),
		"end_time":     now.Format(time.RFC3339),
		"mode":         "pomodoro",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	created := decode[db.Session](t, w)
	if created.ID == 0 || created.ProjectName != "frontend engineering" || created.DurationSeconds != 2700 {
		t.Fatalf("unexpected created session: %+v", created)
	}

	w = call(t, h, http.MethodPut, fmt.Sprintf("/api/sessions/%d", created.ID), map[string]any{
		"project_name": "Frontend Engineering Refactored",
		"start_time":   now.Add(-50 * time.Minute).Format(time.RFC3339),
		"end_time":     now.Format(time.RFC3339),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	if s := decode[db.Session](t, w); s.DurationSeconds != 3000 || s.ProjectName != "frontend engineering refactored" {
		t.Fatalf("unexpected updated session: %+v", s)
	}

	if w := call(t, h, http.MethodPut, "/api/sessions/999", map[string]any{"start_time": "2026-01-01T10:00", "end_time": "2026-01-01T11:00"}); w.Code != http.StatusNotFound {
		t.Errorf("update of a missing session: expected 404, got %d", w.Code)
	}

	if w := call(t, h, http.MethodDelete, fmt.Sprintf("/api/sessions/%d", created.ID), nil); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, h, http.MethodDelete, fmt.Sprintf("/api/sessions/%d", created.ID), nil); w.Code != http.StatusNotFound {
		t.Errorf("second delete: expected 404, got %d", w.Code)
	}
	if all, _ := database.GetAllSessions(); len(all) != 0 {
		t.Fatalf("expected 0 sessions after delete, got %d", len(all))
	}
}

func TestParseFlexibleTime(t *testing.T) {
	cases := []struct {
		in    any
		check func(time.Time) bool
	}{
		{"2026-10-15T14:30:00Z", func(t time.Time) bool { return t.Year() == 2026 && t.Minute() == 30 }},
		{"2026-10-15T14:30:00.999Z", func(t time.Time) bool { return t.Nanosecond() >= 900000000 }},
		{"2026-10-15T14:30:05", func(t time.Time) bool { return t.Second() == 5 && t.Location() == time.Local }},
		{"2026-10-15T14:30", func(t time.Time) bool { return t.Minute() == 30 && t.Location() == time.Local }},
		{"2026-10-15 14:30:05", func(t time.Time) bool { return t.Second() == 5 && t.Location() == time.Local }},
		{int64(1792065842), func(t time.Time) bool { return t.Unix() == 1792065842 }},
		{float64(1792065842), func(t time.Time) bool { return t.Unix() == 1792065842 }},
		{int64(1792065842000), func(t time.Time) bool { return t.Unix() == 1792065842 }},
		{"1792065842000", func(t time.Time) bool { return t.Unix() == 1792065842 }},
	}
	for _, c := range cases {
		got, err := parseFlexibleTime(c.in)
		if err != nil || !c.check(got) {
			t.Errorf("parseFlexibleTime(%v) = %v, %v", c.in, got, err)
		}
	}
	if _, err := parseFlexibleTime("not a date"); err == nil {
		t.Error("expected an error for garbage input")
	}
}

func TestAPISessionsResilient(t *testing.T) {
	h, database := setupTestServer(t)
	nowMilli := time.Now().UnixMilli()

	cases := []struct {
		name    string
		payload map[string]any
		want    int
	}{
		{"alt keys, datetime-local", map[string]any{"project": "Backend", "startTime": "2026-10-15T10:00", "endTime": "2026-10-15T11:00"}, 3600},
		{"SQL dates, negative duration", map[string]any{"project_name": "Systems", "start_time": "2026-10-15 12:00:00", "end_time": "2026-10-15 12:30:00", "duration_seconds": -1}, 1800},
		{"empty interval", map[string]any{"project": "Quick", "start_time": "2026-10-15T14:00:00Z", "end_time": "2026-10-15T14:00:00Z"}, 60},
		{"inverted interval", map[string]any{"project_name": "Inverted", "start_time": "2026-10-15T16:00:00Z", "end_time": "2026-10-15T15:00:00Z"}, 3600},
		{"unix millis", map[string]any{"project": "Timestamp", "startTime": nowMilli - 300000, "endTime": nowMilli}, 300},
	}
	for _, c := range cases {
		w := call(t, h, http.MethodPost, "/api/sessions", c.payload)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", c.name, w.Code, w.Body.String())
		}
		if s := decode[db.Session](t, w); s.DurationSeconds != c.want {
			t.Errorf("%s: expected %ds, got %d", c.name, c.want, s.DurationSeconds)
		}
	}
	if w := call(t, h, http.MethodPost, "/api/sessions", map[string]any{"project": "x", "start_time": "2026-10-15T10:00"}); w.Code != http.StatusBadRequest {
		t.Errorf("missing end_time: expected 400, got %d", w.Code)
	}
	if all, _ := database.GetAllSessions(); len(all) != len(cases) {
		t.Fatalf("expected %d sessions, got %d", len(cases), len(all))
	}
}

func TestAuthFlow(t *testing.T) {
	h, _ := setupTestServer(t)

	// No password yet: the API is open.
	if w := call(t, h, http.MethodGet, "/api/state", nil); w.Code != http.StatusOK {
		t.Fatalf("open API: expected 200, got %d", w.Code)
	}

	w := call(t, h, http.MethodPost, "/api/auth/setup", map[string]string{"password": " s3cret "})
	if w.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", w.Code, w.Body.String())
	}
	token := decode[map[string]any](t, w)["token"].(string)
	if w := call(t, h, http.MethodPost, "/api/auth/setup", map[string]string{"password": "other"}); w.Code != http.StatusConflict {
		t.Errorf("second setup: expected 409, got %d", w.Code)
	}

	if w := call(t, h, http.MethodGet, "/api/state", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("locked API: expected 401, got %d", w.Code)
	}
	for _, hdr := range [][]string{
		{"Authorization", "Bearer " + token},
		{"X-Focus-Token", token},
		{"Cookie", "focus_session=" + token},
		{"Authorization", "Bearer local-secret"},
	} {
		if w := call(t, h, http.MethodGet, "/api/state", nil, hdr...); w.Code != http.StatusOK {
			t.Errorf("auth via %s: expected 200, got %d", hdr[0], w.Code)
		}
	}

	if w := call(t, h, http.MethodPost, "/api/auth/login", map[string]string{"password": "s3cret"}); w.Code != http.StatusOK {
		t.Errorf("login (trimmed password): expected 200, got %d", w.Code)
	}
	if w := call(t, h, http.MethodPost, "/api/auth/login", map[string]string{"password": "nope"}); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong password: expected 401, got %d", w.Code)
	}

	w = call(t, h, http.MethodPost, "/api/auth/change-password",
		map[string]string{"old_password": "s3cret", "new_password": "n3w"}, "Authorization", "Bearer "+token)
	if w.Code != http.StatusOK {
		t.Fatalf("change password: %d %s", w.Code, w.Body.String())
	}
	newToken := decode[map[string]any](t, w)["token"].(string)
	if w := call(t, h, http.MethodGet, "/api/state", nil, "Authorization", "Bearer "+token); w.Code != http.StatusUnauthorized {
		t.Errorf("old tokens must be revoked after a password change, got %d", w.Code)
	}
	if w := call(t, h, http.MethodGet, "/api/state", nil, "Authorization", "Bearer "+newToken); w.Code != http.StatusOK {
		t.Errorf("new token rejected: %d", w.Code)
	}
}

func TestSettingsHidePassword(t *testing.T) {
	h, database := setupTestServer(t)
	_ = database.SetPasswordHash("hash")
	auth := []string{"Authorization", "Bearer local-secret"}

	w := call(t, h, http.MethodGet, "/api/settings", nil, auth...)
	if strings.Contains(w.Body.String(), "password") {
		t.Fatalf("settings expose the password hash: %s", w.Body.String())
	}
	if w := call(t, h, http.MethodPost, "/api/settings", map[string]string{"password_hash": "x"}, auth...); w.Code != http.StatusBadRequest {
		t.Fatalf("writing password_hash: expected 400, got %d", w.Code)
	}
	w = call(t, h, http.MethodPost, "/api/settings", map[string]string{"work_duration": "42"}, auth...)
	if w.Code != http.StatusOK || decode[map[string]string](t, w)["work_duration"] != "42" {
		t.Fatalf("work_duration update failed: %d %s", w.Code, w.Body.String())
	}
	if snap := decode[timer.StateSnapshot](t, call(t, h, http.MethodGet, "/api/state", nil, auth...)); snap.WorkDurationMin != 42 {
		t.Fatalf("timer did not follow the new duration: %d", snap.WorkDurationMin)
	}
}

func TestActionsAndMethods(t *testing.T) {
	h, _ := setupTestServer(t)
	w := call(t, h, http.MethodPost, "/api/action", map[string]string{"action": "start", "project": "CLI", "mode": "free"})
	if snap := decode[timer.StateSnapshot](t, w); snap.State != "free-running" || snap.Project != "cli" {
		t.Fatalf("unexpected snapshot %+v", snap)
	}
	if w := call(t, h, http.MethodPost, "/api/action", map[string]string{"action": "explode"}); w.Code != http.StatusBadRequest {
		t.Errorf("unknown action: expected 400, got %d", w.Code)
	}
	if w := call(t, h, http.MethodPost, "/api/action", "{not json"); w.Code != http.StatusBadRequest {
		t.Errorf("malformed JSON: expected 400, got %d", w.Code)
	}
	if w := call(t, h, http.MethodPost, "/api/state", nil); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/state: expected 405, got %d", w.Code)
	}
	if w := call(t, h, http.MethodGet, "/", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<!DOCTYPE html>") {
		t.Errorf("index not served: %d", w.Code)
	}
}

func TestImportExportRoundTrip(t *testing.T) {
	h, _ := setupTestServer(t)
	csvData := "Project,Start Time,End Time,Duration (seconds)\nMaths,2026-01-05T09:00:00Z,2026-01-05T10:00:00Z,3600\n"
	w := call(t, h, http.MethodPost, "/api/import", csvData, "Content-Type", "text/csv")
	if res := decode[map[string]any](t, w); res["imported"] != float64(1) {
		t.Fatalf("import: %s", w.Body.String())
	}
	for _, format := range []string{"json", "csv", "timew"} {
		w := call(t, h, http.MethodGet, "/api/export?format="+format, nil)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "maths") {
			t.Errorf("export %s: %d %s", format, w.Code, w.Body.String())
		}
	}
	if w := call(t, h, http.MethodGet, "/api/month?year=2026&month=1", nil); decode[db.MonthData](t, w).TotalSeconds != 3600 {
		t.Errorf("month data does not include the import")
	}
}

func TestRenameProjectFollowedByTimer(t *testing.T) {
	h, database := setupTestServer(t)
	now := time.Now()
	if _, err := database.RecordSession(db.Session{ProjectName: "Old", EndTime: now, DurationSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	call(t, h, http.MethodPost, "/api/action", map[string]string{"action": "set_project", "project": "Old"})

	w := call(t, h, http.MethodPost, "/api/projects/rename", map[string]string{"old_name": "Old", "new_name": "New"})
	if w.Code != http.StatusOK || decode[map[string]string](t, w)["name"] != "new" {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	if snap := decode[timer.StateSnapshot](t, call(t, h, http.MethodGet, "/api/state", nil)); snap.Project != "new" {
		t.Errorf("timer should follow the renamed project, got %q", snap.Project)
	}
	if w := call(t, h, http.MethodPost, "/api/projects/rename", map[string]string{"old_name": "New", "new_name": " "}); w.Code != http.StatusBadRequest {
		t.Errorf("empty name: expected 400, got %d", w.Code)
	}
}

func TestVirtualMP3Reader(t *testing.T) {
	r := NewVirtualMP3Reader([]byte("ab"), 2, []byte("XYZ"), 1)
	data, err := io.ReadAll(r)
	if err != nil || string(data) != "ababXYZab" {
		t.Fatalf("got %q, %v", data, err)
	}
	if _, err := r.Seek(3, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	if n, _ := r.Read(buf); string(buf[:n]) != "bXY" {
		t.Fatalf("after seek got %q", buf[:n])
	}
}

// An app is connected by the user, reads the month view only, and is
// disconnected by the user or by itself.
func TestConnectApp(t *testing.T) {
	h, database := setupTestServer(t)
	hash, _ := db.HashPassword("pw")
	_ = database.SetPasswordHash(hash)
	user := []string{"Authorization", "Bearer local-secret"}
	back := "https://assistant.example.com/api/focus/callback"

	if w := call(t, h, http.MethodPost, "/api/connect", map[string]string{"app": "Assistant", "redirect_uri": back}); w.Code != http.StatusUnauthorized {
		t.Fatalf("connect without login: %d", w.Code)
	}
	if w := call(t, h, http.MethodPost, "/api/connect", map[string]string{"app": "Assistant", "redirect_uri": "javascript:alert(1)"}, user...); w.Code != http.StatusBadRequest {
		t.Fatalf("bad redirect: %d", w.Code)
	}
	w := call(t, h, http.MethodPost, "/api/connect", map[string]string{"app": "Assistant", "redirect_uri": back}, user...)
	if w.Code != http.StatusOK {
		t.Fatalf("connect: %d %s", w.Code, w.Body.String())
	}
	code := decode[map[string]string](t, w)["code"]

	if w := call(t, h, http.MethodPost, "/api/connect/token", map[string]string{"code": code, "redirect_uri": "https://evil.example.com/"}); w.Code != http.StatusBadRequest {
		t.Fatalf("code for another address: %d", w.Code)
	}
	if w := call(t, h, http.MethodPost, "/api/connect/token", map[string]string{"code": code, "redirect_uri": back}); w.Code != http.StatusBadRequest {
		t.Fatalf("a code works once, even after a wrong try: %d", w.Code)
	}
	code = decode[map[string]string](t, call(t, h, http.MethodPost, "/api/connect", map[string]string{"app": "Assistant", "redirect_uri": back}, user...))["code"]
	w = call(t, h, http.MethodPost, "/api/connect/token", map[string]string{"code": code, "redirect_uri": back})
	if w.Code != http.StatusOK {
		t.Fatalf("token: %d %s", w.Code, w.Body.String())
	}
	app := []string{"Authorization", "Bearer " + decode[map[string]string](t, w)["token"]}

	if w := call(t, h, http.MethodGet, "/api/month?year=2026&month=11", nil, app...); w.Code != http.StatusOK {
		t.Fatalf("app reads the month: %d", w.Code)
	}
	if w := call(t, h, http.MethodGet, "/api/state", nil, app...); w.Code != http.StatusUnauthorized {
		t.Fatalf("app reads the state: %d", w.Code)
	}
	if w := call(t, h, http.MethodPost, "/api/sessions", map[string]any{}, app...); w.Code != http.StatusUnauthorized {
		t.Fatalf("app writes: %d", w.Code)
	}

	apps := decode[[]db.App](t, call(t, h, http.MethodGet, "/api/apps", nil, user...))
	if len(apps) != 1 || apps[0].Name != "Assistant" || apps[0].UsedAt == "" {
		t.Fatalf("apps: %+v", apps)
	}
	if w := call(t, h, http.MethodDelete, fmt.Sprintf("/api/apps/%d", apps[0].ID), nil, user...); w.Code != http.StatusOK {
		t.Fatalf("disconnect: %d", w.Code)
	}
	if w := call(t, h, http.MethodGet, "/api/month?year=2026&month=11", nil, app...); w.Code != http.StatusUnauthorized {
		t.Fatalf("disconnected app still reads: %d", w.Code)
	}

	// The app leaving by itself.
	code = decode[map[string]string](t, call(t, h, http.MethodPost, "/api/connect", map[string]string{"app": "Assistant", "redirect_uri": back}, user...))["code"]
	app = []string{"Authorization", "Bearer " + decode[map[string]string](t, call(t, h, http.MethodPost, "/api/connect/token", map[string]string{"code": code, "redirect_uri": back}))["token"]}
	if w := call(t, h, http.MethodDelete, "/api/connect", nil, app...); w.Code != http.StatusOK {
		t.Fatalf("leave: %d", w.Code)
	}
	if apps := decode[[]db.App](t, call(t, h, http.MethodGet, "/api/apps", nil, user...)); len(apps) != 0 {
		t.Fatalf("apps after leaving: %+v", apps)
	}
}
