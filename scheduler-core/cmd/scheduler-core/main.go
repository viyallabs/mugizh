// Command scheduler-core is the standalone scheduler service (B10, K-ARC-02): "serve" runs the API, the
// generator and the dispatcher; "migrate" applies the database migrations with the migration owner's DSN.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // embed the IANA database so zones resolve in minimal images

	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/api"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/config"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/dispatch"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/migrations"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/scheduler-core/internal/sched"
)

func main() {
	log := logging.New(os.Stderr, slog.LevelInfo)
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(log)
	case "migrate":
		err = migrate()
	default:
		err = fmt.Errorf("unknown command %q (use serve or migrate)", cmd)
	}
	if err != nil {
		log.Error("scheduler.exit", "reason", err.Error())
		os.Exit(1)
	}
}

func migrate() error {
	cfg, err := config.Load(nil, false)
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, cfg.MigrateURL, db.Options{MaxConns: 2, RequireTLS: cfg.RequireDBTLS})
	if err != nil {
		return err
	}
	defer pool.Close()
	return migrations.Up(ctx, pool)
}

func serve(log *slog.Logger) error {
	cfg, err := config.Load(nil, true)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := db.Open(ctx, cfg.DatabaseURL, db.Options{MaxConns: cfg.DBMaxConns, RequireTLS: cfg.RequireDBTLS, SearchPath: db.Schema})
	if err != nil {
		return err
	}
	defer pool.Close()
	svc := sched.NewService(pool)
	sender := dispatch.NewSender(cfg.CallbackURL, cfg.CallbackSecret, cfg.CallbackTimeout)
	rt, err := dispatch.New(pool, svc, sender, cfg.Dispatch, log)
	if err != nil {
		return err
	}
	if err := rt.Start(ctx); err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           (&api.Server{Svc: svc, Log: log, TokenHashes: cfg.TokenHashes, Ready: pool.Ping}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("scheduler.started", "addr", cfg.ListenAddr)
	select {
	case err = <-errc:
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	if serr := rt.Stop(shutdown); serr != nil && err == nil {
		err = serr
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	log.Info("scheduler.stopped")
	return err
}
