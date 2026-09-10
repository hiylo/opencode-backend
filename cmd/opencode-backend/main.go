package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hiylo/opencode-backend/internal/auth"
	"github.com/hiylo/opencode-backend/internal/config"
	"github.com/hiylo/opencode-backend/internal/opencode"
	"github.com/hiylo/opencode-backend/internal/push"
	"github.com/hiylo/opencode-backend/internal/server"
	"github.com/hiylo/opencode-backend/internal/store"
	"github.com/hiylo/opencode-backend/internal/tasks"
	"github.com/hiylo/opencode-backend/internal/webui"
)

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func main() {
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Build the DSN and open the store.
	var dsn string
	if cfg.DBDriver == "sqlite" {
		dsn = store.SQLiteDSN(cfg.SQLitePath)
	} else {
		dsn = cfg.PostgresDSN
	}
	st, err := store.OpenFromConfig(ctx, cfg.DBDriver, dsn)
	if err != nil {
		log.Fatalf("open %s store: %v", cfg.DBDriver, err)
	}
	defer st.Close()
	log.Printf("storage: %s", cfg.DBDriver)

	// Initialize admin password (first run only) and the auth manager.
	am := auth.NewManager(st)
	created, err := am.Initialize(ctx, cfg.DefaultAdminPassword)
	if err != nil {
		log.Fatalf("initialize auth: %v", err)
	}
	if created {
		log.Printf("initialized admin password (change it via the config UI)")
	}

	oc := opencode.New(cfg.OpenCodeURL)
	hub := push.NewHub()
	go hub.Run()

	srv := server.New(cfg, st, am, oc, hub)
	srv.SetWebUI(webui.New())

	// Async orchestration worker: claims queued tasks and drives the
	// upstream OpenCode server. Runs for the lifetime of the process.
	exec := tasks.NewExecutor(st, hub, cfg.OpenCodeURL)
	go exec.Run(ctx)

	// Orchestration: periodically report upstream health so subscribers get
	// live status without polling from the app.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				healthy := oc.Ping(ctx) == nil
				msg := push.Message{
					Type: "upstream.health",
					Payload: mustJSON(map[string]any{
						"healthy": healthy,
						"time":    time.Now().UTC(),
					}),
				}
				hub.Broadcast(msg)
				if healthy {
					log.Printf("upstream opencode reachable at %s", cfg.OpenCodeURL)
				} else {
					log.Printf("upstream opencode UNREACHABLE at %s", cfg.OpenCodeURL)
				}
			}
		}
	}()

	if err := srv.Start(ctx); err != nil {
		log.Fatalf("server: %v", err)
	}
	log.Println("opencode-backend stopped")
}