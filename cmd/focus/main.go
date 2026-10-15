package main

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"focus/internal/db"
	"focus/internal/server"
	"focus/internal/timer"
	"focus/web"
)

const (
	defaultPort = 8765
	daemonURL   = "http://127.0.0.1:8765"
	statePath   = "/tmp/focus_state.json"
)

func main() {
	if len(os.Args) < 2 {
		runServer(nil)
		return
	}

	command := os.Args[1]
	switch command {
	case "server", "daemon":
		runServer(os.Args[2:])
	case "status", "--watch", "-w", "watch":
		if command == "status" {
			handleStatus(os.Args[2:])
		} else {
			handleStatus(os.Args[1:])
		}
	case "login":
		handleLogin(os.Args[2:])
	case "work", "pomodoro":
		sendAction("start", map[string]any{"mode": "pomodoro"})
	case "stopwatch", "chrono", "free":
		sendAction("start", map[string]any{"mode": "free"})
	case "toggle":
		sendAction("toggle", nil)
	case "pause":
		sendAction("pause", nil)
	case "resume":
		sendAction("resume", nil)
	case "stop", "finish":
		sendAction("stop", nil)
	case "next":
		sendAction("next", nil)
	case "cancel":
		sendAction("cancel", nil)
	case "menu":
		handleFuzzelMenu()
	case "start":
		handleStart(os.Args[2:])
	case "export":
		handleExportCLI(os.Args[2:])
	case "import":
		handleImportCLI(os.Args[2:])
	case "import-timewarrior":
		handleImportTimewarrior()
	case "help", "-h", "--help":
		printHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		printHelp()
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Print(`Focus - Minimalist Rosé Pine Focus Timer & Pomodoro

Usage:
  focus [daemon]               Start server and web UI daemon
      -port <int>              Port to listen on (default: 8765 or $PORT)
      -db <path>               Path to SQLite database (default: ~/.local/share/focus/focus.db)
  focus status [options]       Output current status text (Work: Xmin, Break: Xmin, Stopwatch: Xmin)
      --watch                  Watch continuously (stream text lines)
      --json                   Raw JSON state snapshot
  focus login                  Authenticate CLI with remote Focus server
  focus work                   Start Pomodoro work session (active project)
  focus stopwatch              Start free stopwatch session (active project)
  focus toggle                 Toggle timer (run / pause / resume)
  focus pause                  Pause current session
  focus resume                 Resume current session
  focus next                   Next session (work -> break, or break -> work)
  focus stop                   Stop & save current session
  focus cancel                 Cancel current session without saving
  focus menu                   Interactive Fuzzel menu for Hyprland / Waybar
  focus export [--timew|--csv|--json]  Export all sessions (Timewarrior, CSV or JSON)
  focus import <file>          Import sessions from Timewarrior, CSV or JSON file
`)
}

func runServer(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	portFlag := fs.Int("port", defaultPort, "Port to listen on")
	hostFlag := fs.String("host", "0.0.0.0", "Host address to bind to")
	dbFlag := fs.String("db", "", "Path to SQLite database")
	_ = fs.Parse(args)

	port := *portFlag
	if envPort := os.Getenv("PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil && p > 0 {
			port = p
		}
	}

	host := *hostFlag
	if envHost := os.Getenv("HOST"); envHost != "" {
		host = envHost
	}

	dbPath := *dbFlag
	if dbPath == "" {
		dbPath = os.Getenv("DB_PATH")
	}

	database, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Database error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	engine := timer.NewEngine(database)
	defer engine.Close()

	srv := server.NewServer(database, engine, web.Files)

	addr := fmt.Sprintf("%s:%d", host, port)
	fmt.Printf("🎯 Focus started on http://%s\n", addr)

	httpServer := &http.Server{
		Addr:    addr,
		Handler: srv.Handler(),
	}

	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "HTTP server error: %v\n", err)
		os.Exit(1)
	}
}

var remoteHTTPClient = &http.Client{
	Timeout: 2 * time.Second,
}

func handleStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "Output raw JSON status")
	watch := fs.Bool("watch", false, "Watch continuously")
	_ = fs.Parse(args)

	if !*watch {
		printCurrentStatus(*jsonOut)
		return
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Initial print and baseline
	lastData := printCurrentStatus(*jsonOut)
	_ = os.Stdout.Sync()

	serverURL, token := getTargetServer()
	isRemote := serverURL != daemonURL && serverURL != ""

	var lastAlertID int64
	var initialized bool
	checkAlert := func(snap *timer.StateSnapshot) {
		if snap == nil {
			return
		}
		if !initialized {
			initialized = true
			lastAlertID = snap.AlertID // initial baseline from first poll
			return
		}
		if snap.AlertID > 0 && snap.AlertID > lastAlertID {
			lastAlertID = snap.AlertID
			go triggerDesktopAlert(snap)
		} else if snap.AlertID == 0 {
			lastAlertID = 0
		}
	}

	for range ticker.C {
		if isRemote {
			if snap, err := fetchRemoteSnapshot(serverURL, token); err == nil {
				checkAlert(snap)
				if *jsonOut {
					dataBytes, _ := json.Marshal(snap)
					str := string(dataBytes)
					if str != lastData {
						lastData = str
						fmt.Println(str)
						_ = os.Stdout.Sync()
					}
				} else {
					if snap.Text != lastData {
						lastData = snap.Text
						fmt.Println(snap.Text)
						_ = os.Stdout.Sync()
					}
				}
			}
			continue
		}

		data, err := readStateData()
		if err != nil {
			if snap, errHTTP := fetchRemoteSnapshot(serverURL, token); errHTTP == nil {
				checkAlert(snap)
				if *jsonOut {
					dataBytes, _ := json.Marshal(snap)
					str := string(dataBytes)
					if str != lastData {
						lastData = str
						fmt.Println(str)
						_ = os.Stdout.Sync()
					}
				} else {
					if snap.Text != lastData {
						lastData = snap.Text
						fmt.Println(snap.Text)
						_ = os.Stdout.Sync()
					}
				}
			}
			continue
		}
		var snap timer.StateSnapshot
		if err := json.Unmarshal(data, &snap); err == nil {
			checkAlert(&snap)
			if *jsonOut {
				str := string(data)
				if str != lastData {
					lastData = str
					fmt.Println(str)
					_ = os.Stdout.Sync()
				}
			} else {
				if snap.Text != lastData {
					lastData = snap.Text
					fmt.Println(snap.Text)
					_ = os.Stdout.Sync()
				}
			}
		}
	}
}

func fetchRemoteSnapshot(serverURL, token string) (*timer.StateSnapshot, error) {
	req, err := http.NewRequest("GET", serverURL+"/api/state", nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := remoteHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("unauthorized")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status code %d", resp.StatusCode)
	}
	var snap timer.StateSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func printCurrentStatus(jsonOut bool) string {
	serverURL, token := getTargetServer()
	// Prioritize network query if remote server configured
	if serverURL != daemonURL && serverURL != "" {
		if snap, err := fetchRemoteSnapshot(serverURL, token); err == nil {
			if jsonOut {
				b, _ := json.Marshal(snap)
				str := string(b)
				fmt.Println(str)
				return str
			}
			fmt.Println(snap.Text)
			return snap.Text
		}
	}

	data, err := readStateData()
	if err != nil {
		if snap, errHTTP := fetchRemoteSnapshot(serverURL, token); errHTTP == nil {
			if jsonOut {
				b, _ := json.Marshal(snap)
				str := string(b)
				fmt.Println(str)
				return str
			}
			fmt.Println(snap.Text)
			return snap.Text
		}

		if jsonOut {
			str := `{"state":"idle","text":"Work: 60min"}`
			fmt.Println(str)
			return str
		}
		str := "Work: 60min"
		fmt.Println(str)
		return str
	}

	var snap timer.StateSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		if jsonOut {
			str := `{"state":"idle","text":"Work: 60min"}`
			fmt.Println(str)
			return str
		}
		str := "Work: 60min"
		fmt.Println(str)
		return str
	}

	if jsonOut {
		str := string(data)
		fmt.Println(str)
		return str
	}
	fmt.Println(snap.Text)
	return snap.Text
}



func readStateData() ([]byte, error) {
	if data, err := os.ReadFile(statePath); err == nil {
		return data, nil
	}
	return os.ReadFile("/tmp/timetracker_state.json")
}

func getTargetServer() (string, string) {
	url := os.Getenv("FOCUS_SERVER")
	if url == "" {
		if home, err := os.UserHomeDir(); err == nil {
			srvPath := filepath.Join(home, ".config", "focus", "server")
			if data, err := os.ReadFile(srvPath); err == nil {
				url = strings.TrimSpace(string(data))
			} else {
				// Legacy fallback
				legacyPath := filepath.Join(home, ".config", "timetracker", "server")
				if data, err := os.ReadFile(legacyPath); err == nil {
					url = strings.TrimSpace(string(data))
				}
			}
		}
	}
	if url == "" {
		// Fallback: if token exists, user is connecting to the remote server
		if home, err := os.UserHomeDir(); err == nil {
			tokPath := filepath.Join(home, ".config", "focus", "token")
			legacyTok := filepath.Join(home, ".config", "timetracker", "token")
			if _, err := os.Stat(tokPath); err == nil {
				url = "https://focus.bhr-sys.com"
			} else if _, err := os.Stat(legacyTok); err == nil {
				url = "https://focus.bhr-sys.com"
			}
		}
	}
	if url == "" {
		url = daemonURL
	}
	url = strings.TrimRight(url, "/")

	token := os.Getenv("FOCUS_TOKEN")
	if token == "" {
		if home, err := os.UserHomeDir(); err == nil {
			tokPath := filepath.Join(home, ".config", "focus", "token")
			if data, err := os.ReadFile(tokPath); err == nil {
				token = strings.TrimSpace(string(data))
			} else {
				// Legacy fallback
				legacyTok := filepath.Join(home, ".config", "timetracker", "token")
				if data, err := os.ReadFile(legacyTok); err == nil {
					token = strings.TrimSpace(string(data))
				}
			}
		}
	}
	if token == "" {
		if data, err := os.ReadFile("/tmp/focus_local_token"); err == nil {
			token = strings.TrimSpace(string(data))
		}
	}
	return url, token
}

func saveToken(token string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".config", "focus")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tokPath := filepath.Join(dir, "token")
	return os.WriteFile(tokPath, []byte(token), 0600)
}

func saveServer(serverURL string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".config", "focus")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	srvPath := filepath.Join(dir, "server")
	return os.WriteFile(srvPath, []byte(serverURL), 0644)
}

func readPasswordPrompt() string {
	// Disable terminal echo
	cmd := exec.Command("stty", "-echo")
	cmd.Stdin = os.Stdin
	_ = cmd.Run()
	defer func() {
		cmdReset := exec.Command("stty", "echo")
		cmdReset.Stdin = os.Stdin
		_ = cmdReset.Run()
		fmt.Println()
	}()

	reader := bufio.NewReader(os.Stdin)
	text, _ := reader.ReadString('\n')
	return strings.TrimSpace(text)
}

func handleLogin(args []string) {
	var targetURL string
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		targetURL = strings.TrimSpace(args[0])
	} else {
		targetURL, _ = getTargetServer()
		if targetURL == daemonURL || targetURL == "" {
			targetURL = "https://focus.bhr-sys.com"
		}
	}
	serverURL := strings.TrimRight(targetURL, "/")

	fmt.Printf("Connecting to Focus server: %s\n", serverURL)
	fmt.Print("Password: ")
	pwd := readPasswordPrompt()
	if pwd == "" {
		fmt.Println("Empty password.")
		return
	}

	body, _ := json.Marshal(map[string]string{"password": pwd})
	req, err := http.NewRequest("POST", serverURL+"/api/auth/login", bytes.NewBuffer(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Request creation error: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to reach server (%s): %v\n", serverURL, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		fmt.Println("✗ Incorrect password.")
		return
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("✗ Server error (%d)\n", resp.StatusCode)
		return
	}

	var res struct {
		Ok    bool   `json:"ok"`
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&res)

	token := res.Token
	if token == "" {
		for _, c := range resp.Cookies() {
			if c.Name == "focus_session" {
				token = c.Value
				break
			}
		}
	}

	if token == "" {
		fmt.Println("✗ No session token received from server.")
		return
	}

	if err := saveToken(token); err != nil {
		fmt.Fprintf(os.Stderr, "Token save error: %v\n", err)
		return
	}
	_ = saveServer(serverURL)

	// Also write to /tmp/focus_local_token for compatibility
	_ = os.WriteFile("/tmp/focus_local_token", []byte(token), 0600)

	fmt.Printf("✓ Authentication successful! Token saved for %s in ~/.config/focus/\n", serverURL)
}

func triggerDesktopAlert(snap *timer.StateSnapshot) {
	title := "Deepwork session over"
	msg := "Time for a break."
	if snap.AlertType == "break_ended" {
		title = "Break over"
		msg = "Prepare a new session for deepwork."
	}

	// 1. Notify-send with system_alert category for Mako on Wayland/Linux
	if _, err := exec.LookPath("notify-send"); err == nil {
		_ = exec.Command("notify-send", "--category=system_alert", title, msg).Run()
	}

	// 2. Play desktop alarm sound on PC (PipeWire or PulseAudio fallback)
	if home, err := os.UserHomeDir(); err == nil {
		alarmPath := filepath.Join(home, ".config", "deepwork-alarm.mp3")
		if _, err := os.Stat(alarmPath); err == nil {
			if _, err := exec.LookPath("pw-play"); err == nil {
				_ = exec.Command("pw-play", alarmPath).Run()
			} else if _, err := exec.LookPath("paplay"); err == nil {
				_ = exec.Command("paplay", alarmPath).Run()
			}
		}
	}
}

func sendAction(action string, extra map[string]any) {
	payload := map[string]any{"action": action, "client_id": "cli"}
	for k, v := range extra {
		payload[k] = v
	}

	body, _ := json.Marshal(payload)
	serverURL, token := getTargetServer()

	doPost := func() (*http.Response, error) {
		req, err := http.NewRequest("POST", serverURL+"/api/action", bytes.NewBuffer(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		client := &http.Client{Timeout: 3 * time.Second}
		return client.Do(req)
	}

	resp, err := doPost()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to reach Focus server (%s): %v\n", serverURL, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		fmt.Fprintf(os.Stderr, "Unauthorized: authentication required.\nRun 'focus login' to connect to %s\n", serverURL)
		os.Exit(1)
	}

	var snap timer.StateSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err == nil {
		fmt.Printf("[%s] %s (%s)\n", snap.Project, snap.Text, snap.State)
	}
}

func handleStart(args []string) {
	var proj, mode string
	for i := 0; i < len(args); i++ {
		if (args[i] == "-p" || args[i] == "--project") && i+1 < len(args) {
			proj = args[i+1]
			i++
		} else if (args[i] == "-m" || args[i] == "--mode") && i+1 < len(args) {
			mode = args[i+1]
			i++
		} else if !strings.HasPrefix(args[i], "-") {
			proj = args[i]
		}
	}

	if mode == "" {
		mode = "pomodoro"
	}
	if proj == "" {
		proj = "Work"
	}

	sendAction("start", map[string]any{
		"project": proj,
		"mode":    mode,
	})
}

func exportLocal(format, outPath string) error {
	database, err := db.Open("")
	if err != nil {
		return fmt.Errorf("unable to open local database: %w", err)
	}
	defer database.Close()

	sessions, err := database.GetAllSessions()
	if err != nil {
		return fmt.Errorf("unable to read sessions: %w", err)
	}

	var exportBytes []byte
	if format == "csv" {
		var buf bytes.Buffer
		csvWriter := csv.NewWriter(&buf)
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
		exportBytes = buf.Bytes()
	} else if format == "timewarrior" || format == "timew" {
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
		exportBytes, _ = json.MarshalIndent(timewEntries, "", "  ")
	} else {
		projs, _ := database.ListProjects()
		exportObj := struct {
			Version    int          `json:"version"`
			ExportedAt time.Time    `json:"exported_at"`
			Projects   []db.Project `json:"projects,omitempty"`
			Sessions   []db.Session `json:"sessions"`
		}{
			Version:    1,
			ExportedAt: time.Now().UTC(),
			Projects:   projs,
			Sessions:   sessions,
		}
		exportBytes, _ = json.MarshalIndent(exportObj, "", "  ")
	}

	if outPath == "" {
		ext := format
		if format == "timewarrior" || format == "timew" {
			ext = "timew.json"
		}
		outPath = fmt.Sprintf("focus-export-%s.%s", time.Now().Format("20060102-150405"), ext)
	}

	if err := os.WriteFile(outPath, exportBytes, 0644); err != nil {
		return fmt.Errorf("error writing file %s: %w", outPath, err)
	}

	fmt.Printf("✓ Local export successful (%s) -> %s (%d sessions, %d bytes)\n", strings.ToUpper(format), outPath, len(sessions), len(exportBytes))
	return nil
}

func handleExportCLI(args []string) {
	format := "json"
	var outPath string
	forceLocal := false

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--help" || arg == "-h" {
			fmt.Println("Usage: focus export [options] [output_file]")
			fmt.Println("\nOptions:")
			fmt.Println("  --timewarrior, --timew   Standard Timewarrior JSON format")
			fmt.Println("  --csv                    Spreadsheet CSV format")
			fmt.Println("  --json                   Full JSON format (default)")
			fmt.Println("  --local                  Export directly from local SQLite database")
			return
		} else if arg == "--csv" || arg == "-csv" {
			format = "csv"
		} else if arg == "--json" || arg == "-json" {
			format = "json"
		} else if arg == "--timewarrior" || arg == "--timew" || arg == "-timewarrior" || arg == "-timew" {
			format = "timewarrior"
		} else if arg == "--local" || arg == "-local" {
			forceLocal = true
		} else if !strings.HasPrefix(arg, "-") && outPath == "" {
			outPath = arg
		}
	}

	if forceLocal {
		if err := exportLocal(format, outPath); err != nil {
			fmt.Fprintf(os.Stderr, "Local export error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	serverURL, token := getTargetServer()
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/export?format=%s", serverURL, format), nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Request creation error: %v\n", err)
		os.Exit(1)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := remoteHTTPClient.Do(req)
	if err != nil {
		// Server unreachable - try local DB fallback
		if localErr := exportLocal(format, outPath); localErr == nil {
			return
		}
		fmt.Fprintf(os.Stderr, "Error connecting to server %s: %v\n", serverURL, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		if localErr := exportLocal(format, outPath); localErr == nil {
			return
		}
		fmt.Fprintf(os.Stderr, "Unauthorized: run 'focus login' to connect to %s (or use 'focus export --local')\n", serverURL)
		os.Exit(1)
	}
	if resp.StatusCode == http.StatusNotFound {
		if localErr := exportLocal(format, outPath); localErr == nil {
			return
		}
		fmt.Fprintf(os.Stderr, "Error: Remote server %s does not have the /api/export endpoint.\n", serverURL)
		os.Exit(1)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "Server error (%d): %s\n", resp.StatusCode, string(body))
		os.Exit(1)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Data read error: %v\n", err)
		os.Exit(1)
	}

	if outPath == "" {
		ext := format
		if format == "timewarrior" || format == "timew" {
			ext = "timew.json"
		}
		outPath = fmt.Sprintf("focus-export-%s.%s", time.Now().Format("20060102-150405"), ext)
	}

	if err := os.WriteFile(outPath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing file %s: %v\n", outPath, err)
		os.Exit(1)
	}

	fmt.Printf("✓ Export successful (%s) -> %s (%d bytes)\n", strings.ToUpper(format), outPath, len(data))
}

func importLocal(data []byte, filePath string) {
	sessions, err := db.ParseImportSessions(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing file %s: %v\n", filePath, err)
		os.Exit(1)
	}

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to open local database: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	imported, skipped, err := database.ImportSessions(sessions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Local import error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Local import successful: %d activities imported, %d duplicates ignored (total processed: %d)\n", imported, skipped, len(sessions))
}

func handleImportCLI(args []string) {
	var filePath string
	forceLocal := false

	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			fmt.Println("Usage: focus import [options] <file.json|file.csv>")
			fmt.Println("\nSupported formats:")
			fmt.Println("  - Timewarrior JSON ([{\"start\":..., \"end\":..., \"tags\":[...]}])")
			fmt.Println("  - Focus export JSON (full or session array)")
			fmt.Println("  - Spreadsheet CSV (columns: Project, Start Time, End Time, Duration, Note)")
			fmt.Println("\nOptions:")
			fmt.Println("  --local    Import directly into local SQLite database")
			return
		} else if arg == "--local" || arg == "-local" {
			forceLocal = true
		} else if !strings.HasPrefix(arg, "-") && filePath == "" {
			filePath = arg
		}
	}

	if filePath == "" {
		fmt.Fprintf(os.Stderr, "Usage: focus import [--local] <file.json|file.csv>\n")
		os.Exit(1)
	}

	fileData, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to read file %s: %v\n", filePath, err)
		os.Exit(1)
	}

	if forceLocal {
		importLocal(fileData, filePath)
		return
	}

	serverURL, token := getTargetServer()
	contentType := "application/json"
	if strings.HasSuffix(strings.ToLower(filePath), ".csv") {
		contentType = "text/csv"
	}

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/import", serverURL), bytes.NewReader(fileData))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Request error: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := remoteHTTPClient.Do(req)
	if err != nil {
		// Server unreachable - try local DB
		importLocal(fileData, filePath)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		fmt.Fprintf(os.Stderr, "Unauthorized: run 'focus login' to connect to %s (or use 'focus import --local %s')\n", serverURL, filePath)
		os.Exit(1)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "Import error (%d): %s\n", resp.StatusCode, string(body))
		os.Exit(1)
	}

	var res struct {
		OK       bool `json:"ok"`
		Imported int  `json:"imported"`
		Skipped  int  `json:"skipped"`
		Total    int  `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		fmt.Println("✓ Import complete.")
		return
	}

	fmt.Printf("✓ Import successful: %d activities imported, %d duplicates ignored (total processed: %d)\n", res.Imported, res.Skipped, res.Total)
}

func handleImportTimewarrior() {
	cmd := exec.Command("timew", "export")
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error executing 'timew export': %v\n", err)
		os.Exit(1)
	}

	type timewEntry struct {
		Start string   `json:"start"`
		End   string   `json:"end"`
		Tags  []string `json:"tags"`
	}

	var entries []timewEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing Timewarrior JSON: %v\n", err)
		os.Exit(1)
	}

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Database error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	const timewTimeFormat = "20060102T150405Z"
	imported := 0
	for _, e := range entries {
		st, err1 := time.Parse(timewTimeFormat, e.Start)
		et, err2 := time.Parse(timewTimeFormat, e.End)
		if err1 != nil || err2 != nil {
			continue
		}

		proj := "Default"
		if len(e.Tags) > 0 {
			proj = e.Tags[0]
		}

		dur := int(et.Sub(st).Seconds())
		if dur <= 0 {
			continue
		}

		_, err := database.RecordSession(db.Session{
			ProjectName:     proj,
			Mode:            "pomodoro",
			StartTime:       st,
			EndTime:         et,
			DurationSeconds: dur,
			Completed:       true,
		})
		if err == nil {
			imported++
		}
	}

	fmt.Printf("✓ %d Timewarrior sessions successfully imported into SQLite database!\n", imported)
}

func handleFuzzelMenu() {
	var snap timer.StateSnapshot
	serverURL, token := getTargetServer()
	if serverURL != daemonURL && serverURL != "" {
		if s, err := fetchRemoteSnapshot(serverURL, token); err == nil {
			snap = *s
		}
	} else if data, err := readStateData(); err == nil {
		_ = json.Unmarshal(data, &snap)
	} else if s, err := fetchRemoteSnapshot(serverURL, token); err == nil {
		snap = *s
	}

	type menuItem struct {
		label  string
		action string
	}

	var items []menuItem
	prompt := "Focus > "

	if snap.Project != "" && snap.Text != "" {
		prompt = fmt.Sprintf("Focus [%s - %s] > ", snap.Project, snap.Text)
	} else if snap.Text != "" {
		prompt = fmt.Sprintf("Focus [%s] > ", snap.Text)
	}

	switch snap.Status {
	case "running":
		if snap.Mode == "pomodoro" {
			if snap.Phase == "work" {
				items = append(items, menuItem{"⏸  Pause", "pause"})
				items = append(items, menuItem{"⏭  Next (Pass to Break)", "next"})
				items = append(items, menuItem{"⏹  Stop & Record", "stop"})
				items = append(items, menuItem{"✕  Cancel", "cancel"})
			} else {
				items = append(items, menuItem{"⏸  Pause Break", "pause"})
				items = append(items, menuItem{"⏭  Next (Pass to Work)", "next"})
				items = append(items, menuItem{"✕  Cancel Break", "cancel"})
			}
		} else { // free
			items = append(items, menuItem{"⏸  Pause", "pause"})
			items = append(items, menuItem{"⏹  Stop & Record", "stop"})
			items = append(items, menuItem{"✕  Cancel", "cancel"})
		}
	case "paused":
		if snap.Mode == "pomodoro" {
			if snap.Phase == "work" {
				items = append(items, menuItem{"▶  Resume Work", "resume"})
				items = append(items, menuItem{"⏭  Next (Pass to Break)", "next"})
				items = append(items, menuItem{"⏹  Stop & Record", "stop"})
				items = append(items, menuItem{"✕  Cancel", "cancel"})
			} else {
				items = append(items, menuItem{"▶  Resume Break", "resume"})
				items = append(items, menuItem{"⏭  Next (Pass to Work)", "next"})
				items = append(items, menuItem{"✕  Cancel Break", "cancel"})
			}
		} else {
			items = append(items, menuItem{"▶  Resume", "resume"})
			items = append(items, menuItem{"⏹  Stop & Record", "stop"})
			items = append(items, menuItem{"✕  Cancel", "cancel"})
		}
	default: // idle or inactive
		items = append(items, menuItem{"▶  Start Work (Pomodoro)", "work"})
		items = append(items, menuItem{"⏱  Start Stopwatch", "stopwatch"})
	}

	var menuText strings.Builder
	for _, it := range items {
		menuText.WriteString(it.label + "\n")
	}

	var cmd *exec.Cmd
	if _, err := exec.LookPath("fuzzel"); err == nil {
		cmd = exec.Command("fuzzel", "--dmenu", "-p", prompt, "-l", fmt.Sprintf("%d", len(items)))
	} else if _, err := exec.LookPath("wofi"); err == nil {
		cmd = exec.Command("wofi", "--dmenu", "-p", prompt)
	} else if _, err := exec.LookPath("rofi"); err == nil {
		cmd = exec.Command("rofi", "-dmenu", "-p", prompt)
	} else {
		fmt.Fprintf(os.Stderr, "Neither fuzzel, wofi nor rofi was found in PATH.\n")
		return
	}

	cmd.Stdin = strings.NewReader(menuText.String())
	out, err := cmd.Output()
	if err != nil {
		// Cancelled by user (e.g. Esc)
		return
	}

	selected := strings.TrimSpace(string(out))
	for _, it := range items {
		if it.label == selected {
			switch it.action {
			case "work":
				sendAction("start", map[string]any{"mode": "pomodoro"})
			case "stopwatch":
				sendAction("start", map[string]any{"mode": "free"})
			case "pause":
				sendAction("pause", nil)
			case "resume":
				sendAction("resume", nil)
			case "next":
				sendAction("next", nil)
			case "stop":
				sendAction("stop", nil)
			case "cancel":
				confirmItems := []string{"✕  No, continue session", "✓  Yes, cancel session"}
				var confirmText strings.Builder
				for _, ci := range confirmItems {
					confirmText.WriteString(ci + "\n")
				}
				var cCmd *exec.Cmd
				if _, err := exec.LookPath("fuzzel"); err == nil {
					cCmd = exec.Command("fuzzel", "--dmenu", "-p", "Cancel session? > ", "-l", "2")
				} else if _, err := exec.LookPath("wofi"); err == nil {
					cCmd = exec.Command("wofi", "--dmenu", "-p", "Cancel session? > ")
				} else if _, err := exec.LookPath("rofi"); err == nil {
					cCmd = exec.Command("rofi", "-dmenu", "-p", "Cancel session? > ")
				}
				if cCmd != nil {
					cCmd.Stdin = strings.NewReader(confirmText.String())
					cOut, cErr := cCmd.Output()
					if cErr == nil && strings.Contains(string(cOut), "Yes") {
						sendAction("cancel", nil)
					}
				} else {
					sendAction("cancel", nil)
				}
			}
			return
		}
	}
}
