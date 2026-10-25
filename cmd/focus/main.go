// Command focus runs the Focus daemon and drives it from the command line.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"focus/internal/db"
	"focus/internal/paths"
	"focus/internal/server"
	"focus/internal/timer"
	"focus/web"
)

const (
	defaultPort = 8765
	defaultHost = "127.0.0.1"
	localURL    = "http://127.0.0.1:8765"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		runDaemon(nil)
		return
	}

	switch cmd, rest := args[0], args[1:]; cmd {
	case "daemon", "server":
		runDaemon(rest)
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
	case "menu":
		runMenu()
	case "export":
		runExport(rest)
	case "import":
		runImport(rest)
	case "import-timewarrior":
		runImportTimewarrior(rest)
	case "help", "-h", "--help":
		printHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		printHelp()
		os.Exit(2)
	}
}

func printHelp() {
	fmt.Print(`Focus - Minimalist Rosé Pine Focus Timer & Pomodoro

Usage:
  focus [daemon]               Start the HTTP server and web UI
      -host <addr>             Address to bind (default: 127.0.0.1, or $HOST)
      -port <int>              Port to listen on (default: 8765, or $PORT)
      -db <path>               SQLite database (default: ~/.local/share/focus/focus.db, or $DB_PATH)
  focus status [--watch] [--json]
                               Print the current status (--watch: one line per change)
  focus login [url]            Authenticate the CLI against a (remote) Focus server
  focus work                   Start a Pomodoro work session
  focus stopwatch              Start a stopwatch session
  focus start [-p project] [-m pomodoro|free]
                               Start a session for a project
  focus toggle                 Start / pause / resume
  focus pause | resume         Pause or resume the current session
  focus next                   Switch phase (work -> break, break -> work)
  focus stop                   Stop and record the current session
  focus cancel                 Cancel the current session without recording
  focus menu                   Interactive menu (fuzzel, wofi or rofi)
  focus export [--json|--csv|--timew] [--local] [file]
                               Export every session
  focus import [--local] <file>
                               Import sessions (Focus JSON, Timewarrior JSON or CSV)
  focus import-timewarrior [--local]
                               Import the output of 'timew export'

Environment:
  FOCUS_SERVER                 Server URL used by the CLI (default: saved by 'focus login', else ` + localURL + `)
  FOCUS_TOKEN                  Daemon: fixed CLI token. CLI: token sent to the server.
`)
}

// ==================== Daemon ====================

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// localTokenPath is the per-user file holding the local daemon's CLI token.
func localTokenPath() string {
	path, err := paths.File("local_token")
	if err != nil {
		return ""
	}
	return path
}

func runDaemon(args []string) {
	defaultPortValue := defaultPort
	if p, err := strconv.Atoi(os.Getenv("PORT")); err == nil && p > 0 {
		defaultPortValue = p
	}
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	port := fs.Int("port", defaultPortValue, "port to listen on")
	host := fs.String("host", envOr("HOST", defaultHost), "address to bind")
	dbPath := fs.String("db", os.Getenv("DB_PATH"), "path to the SQLite database")
	_ = fs.Parse(args)

	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer database.Close()

	engine := timer.NewEngine(database, timer.StateFilePath())
	defer engine.Close()

	// The local token lets the CLI of the same user talk to a password-protected
	// daemon. A generated token that cannot be stored privately is disabled.
	token := os.Getenv("FOCUS_TOKEN")
	if token == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		token = hex.EncodeToString(b)
		path := localTokenPath()
		if path == "" {
			token = ""
		} else if err := paths.WriteFileAtomic(path, []byte(token)); err != nil {
			log.Printf("warning: local CLI token disabled: %v", err)
			token = ""
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	addr := net.JoinHostPort(*host, strconv.Itoa(*port))
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.NewServer(database, engine, web.Files, token).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// Request contexts end with the daemon, which closes the SSE streams.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("Focus listening on http://%s", addr)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http server: %v", err)
			engine.Close()
			database.Close()
			os.Exit(1)
		}
	case <-ctx.Done():
		log.Print("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}
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
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// target returns the server URL and the token to send to it. A credential is
// only ever sent to the server it belongs to: $FOCUS_TOKEN is explicit, the
// token saved by 'focus login' goes to the saved server only, and the local
// daemon's token to the local daemon only.
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
	case isLocal(serverURL):
		token = readTrimmed(localTokenPath())
	}
	return serverURL, token
}

func isLocal(serverURL string) bool {
	return serverURL == localURL
}

// request performs an authenticated HTTP request to the target server.
func request(method, path string, body io.Reader, contentType string, timeout time.Duration) (*http.Response, error) {
	serverURL, token := target()
	req, err := http.NewRequest(method, serverURL+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
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
		return nil, fmt.Errorf("unauthorized: run 'focus login' to authenticate with %s", serverURL)
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

// currentSnapshot reads the local state file when targeting the local daemon
// (no HTTP round-trip, works for any user), and falls back to the API.
func currentSnapshot() (*timer.StateSnapshot, error) {
	serverURL, _ := target()
	if isLocal(serverURL) {
		if data, err := os.ReadFile(timer.StateFilePath()); err == nil {
			var snap timer.StateSnapshot
			if err := json.Unmarshal(data, &snap); err == nil {
				return &snap, nil
			}
		}
	}
	resp, err := request(http.MethodGet, "/api/state", nil, "", 2*time.Second)
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
		return `{"state":"offline","status":"offline","text":"","tooltip":"Focus daemon unreachable"}`
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

	// The daemon notifies on its own host; a watcher of a remote server
	// relays its alerts to this desktop.
	serverURL, _ := target()
	notify := !isLocal(serverURL)
	var lastAlert int64
	if snap != nil {
		lastAlert = snap.AlertID
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		snap, _ := currentSnapshot()
		if snap != nil {
			if notify && snap.AlertID > lastAlert {
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
	resp, err := request(http.MethodPost, "/api/action", bytes.NewReader(body), "application/json", 5*time.Second)
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

func runLogin(args []string) {
	serverURL := ""
	if len(args) > 0 {
		serverURL = strings.TrimRight(strings.TrimSpace(args[0]), "/")
	}
	if serverURL == "" {
		serverURL, _ = target()
	}
	if serverURL == "" {
		fail(errors.New("usage: focus login <server url>"))
	}

	fmt.Printf("Connecting to Focus server: %s\nPassword: ", serverURL)
	password := readPassword()
	if strings.TrimSpace(password) == "" {
		fail(errors.New("empty password"))
	}

	body, _ := json.Marshal(map[string]string{"password": password})
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Post(serverURL+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		fail(fmt.Errorf("cannot reach %s: %w", serverURL, err))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		fail(errors.New("incorrect password"))
	}
	if resp.StatusCode != http.StatusOK {
		fail(fmt.Errorf("server error (%d)", resp.StatusCode))
	}
	var res struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil || res.Token == "" {
		fail(errors.New("no session token received from server"))
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
	fmt.Printf("✓ Authenticated. Token saved for %s in %s\n", serverURL, dir)
}

// ==================== Import / export ====================

func runExport(args []string) {
	format, outPath, local := db.FormatJSON, "", false
	for _, arg := range args {
		switch strings.TrimLeft(arg, "-") {
		case "h", "help":
			fmt.Println("Usage: focus export [--json|--csv|--timew] [--local] [output_file]")
			return
		case "json", "csv", "timew", "timewarrior":
			format = db.NormalizeExportFormat(strings.TrimLeft(arg, "-"))
		case "local":
			local = true
		default:
			if !strings.HasPrefix(arg, "-") && outPath == "" {
				outPath = arg
			}
		}
	}
	if outPath == "" {
		outPath = db.ExportFileName(format, time.Now())
	}

	var buf bytes.Buffer
	serverURL, _ := target()
	if !local {
		resp, err := request(http.MethodGet, "/api/export?format="+format, nil, "", time.Minute)
		if err == nil {
			_, err = io.Copy(&buf, resp.Body)
			resp.Body.Close()
		}
		// Only the local daemon's own database is a valid fallback.
		if err != nil && !isLocal(serverURL) {
			fail(err)
		}
		local = err != nil
	}
	if local {
		buf.Reset()
		if err := withLocalDB(func(d *db.DB) error { return d.WriteExport(&buf, format) }); err != nil {
			fail(err)
		}
	}
	if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("✓ Exported %s (%d bytes) -> %s\n", strings.ToUpper(format), buf.Len(), outPath)
}

func runImport(args []string) {
	var path string
	local := false
	for _, arg := range args {
		switch strings.TrimLeft(arg, "-") {
		case "h", "help":
			fmt.Println("Usage: focus import [--local] <file.json|file.csv>")
			fmt.Println("Formats: Focus JSON export, Timewarrior JSON export, CSV (Project, Start Time, End Time, Duration, Note)")
			return
		case "local":
			local = true
		default:
			if !strings.HasPrefix(arg, "-") && path == "" {
				path = arg
			}
		}
	}
	if path == "" {
		fail(errors.New("usage: focus import [--local] <file>"))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fail(err)
	}
	importData(data, local)
}

func runImportTimewarrior(args []string) {
	out, err := exec.Command("timew", "export").Output()
	if err != nil {
		fail(fmt.Errorf("'timew export' failed: %w", err))
	}
	importData(out, len(args) > 0 && strings.TrimLeft(args[0], "-") == "local")
}

// importData sends data to the server, or imports it into the local
// database when asked to or when the local daemon is not running.
func importData(data []byte, local bool) {
	sessions, err := db.ParseImportSessions(data)
	if err != nil {
		fail(err)
	}

	var res struct {
		Imported int `json:"imported"`
		Skipped  int `json:"skipped"`
	}
	serverURL, _ := target()
	if !local {
		resp, err := request(http.MethodPost, "/api/import", bytes.NewReader(data), "application/octet-stream", time.Minute)
		if err == nil {
			err = json.NewDecoder(resp.Body).Decode(&res)
			resp.Body.Close()
		}
		if err != nil && !isLocal(serverURL) {
			fail(err)
		}
		local = err != nil
	}
	if local {
		err := withLocalDB(func(d *db.DB) (err error) {
			res.Imported, res.Skipped, err = d.ImportSessions(sessions)
			return err
		})
		if err != nil {
			fail(err)
		}
	}
	fmt.Printf("✓ Imported %d sessions, skipped %d (duplicates or invalid) out of %d\n", res.Imported, res.Skipped, len(sessions))
}

func withLocalDB(fn func(*db.DB) error) error {
	d, err := db.Open(os.Getenv("DB_PATH"))
	if err != nil {
		return fmt.Errorf("open local database: %w", err)
	}
	defer d.Close()
	return fn(d)
}

// ==================== Menu ====================

type menuItem struct {
	label string
	run   func()
}

func action(name string, extra map[string]any) func() {
	return func() { sendAction(name, extra) }
}

// dmenu shows items in the first available launcher and returns the chosen line.
func dmenu(prompt string, lines []string) (string, bool) {
	var cmd *exec.Cmd
	switch {
	case lookPath("fuzzel"):
		cmd = exec.Command("fuzzel", "--dmenu", "-p", prompt, "-l", strconv.Itoa(len(lines)))
	case lookPath("wofi"):
		cmd = exec.Command("wofi", "--dmenu", "-p", prompt)
	case lookPath("rofi"):
		cmd = exec.Command("rofi", "-dmenu", "-p", prompt)
	default:
		fail(errors.New("none of fuzzel, wofi or rofi was found in PATH"))
	}
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		return "", false // dismissed
	}
	return strings.TrimSpace(string(out)), true
}

func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func runMenu() {
	snap, _ := currentSnapshot()
	if snap == nil {
		snap = &timer.StateSnapshot{Status: timer.StatusIdle}
	}

	prompt := "Focus > "
	if snap.Text != "" {
		prompt = fmt.Sprintf("Focus [%s - %s] > ", snap.Project, snap.Text)
	}

	confirmCancel := func() {
		yes := "✓  Yes, cancel session"
		if choice, ok := dmenu("Cancel session? > ", []string{"✕  No, continue session", yes}); ok && choice == yes {
			sendAction("cancel", nil)
		}
	}
	pomodoroBreak := snap.Mode == db.ModePomodoro && snap.Phase == timer.PhaseBreak

	var items []menuItem
	switch snap.Status {
	case timer.StatusRunning, timer.StatusPaused:
		if snap.Status == timer.StatusRunning {
			items = append(items, menuItem{"⏸  Pause", action("pause", nil)})
		} else {
			items = append(items, menuItem{"▶  Resume", action("resume", nil)})
		}
		if snap.Mode == db.ModePomodoro {
			label := "⏭  Next (Pass to Break)"
			if pomodoroBreak {
				label = "⏭  Next (Pass to Work)"
			}
			items = append(items, menuItem{label, action("next", nil)})
		}
		if !pomodoroBreak {
			items = append(items, menuItem{"⏹  Stop & Record", action("stop", nil)})
		}
		items = append(items, menuItem{"✕  Cancel", confirmCancel})
	default:
		items = append(items,
			menuItem{"▶  Start Work (Pomodoro)", action("start", map[string]any{"mode": db.ModePomodoro})},
			menuItem{"⏱  Start Stopwatch", action("start", map[string]any{"mode": db.ModeFree})},
		)
	}

	labels := make([]string, len(items))
	for i, it := range items {
		labels[i] = it.label
	}
	choice, ok := dmenu(prompt, labels)
	if !ok {
		return
	}
	for _, it := range items {
		if it.label == choice {
			it.run()
			return
		}
	}
}
