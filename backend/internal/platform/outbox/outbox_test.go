package outbox_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

func TestEmitRelayDeliverOnce(t *testing.T) {
	ctx := context.Background()
	env := testkit.New(t, testkit.Spec{Key: "outbox", Sources: []migrate.Source{dbschema.Source()},
		ExtraLogins: map[string]string{"queue": "mugizh_queue"}})
	if _, err := env.Owner.Exec(ctx, `SET search_path TO records; `+outbox.TableDDL+`; SET search_path TO booking; `+outbox.TableDDL); err != nil {
		t.Fatal(err)
	}
	if err := outbox.MigrateQueue(ctx, env.Owner); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := outbox.NewBus(log)

	var mu sync.Mutex
	calls := 0
	done := make(chan struct{}, 10)
	bus.Subscribe("booking.on_consent_revoked", "consent.revoked", func(ctx context.Context, e outbox.Event) error {
		// Deliver the same event twice to prove Once makes it idempotent.
		for i := 0; i < 2; i++ {
			if err := outbox.Once(ctx, env.Pools.For("booking"), "booking.on_consent_revoked", e, func(tx pgx.Tx) error {
				mu.Lock()
				calls++
				mu.Unlock()
				return nil
			}); err != nil {
				return err
			}
		}
		done <- struct{}{}
		return nil
	})

	workers := river.NewWorkers()
	outbox.RegisterDelivery(workers, bus)
	client, err := outbox.NewClient(env.Pools.For("queue"), workers, map[string]river.QueueConfig{"events": {MaxWorkers: 2}}, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Stop(context.Background()) }()

	grant := ids.New()
	if err := db.InTx(ctx, env.Pools.For("records"), pgx.TxOptions{}, func(tx pgx.Tx) error {
		return outbox.Emit(ctx, tx, outbox.Event{Type: "consent.revoked", SubjectRef: ids.New(), Payload: map[string]string{"grantId": grant}})
	}); err != nil {
		t.Fatal(err)
	}
	relay := &outbox.Relay{Module: "records", Pool: env.Pools.For("records"), Queue: client, Bus: bus, Log: log}
	if n, err := relay.Once(ctx, 10); err != nil || n != 1 {
		t.Fatalf("relay n=%d err=%v", n, err)
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("event not delivered")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("handler ran %d times, want exactly 1", calls)
	}
	if n, _ := relay.Once(ctx, 10); n != 0 {
		t.Fatal("outbox must be empty after relay")
	}
}

func TestEmitRejectsContent(t *testing.T) {
	ctx := context.Background()
	env := testkit.New(t, testkit.Spec{Key: "outbox", Sources: []migrate.Source{dbschema.Source()},
		ExtraLogins: map[string]string{"queue": "mugizh_queue"}})
	_, _ = env.Owner.Exec(ctx, `SET search_path TO records; `+outbox.TableDDL)
	for _, e := range []outbox.Event{
		{Type: "Consent Revoked"},
		{Type: "prescription.signed", Payload: map[string]string{"medicine": "metformin 500 mg"}},
		{Type: "appointment.confirmed", SubjectRef: "Ravi Kumar"},
	} {
		err := db.InTx(ctx, env.Pools.For("records"), pgx.TxOptions{}, func(tx pgx.Tx) error { return outbox.Emit(ctx, tx, e) })
		if !errors.Is(err, outbox.ErrInvalid) {
			t.Errorf("%+v: want ErrInvalid, got %v", e, err)
		}
	}
}
