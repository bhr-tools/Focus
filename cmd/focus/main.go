// Command focus drives a Focus server from the command line and status
// bars: the timer's state (once or as a stream) and its actions. The server
// itself is focus-server.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bhr-tools/focus/internal/db"
	"github.com/bhr-tools/focus/internal/timer"
)

const localURL = "http://127.0.0.1:8765"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		printHelp()
		return
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "status":
		runStatus(rest)
	case "watch", "--watch", "-w":
		runStatus(append([]string{"--watch"}, rest...))
	case "login":
		runLogin(rest)
	case "work", "pomodoro":
		sendAction("start", map[string]any{"mode": db.ModePomodoro})
	case "stopwatch", "chrono", "free":
		sendAction("start", map[string]any{"mode": db.ModeFree})
	case "start":
		runStart(rest)
	case "toggle", "pause", "resume", "next", "cancel":
		sendAction(cmd, nil)
	case "stop", "finish":
		sendAction("stop", nil)
	case "help", "-h", "--help":
		printHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		printHelp()
		os.Exit(2)
	}
}

func printHelp() {
	fmt.Print(`Focus - drive a Focus server

Usage:
  focus login <url>            Sign in to a Focus server (asks for its password)
  focus status [--watch] [--json]
                               Print the current status (--watch: one line per change)
  focus work                   Start a Pomodoro work session
  focus stopwatch              Start a stopwatch session
  focus start [-p project] [-m pomodoro|free]
                               Start a session for a project
  focus toggle                 Start / pause / resume
  focus pause | resume         Pause or resume the current session
  focus next                   Switch phase (work -> break, break -> work)
  focus stop                   Stop and record the current session
  focus cancel                 Cancel the current session without recording

Environment:
  FOCUS_SERVER                 Server URL (default: saved by 'focus login', else ` + localURL + `)
  FOCUS_TOKEN                  Token sent to the server (default: saved by 'focus login')
`)
}

// ==================== Server target & HTTP ====================

func configDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "focus")
}

func readTrimmed(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// target returns the server URL and the token to send to it. The token saved
// by 'focus login' only ever goes to the server it was saved for.
func target() (serverURL, token string) {
	savedServer, savedToken := "", ""
	if dir := configDir(); dir != "" {
		savedServer = strings.TrimRight(readTrimmed(filepath.Join(dir, "server")), "/")
		savedToken = readTrimmed(filepath.Join(dir, "token"))
	}
	serverURL = strings.TrimRight(os.Getenv("FOCUS_SERVER"), "/")
	if serverURL == "" {
		serverURL = savedServer
	}
	if serverURL == "" {
		serverURL = localURL
	}
	switch {
	case os.Getenv("FOCUS_TOKEN") != "":
		token = os.Getenv("FOCUS_TOKEN")
	case savedToken != "" && serverURL == savedServer:
		token = savedToken
	}
	return serverURL, token
}

// request performs an authenticated HTTP request to the target server.
func request(method, path string, body io.Reader, timeout time.Duration) (*http.Response, error) {
	serverURL, token := target()
	req, err := http.NewRequest(method, serverURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach Focus server %s: %w", serverURL, err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, fmt.Errorf("unauthorized: run 'focus login %s'", serverURL)
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("server error (%d): %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return resp, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "✗", err)
	os.Exit(1)
}

// ==================== Status ====================

func currentSnapshot() (*timer.StateSnapshot, error) {
	resp, err := request(http.MethodGet, "/api/state", nil, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var snap timer.StateSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// statusLine renders a snapshot (or the offline state) as one output line.
func statusLine(snap *timer.StateSnapshot, asJSON bool) string {
	if !asJSON {
		if snap == nil {
			return "Focus offline"
		}
		return snap.Text
	}
	if snap == nil {
		return `{"state":"offline","status":"offline","text":"","tooltip":"Focus server unreachable"}`
	}
	data, _ := json.Marshal(snap)
	return string(data)
}

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the raw JSON snapshot")
	watch := fs.Bool("watch", false, "print a new line on every change")
	_ = fs.Parse(args)

	snap, _ := currentSnapshot()
	last := statusLine(snap, *asJSON)
	fmt.Println(last)
	if !*watch {
		return
	}

	// The server cannot reach this desktop: the watcher relays its alerts.
	var lastAlert int64
	if snap != nil {
		lastAlert = snap.AlertID
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		snap, _ := currentSnapshot()
		if snap != nil {
			if snap.AlertID > lastAlert {
				go desktopAlert(snap.AlertType)
			}
			lastAlert = snap.AlertID
		}
		if line := statusLine(snap, *asJSON); line != last {
			last = line
			fmt.Println(line)
		}
	}
}

func desktopAlert(alertType string) {
	title, body := timer.AlertMessage(alertType)
	if _, err := exec.LookPath("notify-send"); err == nil {
		_ = exec.Command("notify-send", "--category=system_alert", title, body).Run()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	sound := filepath.Join(home, ".config", "deepwork-alarm.mp3")
	if _, err := os.Stat(sound); err != nil {
		return
	}
	for _, player := range []string{"pw-play", "paplay"} {
		if _, err := exec.LookPath(player); err == nil {
			_ = exec.Command(player, sound).Run()
			return
		}
	}
}

// ==================== Actions ====================

func sendAction(action string, extra map[string]any) {
	payload := map[string]any{"action": action, "client_id": "cli"}
	for k, v := range extra {
		payload[k] = v
	}
	body, _ := json.Marshal(payload)
	resp, err := request(http.MethodPost, "/api/action", bytes.NewReader(body), 5*time.Second)
	if err != nil {
		fail(err)
	}
	defer resp.Body.Close()
	var snap timer.StateSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		fail(fmt.Errorf("invalid server response: %w", err))
	}
	fmt.Printf("[%s] %s (%s)\n", snap.Project, snap.Text, snap.State)
}

func runStart(args []string) {
	var project string
	mode := db.ModePomodoro
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case (arg == "-p" || arg == "--project") && i+1 < len(args):
			i++
			project = args[i]
		case (arg == "-m" || arg == "--mode") && i+1 < len(args):
			i++
			mode = args[i]
		case !strings.HasPrefix(arg, "-"):
			project = arg
		}
	}
	// An empty project keeps the current one.
	sendAction("start", map[string]any{"project": project, "mode": mode})
}

// ==================== Login ====================

func readPassword() string {
	// Hide the typed password when stdin is a terminal.
	if err := sttyRun("-echo"); err == nil {
		defer func() {
			_ = sttyRun("echo")
			fmt.Println()
		}()
	}
	text, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimRight(text, "\r\n")
}

func sttyRun(arg string) error {
	cmd := exec.Command("stty", arg)
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

// runLogin signs in with the password, as the web app does, then asks for a
// lasting token named after this machine (revocable from the web app).
func runLogin(args []string) {
	serverURL := ""
	if len(args) > 0 {
		serverURL = strings.TrimRight(strings.TrimSpace(args[0]), "/")
	}
	if serverURL == "" {
		serverURL, _ = target()
	}

	fmt.Printf("Connecting to Focus server: %s\nPassword: ", serverURL)
	password := readPassword()
	if strings.TrimSpace(password) == "" {
		fail(errors.New("empty password"))
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 15 * time.Second, Jar: jar}
	post := func(path string, payload any) *http.Response {
		body, _ := json.Marshal(payload)
		resp, err := client.Post(serverURL+path, "application/json", bytes.NewReader(body))
		if err != nil {
			fail(fmt.Errorf("cannot reach %s: %w", serverURL, err))
		}
		return resp
	}
	resp := post("/api/auth/login", map[string]string{"password": password})
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		fail(errors.New("incorrect password"))
	case resp.StatusCode != http.StatusOK:
		fail(fmt.Errorf("server error (%d)", resp.StatusCode))
	}

	host, _ := os.Hostname()
	resp = post("/api/cli-token", map[string]string{"name": "Command line · " + host})
	defer resp.Body.Close()
	var res struct {
		Token string `json:"token"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&res) != nil || res.Token == "" {
		fail(fmt.Errorf("no token received from the server (%d)", resp.StatusCode))
	}

	dir := configDir()
	if dir == "" {
		fail(errors.New("home directory not found"))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fail(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte(res.Token), 0o600); err != nil {
		fail(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server"), []byte(serverURL), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("✓ Signed in. Token saved for %s in %s\n", serverURL, dir)
}
