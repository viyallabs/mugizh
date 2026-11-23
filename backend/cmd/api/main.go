// Command api serves the Core API (S-090). Configuration and secrets come from the
// environment; start-up fails closed on any configuration problem.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/app"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
)

func main() {
	log := logging.New(os.Stdout, slog.LevelInfo)
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("api.start_failed", logging.Err(err))
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app.UseStrictJSON()
	s, err := app.LoadSettings(os.Getenv)
	if err != nil {
		return err
	}
	core, err := app.BuildCore(ctx, s, log)
	if err != nil {
		return err
	}
	defer core.Close()
	pol, err := app.Specs()
	if err != nil {
		return err
	}
	go core.Records.Listen(ctx) // consent cache invalidation across instances
	h := app.NewRouter(app.RouterConfig{Log: log, Crypto: core.Crypto, Auth: core.Identity, Idempotency: core.Idem,
		TrustedProxies: s.TrustedProxies, Ready: core.Ready,
		Mount: app.Mounts(app.Modules{Identity: core.Identity, Records: core.Records, Verification: core.Verification,
			Directory: core.Directory, Booking: core.Booking, Payments: core.Payments, Notify: core.Notify, Rights: core.Rights,
			Admin: core.Admin, Rx: core.Rx, Consult: core.Consult, Sharing: core.Sharing, History: core.History}, pol, log, time.Now),
		Raw: []func(chi.Router){func(r chi.Router) {
			// Signed callbacks that authenticate themselves (vendor webhook, scheduler-core).
			r.Post("/webhooks/payments/{clinicLinkRef}", core.Payments.WebhookHandler().ServeHTTP)
			r.Post("/internal/v1/scheduler/occurrence-due", core.Notify.OccurrenceDueHandler(s.SchedulerSecret, 5*time.Minute).ServeHTTP)
		}}})
	srv := &http.Server{Addr: s.HTTPAddr, Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("api.listening", "addr", s.HTTPAddr, "env", string(s.Env))
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shut, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return srv.Shutdown(shut)
}
