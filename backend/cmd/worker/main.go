// Command worker runs background work: the audit chain, event relays and delivery
// (River), the idempotency purge, and the daily audit anchor and verification jobs.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/riverqueue/river"

	"gitlab.com/viyallabs/mugizh/backend/internal/app"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

func main() {
	log := logging.New(os.Stdout, slog.LevelInfo)
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("worker.start_failed", logging.Err(err))
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s, err := app.LoadSettings(os.Getenv)
	if err != nil {
		return err
	}
	core, err := app.BuildCore(ctx, s, log)
	if err != nil {
		return err
	}
	defer core.Close()
	qpool, err := db.Open(ctx, s.QueueURL, db.Options{MaxConns: 10, RequireTLS: s.RequireTLS(), SearchPath: outbox.QueueSchema})
	if err != nil {
		return err
	}
	defer qpool.Close()

	bus := outbox.NewBus(log)
	app.Subscribe(bus, core)
	workers := river.NewWorkers()
	outbox.RegisterDelivery(workers, bus)
	client, err := outbox.NewClient(qpool, workers, map[string]river.QueueConfig{"events": {MaxWorkers: 20}}, log)
	if err != nil {
		return err
	}
	if err := client.Start(ctx); err != nil {
		return err
	}
	defer func() { _ = client.Stop(context.Background()) }()

	for _, m := range app.RelayModules {
		r := &outbox.Relay{Module: m, Pool: core.Pools.For(m), Queue: client, Bus: bus, Log: log}
		go r.Run(ctx, time.Second)
	}
	go core.Audit.Run(ctx, time.Second)
	go every(ctx, log, 5*time.Minute, "verification.process_pending", func(ctx context.Context) error { _, err := core.Verification.ProcessPending(ctx, 100); return err })
	go every(ctx, log, 6*time.Hour, "verification.reverify", func(ctx context.Context) error {
		_, err := core.Verification.RunReverification(ctx, time.Now())
		return err
	})
	go every(ctx, log, time.Minute, "booking.expire_holds", func(ctx context.Context) error { _, err := core.Booking.ExpireHolds(ctx, time.Now()); return err })
	go every(ctx, log, time.Minute, "booking.reminders", func(ctx context.Context) error { _, err := core.Booking.EmitDueReminders(ctx, time.Now()); return err })
	// S-145: finish any clinic closure a crash left half done (each appointment is re-checked).
	go every(ctx, log, time.Minute, "booking.resume_closures", func(ctx context.Context) error { _, err := core.Booking.ResumeClosures(ctx); return err })
	// Consult: doctor no-show after the join timeout (E-BK-04, RF03) and the poll fallback for
	// vendor participant events. An unknown answer never penalises the doctor.
	go every(ctx, log, time.Minute, "consult.doctor_no_show", func(ctx context.Context) error {
		_, err := core.Consult.DetectDoctorNoShow(ctx, time.Now())
		return err
	})
	go every(ctx, log, time.Minute, "consult.sync_sessions", func(ctx context.Context) error { _, err := core.Consult.SyncOpenSessions(ctx, time.Now()); return err })
	go every(ctx, log, 24*time.Hour, "consult.purge_participant_refs", func(ctx context.Context) error {
		_, err := core.Consult.PurgeParticipantRefs(ctx, time.Now())
		return err
	})
	go every(ctx, log, 6*time.Hour, "booking.materialise_slots", func(ctx context.Context) error { _, err := core.Booking.MaterialiseSlots(ctx, time.Now()); return err })
	go every(ctx, log, 5*time.Minute, "booking.reconfirmations", func(ctx context.Context) error {
		_, err := core.Booking.SweepReconfirmations(ctx, time.Now())
		return err
	})
	go every(ctx, log, time.Minute, "records.rights_due", func(ctx context.Context) error { return core.Rights.RunDue(ctx) })
	// AAD-05 and RET-03: hidden documents (quarantined or deleted by the patient) are hard-deleted well within 24 hours.
	go every(ctx, log, 5*time.Minute, "records.purge_hidden_documents", func(ctx context.Context) error { _, err := core.Rights.PurgeHidden(ctx, 100); return err })
	go every(ctx, log, 15*time.Minute, "admin.grievance_due_soon", func(ctx context.Context) error {
		alerts, err := core.Admin.DueSoon(ctx, time.Now())
		if len(alerts) > 0 {
			log.Warn("admin.grievances_due_soon", "count", len(alerts)) // paging hook (S-142)
		}
		return err
	})
	go every(ctx, log, 2*time.Second, "notifications.process_due", func(ctx context.Context) error { _, err := core.Notify.ProcessDue(ctx, time.Now(), 200); return err })
	go every(ctx, log, 24*time.Hour, "notifications.purge_logs", func(ctx context.Context) error { _, err := core.Notify.PurgeDeliveryLogs(ctx, time.Now()); return err })
	go every(ctx, log, 5*time.Second, "payments.webhooks", func(ctx context.Context) error { _, err := core.Payments.ProcessWebhooks(ctx, 100); return err })
	go every(ctx, log, 24*time.Hour, "payments.reconcile", func(ctx context.Context) error {
		links, err := core.Payments.ListLinks(ctx)
		if err != nil {
			return err
		}
		yesterday := time.Now().UTC().Add(-24 * time.Hour)
		for _, l := range links {
			if _, err := core.Payments.Reconcile(ctx, l.LinkRef, yesterday); err != nil {
				log.Error("payments.reconcile_failed", logging.Err(err))
			}
		}
		return nil
	})
	go every(ctx, log, 30*time.Second, "payments.poll", func(ctx context.Context) error { _, err := core.Payments.PollPending(ctx, time.Now()); return err })
	go every(ctx, log, 6*time.Hour, "identity.purge_expired", func(ctx context.Context) error { _, err := core.Identity.PurgeExpired(ctx, time.Now()); return err })
	go every(ctx, log, 6*time.Hour, "verification.purge_evidence", func(ctx context.Context) error {
		_, err := core.Verification.PurgeEvidence(ctx, time.Now())
		return err
	})
	go daily(ctx, log, core)
	log.Info("worker.started", "env", string(s.Env))
	<-ctx.Done()
	return nil
}

// daily anchors yesterday's audit chain, verifies it, and purges expired idempotency keys.
func daily(ctx context.Context, log *slog.Logger, core *app.Core) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		y := time.Now().UTC().Add(-24 * time.Hour)
		if err := core.Audit.Anchor(ctx, y); err != nil {
			log.Error("audit.anchor_failed", logging.Err(err))
		}
		if _, err := core.Audit.Verify(ctx, y); err != nil {
			log.Error("audit.verify_failed", logging.Err(err))
		}
		if _, err := core.Idem.Purge(ctx); err != nil {
			log.Error("idempotency.purge_failed", logging.Err(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// every runs fn now and then at each interval until ctx ends; failures are logged.
func every(ctx context.Context, log *slog.Logger, d time.Duration, job string, fn func(context.Context) error) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		if err := fn(ctx); err != nil {
			log.Error("worker.job_failed", "job", job, logging.Err(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
