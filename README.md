# Focus

**Focus** is a minimalist, self-hosted Pomodoro timer and time-tracking application engineered in **Go** with a clean **Rosé Pine** aesthetic. It combines precise interval tracking with a lightweight, offline-first architecture.

- **Vibe coded:** After using this kind of tools for 4+ years, I needed to create my own, that would do exactly what I wanted, extremely well, without the cost of opportunity of learning how to do everthing myself in a domain (web dev) where I don't want/need to be an expert.

- **Dual Modes:**
    
    - **Pomodoro:** Configurable work and break intervals with pause, resume, cancel, and phase-skipping controls.
        
    - **Stopwatch:** Freeform ascending time-tracking mode.
        
- **Project Color Coding:** Automatic color assignment from the official Rosé Pine palette for each tracked project.
    
- **System Theme Sync:** Seamlessly synchronizes with system color schemes (`dark` / `light`).
    
- **Local-First & Standalone:** A single Go binary with embedded SQLite and static assets. No heavy dependencies.
    

## Installation

### 1. Standalone Linux (Non-NixOS)

Focus can be built and run on any Linux distribution using the standard Go toolchain (Go 1.26+ required).

1. **Clone the repository:**
    
    ```
    git clone https://github.com/bhr-tools/Focus.git
    cd Focus
    ```
    
2. **Build the binary:**
    
    ```
    go build -o focus ./cmd/focus
    ```
    
3. **Run the daemon:**
    
    ```
    ./focus daemon
    ```
    
    Open your browser at `http://127.0.0.1:8765`.
    

### 2. Via Nix Flakes (NixOS / Home Manager)

If you use Nix with flakes enabled, you can run or install Focus directly:

- **Run directly:**
    
    ```
    nix run github:bhr-tools/Focus
    ```
    
- **Add to your Nix Flake inputs:**
    
    ```
    inputs.focus.url = "github:bhr-tools/Focus";
    ```
    

### 3. Docker

```
docker compose up -d --build
```

Open `http://127.0.0.1:8765`. Data lives in the `focus-data` volume (`/data/focus.db`).
Set `TZ` in `docker-compose.yml` to your time zone: days and months are computed in local time.
The port is only published on `127.0.0.1`; put a reverse proxy with HTTPS in front of it to expose it.

### Network exposure

The daemon listens on `127.0.0.1` by default (`-host` or `$HOST` to change it). Until a password
is set, the API is open: set one from the web UI before exposing Focus on a network.

## Waybar Integration

Focus exposes a real-time status output designed for status bars like Waybar.

Add this module configuration to your `~/.config/waybar/config.jsonc`:

```
"custom/focus": {
  "format": "{}",
  "return-type": "json",
  "exec": "focus status --watch --json",
  "on-click": "focus toggle",
  "on-click-right": "focus next",
  "on-click-middle": "focus menu",
  "tooltip": true
}
```

## CLI Command Reference

Manage your timer directly from the terminal or keybindings:

|   |   |
|---|---|
|**Command**|**Description**|
|`focus daemon`|Start the HTTP server and web UI daemon|
|`focus status`|Output current timer status text|
|`focus status --watch --json`|Stream real-time JSON state updates (one line per change)|
|`focus login <url>`|Authenticate the CLI against a remote Focus server|
|`focus toggle`|Toggle timer state (start / pause / resume)|
|`focus pause`|Pause active timer session|
|`focus resume`|Resume paused timer session|
|`focus next`|Advance to the next phase (work to break, or break to work)|
|`focus stop` / `focus finish`|Stop and save current session to SQLite|
|`focus cancel`|Cancel current session without recording|
|`focus work`|Quick start Pomodoro work session|
|`focus stopwatch`|Quick start free stopwatch session|
|`focus menu`|Interactive fuzzy launcher menu (Fuzzel / Wofi / Rofi)|
|`focus export`|Export session history (JSON or CSV)|
|`focus import <file>`|Import history from backup file|
