package audit_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

func setup(t *testing.T) (*testkit.Env, *audit.Module, *[]string) {
	t.Helper()
	env := testkit.New(t, testkit.Spec{Sources: []migrate.Source{dbschema.Source(), audit.Source()}})
	mod := audit.New(env.Pools.For("audit"), blobstore.LocalDir{Root: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var alerts []string
	mod.Alert = func(_ context.Context, code string) { alerts = append(alerts, code) }
	return env, mod, &alerts
}

func record(t *testing.T, env *testkit.Env, module string, n int) {
	t.Helper()
	ctx := context.Background()
	err := db.InTx(ctx, env.Pools.For(module), pgx.TxOptions{}, func(tx pgx.Tx) error {
		for i := 0; i < n; i++ {
			if err := ap.Record(ctx, tx, ap.Event{ActorRef: ids.New(), ActorRole: "doctor", Action: "records.read",
				SubjectRef: ids.New(), ResourceType: "prescription", ResourceRef: ids.New(), Purpose: "T1_current_care",
				GrantID: ids.New(), Result: ap.Allowed, RequestID: ids.New()}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TC-65 (E-SEC-12): module roles cannot read, change or delete audit rows; altering one
// chained row is detected by the verify job and raises an alert.
func TestTC65TamperEvidence(t *testing.T) {
	env, mod, alerts := setup(t)
	ctx := context.Background()
	record(t, env, "identity", 3)
	record(t, env, "records", 2)

	// Module roles: INSERT only on the outbox.
	for _, sql := range []string{
		`SELECT count(*) FROM audit.audit_outbox`,
		`UPDATE audit.audit_outbox SET action='x.y'`,
		`DELETE FROM audit.audit_outbox`,
		`SELECT count(*) FROM audit.audit_event`,
	} {
		_, err := env.Pools.For("identity").Exec(ctx, sql)
		if !db.IsInsufficientPrivilege(err) {
			t.Errorf("identity role ran %q: err=%v", sql, err)
		}
	}

	n, err := mod.ChainBatch(ctx, 100)
	if err != nil || n != 5 {
		t.Fatalf("chain: n=%d err=%v", n, err)
	}
	// Even the audit role cannot change or delete chained events.
	for _, sql := range []string{`UPDATE audit.audit_event SET action='x.y'`, `DELETE FROM audit.audit_event`, `TRUNCATE audit.audit_event`} {
		_, err := env.Pools.For("audit").Exec(ctx, sql)
		if !db.IsInsufficientPrivilege(err) {
			t.Errorf("audit role ran %q: err=%v", sql, err)
		}
	}

	today := time.Now().UTC()
	breaks, err := mod.Verify(ctx, today)
	if err != nil || len(breaks) != 0 {
		t.Fatalf("clean chain must verify: %v %v", breaks, err)
	}

	// An attacker with database superuser rights edits one row.
	if _, err := env.Owner.Exec(ctx, `UPDATE audit.audit_event SET resource_ref='forged' WHERE seq=3`); err != nil {
		t.Fatal(err)
	}
	breaks, err = mod.Verify(ctx, today)
	if err != nil {
		t.Fatal(err)
	}
	if len(breaks) == 0 || breaks[0].Seq != 3 {
		t.Fatalf("tampering not detected: %+v", breaks)
	}
	if len(*alerts) == 0 || (*alerts)[0] != "audit.chain_broken" {
		t.Fatalf("no alert raised: %v", *alerts)
	}
}

func TestDeletedRowAndAnchorMismatchDetected(t *testing.T) {
	env, mod, _ := setup(t)
	ctx := context.Background()
	day := time.Now().UTC()
	record(t, env, "booking", 4)
	if _, err := mod.ChainBatch(ctx, 100); err != nil {
		t.Fatal(err)
	}
	// Anchor the day from "tomorrow".
	mod.SetClock(func() time.Time { return day.Add(24 * time.Hour) })
	if err := mod.Anchor(ctx, day); err != nil {
		t.Fatal(err)
	}
	if b, _ := mod.Verify(ctx, day); len(b) != 0 {
		t.Fatalf("anchored chain must verify: %+v", b)
	}
	// Remove the last event: the chain itself still links, but the anchors no longer match.
	if _, err := env.Owner.Exec(ctx, `DELETE FROM audit.audit_event WHERE seq=4`); err != nil {
		t.Fatal(err)
	}
	b, _ := mod.Verify(ctx, day)
	if len(b) == 0 {
		t.Fatal("deleting the last event must break the anchor check")
	}
}

func TestChainLinksAcrossBatchesAndDays(t *testing.T) {
	env, mod, _ := setup(t)
	ctx := context.Background()
	d1 := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	mod.SetClock(func() time.Time { return d1 })
	record(t, env, "identity", 2)
	if _, err := mod.ChainBatch(ctx, 1); err != nil { // two batches on day 1
		t.Fatal(err)
	}
	if _, err := mod.ChainBatch(ctx, 1); err != nil {
		t.Fatal(err)
	}
	d2 := d1.Add(24 * time.Hour)
	mod.SetClock(func() time.Time { return d2 })
	record(t, env, "identity", 1)
	if _, err := mod.ChainBatch(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if err := mod.Anchor(ctx, d1); err != nil { // a finished day must be anchored (review L11)
		t.Fatal(err)
	}
	for _, d := range []time.Time{d1, d2} {
		if b, err := mod.Verify(ctx, d); err != nil || len(b) != 0 {
			t.Fatalf("day %s: %+v %v", d.Format("2006-01-02"), b, err)
		}
	}
	// Tampering with day 1's last hash breaks day 2's link.
	if _, err := env.Owner.Exec(ctx, `UPDATE audit.audit_event SET hash=decode(repeat('00',32),'hex') WHERE partition_day='2026-11-16' AND seq=2`); err != nil {
		t.Fatal(err)
	}
	if b, _ := mod.Verify(ctx, d2); len(b) == 0 {
		t.Fatal("day 2 must detect the broken link to day 1")
	}
}

func TestRecordRejectsContentAndRollsBackCaller(t *testing.T) {
	env, _, _ := setup(t)
	ctx := context.Background()
	bad := []ap.Event{
		{ActorRef: "Lakshmi N", Action: "records.read", Result: ap.Allowed},  // a name, not a reference
		{ActorRef: ids.New(), Action: "Read the record", Result: ap.Allowed}, // free text action
		{ActorRef: ids.New(), Action: "records.read", ResourceType: "type 2 diabetes", Result: ap.Allowed},
		{ActorRef: ids.New(), Action: "records.read", Result: "ok"},
	}
	for i, e := range bad {
		err := db.InTx(ctx, env.Pools.For("records"), pgx.TxOptions{}, func(tx pgx.Tx) error { return ap.Record(ctx, tx, e) })
		if !errors.Is(err, ap.ErrInvalid) {
			t.Errorf("case %d: want ErrInvalid, got %v", i, err)
		}
	}
	// A failed audit insert fails the caller's whole transaction (fail closed).
	_, _ = env.Owner.Exec(ctx, `REVOKE INSERT ON audit.audit_outbox FROM mugizh_mod_records`)
	err := db.InTx(ctx, env.Pools.For("records"), pgx.TxOptions{}, func(tx pgx.Tx) error {
		return ap.Record(ctx, tx, ap.Event{ActorRef: ids.New(), Action: "records.read", Result: ap.Allowed})
	})
	if err == nil {
		t.Fatal("audit insert failure must fail the caller")
	}
	_, _ = env.Owner.Exec(ctx, `GRANT INSERT ON audit.audit_outbox TO mugizh_mod_records`)
}

// Review L11: a finished day with events but no anchor is reported (possible truncation).
func TestMissingAnchorIsABreak(t *testing.T) {
	env, mod, alerts := setup(t)
	ctx := context.Background()
	d1 := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	mod.SetClock(func() time.Time { return d1 })
	record(t, env, "identity", 2)
	if _, err := mod.ChainBatch(ctx, 10); err != nil {
		t.Fatal(err)
	}
	mod.SetClock(func() time.Time { return d1.Add(24 * time.Hour) })
	b, err := mod.Verify(ctx, d1)
	if err != nil || len(b) != 1 || len(*alerts) == 0 {
		t.Fatalf("missing anchor must be a break with an alert: %+v %v %v", b, err, *alerts)
	}
}
