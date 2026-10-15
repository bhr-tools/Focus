package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"focus/internal/db"
	"focus/internal/timer"
	"focus/web"
)

func setupTestServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_server.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() {
		database.Close()
	})

	timerEngine := timer.NewEngine(database)
	srv := NewServer(database, timerEngine, web.Files)
	return srv, database
}

func TestAPISessionsCRUD(t *testing.T) {
	srv, database := setupTestServer(t)

	now := time.Now().UTC().Truncate(time.Second)

	// 1. Create session via POST /api/sessions
	payload := map[string]interface{}{
		"project_name": "Frontend Engineering",
		"start_time":   now.Add(-45 * time.Minute).Format(time.RFC3339),
		"end_time":     now.Format(time.RFC3339),
		"mode":         "pomodoro",
	}

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewReader(body))
	w := httptest.NewRecorder()

	srv.handleSessionDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var created db.Session
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}
	if created.ID == 0 || created.ProjectName != "Frontend Engineering" {
		t.Fatalf("unexpected created session: %+v", created)
	}

	// 2. Update session via PUT /api/sessions/{id}
	updatePayload := map[string]interface{}{
		"project_name": "Frontend Engineering Refactored",
		"start_time":   now.Add(-50 * time.Minute).Format(time.RFC3339),
		"end_time":     now.Format(time.RFC3339),
	}
	updateBody, _ := json.Marshal(updatePayload)
	updateReq := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/sessions/%d", created.ID), bytes.NewReader(updateBody))
	updateW := httptest.NewRecorder()

	srv.handleSessionDetail(updateW, updateReq)
	if updateW.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on update, got %d: %s", updateW.Code, updateW.Body.String())
	}

	// 3. Delete session via DELETE /api/sessions/{id}
	delReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/sessions/%d", created.ID), nil)
	delW := httptest.NewRecorder()

	srv.handleSessionDetail(delW, delReq)
	if delW.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on delete, got %d: %s", delW.Code, delW.Body.String())
	}

	all, _ := database.GetAllSessions()
	if len(all) != 0 {
		t.Fatalf("expected 0 sessions after delete, got %d", len(all))
	}
}

func TestParseFlexibleTime(t *testing.T) {
	// 1. RFC3339 & RFC3339Nano
	t1, err := parseFlexibleTime("2026-10-15T14:30:00Z")
	if err != nil || t1.Year() != 2026 || t1.Minute() != 30 {
		t.Errorf("RFC3339 parse failed: %v, time: %v", err, t1)
	}
	t2, err := parseFlexibleTime("2026-10-15T14:30:00.999Z")
	if err != nil || t2.Nanosecond() < 900000000 {
		t.Errorf("RFC3339Nano parse failed: %v, time: %v", err, t2)
	}

	// 2. ISO local format from <input type="datetime-local">
	t3, err := parseFlexibleTime("2026-10-15T14:30:05")
	if err != nil || t3.Second() != 5 || t3.Location() != time.Local {
		t.Errorf("ISO datetime-local with seconds failed: %v, time: %v", err, t3)
	}
	t4, err := parseFlexibleTime("2026-10-15T14:30")
	if err != nil || t4.Minute() != 30 || t4.Location() != time.Local {
		t.Errorf("ISO datetime-local without seconds failed: %v, time: %v", err, t4)
	}

	// 3. Standard SQL format
	t5, err := parseFlexibleTime("2026-10-15 14:30:05")
	if err != nil || t5.Second() != 5 || t5.Location() != time.Local {
		t.Errorf("SQL format failed: %v, time: %v", err, t5)
	}

	// 4. Numeric unix timestamp in seconds (int64 / float64)
	unixSec := int64(1792065842)
	t6, err := parseFlexibleTime(unixSec)
	if err != nil || t6.Unix() != unixSec {
		t.Errorf("Unix seconds int64 failed: %v, time: %v", err, t6)
	}
	t7, err := parseFlexibleTime(float64(unixSec))
	if err != nil || t7.Unix() != unixSec {
		t.Errorf("Unix seconds float64 failed: %v, time: %v", err, t7)
	}

	// 5. Numeric unix timestamp in milliseconds (int64 / float64)
	unixMilli := int64(1792065842000)
	t8, err := parseFlexibleTime(unixMilli)
	if err != nil || t8.Unix() != unixSec {
		t.Errorf("Unix millis int64 failed: %v, time: %v", err, t8)
	}
	t9, err := parseFlexibleTime(float64(unixMilli))
	if err != nil || t9.Unix() != unixSec {
		t.Errorf("Unix millis float64 failed: %v, time: %v", err, t9)
	}
}

func TestAPISessionsResilient(t *testing.T) {
	srv, database := setupTestServer(t)

	// 1. Accept 'project' instead of 'project_name', and 'startTime'/'endTime' instead of 'start_time'/'end_time'
	// using datetime-local format
	p1 := map[string]interface{}{
		"project":   "Backend Engineering",
		"startTime": "2026-10-15T10:00",
		"endTime":   "2026-10-15T11:00",
	}
	b1, _ := json.Marshal(p1)
	req1 := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewReader(b1))
	w1 := httptest.NewRecorder()
	srv.handleSessionDetail(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w1.Code, w1.Body.String())
	}
	var s1 db.Session
	_ = json.Unmarshal(w1.Body.Bytes(), &s1)
	if s1.ProjectName != "Backend Engineering" || s1.DurationSeconds != 3600 {
		t.Errorf("unexpected session s1: %+v", s1)
	}

	// 2. Accept SQL format dates and 'duration_seconds <= 0'
	p2 := map[string]interface{}{
		"project_name":     "Systems Architecture",
		"start_time":       "2026-10-15 12:00:00",
		"end_time":         "2026-10-15 12:30:00",
		"duration_seconds": -1,
	}
	b2, _ := json.Marshal(p2)
	req2 := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewReader(b2))
	w2 := httptest.NewRecorder()
	srv.handleSessionDetail(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w2.Code, w2.Body.String())
	}
	var s2 db.Session
	_ = json.Unmarshal(w2.Body.Bytes(), &s2)
	if s2.DurationSeconds != 1800 {
		t.Errorf("expected duration 1800s, got %d", s2.DurationSeconds)
	}

	// 3. Fallback to 60s minimum if end_time == start_time
	p3 := map[string]interface{}{
		"project":    "Quick Log",
		"start_time": "2026-10-15T14:00:00Z",
		"end_time":   "2026-10-15T14:00:00Z",
	}
	b3, _ := json.Marshal(p3)
	req3 := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewReader(b3))
	w3 := httptest.NewRecorder()
	srv.handleSessionDetail(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w3.Code, w3.Body.String())
	}
	var s3 db.Session
	_ = json.Unmarshal(w3.Body.Bytes(), &s3)
	if s3.DurationSeconds != 60 {
		t.Errorf("expected 60s fallback duration, got %d", s3.DurationSeconds)
	}

	// 4. Inverted start and end time (end_time before start_time)
	p4 := map[string]interface{}{
		"project_name": "Inverted Time",
		"start_time":   "2026-10-15T16:00:00Z",
		"end_time":     "2026-10-15T15:00:00Z",
	}
	b4, _ := json.Marshal(p4)
	req4 := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewReader(b4))
	w4 := httptest.NewRecorder()
	srv.handleSessionDetail(w4, req4)
	if w4.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w4.Code, w4.Body.String())
	}
	var s4 db.Session
	_ = json.Unmarshal(w4.Body.Bytes(), &s4)
	if s4.DurationSeconds != 3600 {
		t.Errorf("expected 3600s duration for inverted start/end, got %d", s4.DurationSeconds)
	}

	// 5. Unix timestamp in milliseconds
	nowMilli := time.Now().UnixMilli()
	p5 := map[string]interface{}{
		"project":   "Timestamp Test",
		"startTime": nowMilli - 300000, // 5 min ago
		"endTime":   nowMilli,
	}
	b5, _ := json.Marshal(p5)
	req5 := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewReader(b5))
	w5 := httptest.NewRecorder()
	srv.handleSessionDetail(w5, req5)
	if w5.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w5.Code, w5.Body.String())
	}
	var s5 db.Session
	_ = json.Unmarshal(w5.Body.Bytes(), &s5)
	if s5.DurationSeconds != 300 {
		t.Errorf("expected 300s duration for millis timestamp, got %d", s5.DurationSeconds)
	}

	all, err := database.GetAllSessions()
	if err != nil || len(all) != 5 {
		t.Fatalf("expected 5 sessions in db, got %d (err: %v)", len(all), err)
	}
}
