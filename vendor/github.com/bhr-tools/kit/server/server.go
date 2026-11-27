// Package server starts an app: flags (that fall back on environment
// variables, so the NixOS module and a shell configure it alike), the
// routes every app shares (/healthz, /kit/, /sw.js, the web app) wrapped in
// security headers, and a clean shutdown.
//
//	cfg := server.Flags(8080, "getdone.db")
//	window := flag.Duration(…)              // the app's own flags
//	flag.Parse()
//	store := open(cfg.DBPath())
//	h := server.Handler(server.Options{API: api.Handler(), Web: web.Files, Ping: store.Ping, Offline: true})
//	server.Run("Get Done", cfg.Addr(), h)
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	kitweb "github.com/bhr-tools/kit/web"
)

// DefaultCSP allows only the app's own files. Apps that frame or proxy
// content (mail bodies, PDF previews) pass their own.
const DefaultCSP = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; manifest-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// Config holds the flags every app has.
type Config struct {
	Host    string
	Port    int
	DataDir string
	DB      string // database file; empty: DataDir/dbFile
	NoAuth  bool
	dbFile  string
}

// Flags registers -host, -port, -data-dir, -db and -no-auth on the default
// flag set (env HOST, PORT, DATA_DIR, DB_PATH, NO_AUTH). Call flag.Parse()
// after adding the app's own flags.
func Flags(defaultPort int, dbFile string) *Config {
	c := &Config{dbFile: dbFile}
	flag.StringVar(&c.Host, "host", EnvOr("HOST", "127.0.0.1"), "listen address (env HOST)")
	flag.IntVar(&c.Port, "port", EnvInt("PORT", defaultPort), "listen port (env PORT)")
	flag.StringVar(&c.DataDir, "data-dir", EnvOr("DATA_DIR", "./data"), "directory of the database and files (env DATA_DIR)")
	flag.StringVar(&c.DB, "db", os.Getenv("DB_PATH"), "database file, overrides -data-dir (env DB_PATH)")
	flag.BoolVar(&c.NoAuth, "no-auth", EnvBool("NO_AUTH"), "disable the master password (env NO_AUTH=true)")
	return c
}

// DBPath is -db, or the database file in -data-dir.
func (c *Config) DBPath() string {
	if c.DB != "" {
		return c.DB
	}
	return filepath.Join(c.DataDir, c.dbFile)
}

// Addr is host:port; it stops the program on an invalid port.
func (c *Config) Addr() string {
	if c.Port < 1 || c.Port > 65535 {
		log.Fatalf("invalid port %d", c.Port)
	}
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// Options of Handler.
type Options struct {
	API     http.Handler // mounted on /api/
	Web     fs.FS        // the app's files, index.html at the root
	Ping    func() error // /healthz answers 503 when it fails (nil: always ok)
	Offline bool         // serve /sw.js, which keeps the app and kit files offline
	CSP     string       // default DefaultCSP
	Extra   func(mux *http.ServeMux)
}

// Handler routes /api/, /healthz, /kit/, /sw.js and the web app.
func Handler(o Options) http.Handler {
	mux := http.NewServeMux()
	if o.API != nil {
		mux.Handle("/api/", o.API)
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if o.Ping != nil {
			if err := o.Ping(); err != nil {
				http.Error(w, "database unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		fmt.Fprintln(w, "ok")
	})
	kitFiles, err := fs.Sub(kitweb.Files, ".")
	if err != nil {
		log.Fatalf("kit files: %v", err)
	}
	mux.Handle("/kit/", NoCache(http.StripPrefix("/kit", http.FileServerFS(kitFiles))))
	if o.Offline {
		mux.Handle("GET /sw.js", serviceWorker(o.Web, kitFiles))
	}
	if o.Extra != nil {
		o.Extra(mux)
	}
	if o.Web != nil {
		mux.Handle("/", NoCache(http.FileServerFS(o.Web)))
	}
	csp := o.CSP
	if csp == "" {
		csp = DefaultCSP
	}
	return SecurityHeaders(csp, mux)
}

// serviceWorker serves the kit's sw.js prefixed with the files to keep for
// offline use (the app's and the kit's, under /kit/) and a version that
// changes with any of them.
func serviceWorker(app, kit fs.FS) http.Handler {
	files := []string{}
	sum := sha256.New()
	add := func(fsys fs.FS, prefix string) {
		err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || p == "sw.js" || strings.HasSuffix(p, ".go") {
				return err
			}
			b, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			sum.Write([]byte(prefix + p))
			sum.Write(b)
			if prefix == "/" && p == "index.html" {
				files = append(files, "/") // the file server answers "/" with index.html
			} else {
				files = append(files, prefix+p)
			}
			return nil
		})
		if err != nil {
			log.Fatalf("service worker: %v", err)
		}
	}
	if app != nil {
		add(app, "/")
	}
	add(kit, "/kit/")
	src, err := fs.ReadFile(kit, "sw.js")
	if err != nil {
		log.Fatalf("service worker: %v", err)
	}
	sum.Write(src)
	list, _ := json.Marshal(files)
	body := fmt.Sprintf("const VERSION = %q;\nconst FILES = %s;\n%s", hex.EncodeToString(sum.Sum(nil))[:16], list, src)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		io.WriteString(w, body)
	})
}

// NoCache makes browsers revalidate every load, so an upgrade never mixes
// old and new JavaScript modules.
func NoCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// SecurityHeaders sets the headers of every answer; a handler may still set
// its own Content-Security-Policy (sandboxed attachments).
func SecurityHeaders(csp string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", csp)
		next.ServeHTTP(w, r)
	})
}

// Timeout bounds reading a request and writing its answer. An app that
// receives large uploads or holds long polls raises it before Run.
var Timeout = 60 * time.Second

// Run serves h on addr until SIGINT or SIGTERM, then shuts down cleanly.
func Run(name, addr string, h http.Handler) {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       Timeout,
		WriteTimeout:      Timeout,
		IdleTimeout:       2 * Timeout,
	}
	go func() {
		log.Printf("%s listening on http://%s", name, addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// EnvOr is the trimmed environment variable key, or fallback when unset.
func EnvOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// EnvInt parses an integer variable; an invalid value stops the program.
func EnvInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatalf("invalid %s=%q: not a number", key, v)
	}
	return n
}

// EnvBool parses a boolean variable (unset: false); an invalid value stops
// the program.
func EnvBool(key string) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Fatalf("invalid %s=%q: expected true or false", key, v)
	}
	return b
}

// EnvDuration parses a duration variable (1h, 30m…); an invalid value stops
// the program.
func EnvDuration(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatalf("invalid %s=%q: expected a duration such as 24h", key, v)
	}
	return d
}
