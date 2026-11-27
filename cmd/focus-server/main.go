// Command focus-server serves Focus: the timer, its history and the web app.
package main

import (
	"flag"
	"log"

	"github.com/bhr-tools/kit/auth"
	"github.com/bhr-tools/kit/server"

	"github.com/bhr-tools/focus/internal/db"
	fserver "github.com/bhr-tools/focus/internal/server"
	"github.com/bhr-tools/focus/internal/timer"
	"github.com/bhr-tools/focus/web"
)

func main() {
	cfg := server.Flags(8765, "focus.db")
	flag.Parse()

	database, err := db.Open(cfg.DBPath())
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer database.Close()
	a, err := auth.New(database.SQL(), fserver.SessionCookie, cfg.NoAuth)
	if err != nil {
		log.Fatalf("auth: %v", err)
	}
	engine := timer.NewEngine(database)
	defer engine.Close()

	api := fserver.NewServer(database, engine, web.Files, a)
	h := server.Handler(server.Options{
		API:  api.Handler(),
		Web:  web.Files,
		Ping: func() error { return database.SQL().Ping() },
	})
	server.Run("Focus", cfg.Addr(), h)
}
