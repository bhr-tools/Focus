// Package httpx holds the JSON helpers every app's API uses: answers,
// errors mapped to statuses, request decoding and the cross-site guard.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// MaxJSONBody caps the size of a JSON request body.
const MaxJSONBody = 1 << 20 // 1 MiB

// ErrNotFound is returned by stores when the requested row does not exist;
// Fail answers it with 404.
var ErrNotFound = errors.New("not found")

// ValidationError reports rejected input. Its message is meant for the user;
// Fail answers it with 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// Invalid returns a ValidationError with a formatted message.
func Invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// WriteJSON answers with v as JSON, never cached.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

// WriteError answers {"error": msg}.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// Fail answers with the status matching err: 404, 400 for invalid input, or
// 500 (logged, with a generic message).
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *ValidationError
	switch {
	case errors.Is(err, ErrNotFound):
		WriteError(w, http.StatusNotFound, "Not found")
	case errors.As(err, &invalid):
		WriteError(w, http.StatusBadRequest, invalid.Msg)
	default:
		log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		WriteError(w, http.StatusInternalServerError, "Internal server error")
	}
}

// Decode reads a JSON request body into dst. On failure it answers the
// request and returns false.
func Decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	body := http.MaxBytesReader(w, r.Body, MaxJSONBody)
	if err := json.NewDecoder(body).Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			WriteError(w, http.StatusRequestEntityTooLarge, "Request body too large")
		case errors.Is(err, io.EOF):
			WriteError(w, http.StatusBadRequest, "Request body is empty")
		default:
			WriteError(w, http.StatusBadRequest, "Invalid JSON: "+err.Error())
		}
		return false
	}
	return true
}

// RejectCrossSite refuses state-changing requests sent by another site
// (CSRF). Browsers label every request with Sec-Fetch-Site; requests without
// it (curl, scripts) are not browser-driven and are let through.
func RejectCrossSite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			WriteError(w, http.StatusForbidden, "Cross-site request refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// OptionalString trims s; empty becomes nil.
func OptionalString(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}
