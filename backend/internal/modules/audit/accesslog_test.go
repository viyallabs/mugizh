package audit_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// S-170: the access log lists allowed reads by people only, newest first, and pages.
func TestAccessLogShowsAllowedReadsByPeopleOnly(t *testing.T) {
	env, mod, _ := setup(t)
	ctx := context.Background()
	patient, doc, other := ids.New(), ids.New(), ids.New()
	ev := func(actor, role, action string, res ap.Result, subject string) ap.Event {
		return ap.Event{ActorRef: actor, ActorRole: role, Action: action, SubjectRef: subject, ResourceType: "note",
			Purpose: "T3_history_share", Result: res, RequestID: ids.New()}
	}
	events := []ap.Event{
		ev(doc, "doctor", "records.read.clinical_history", ap.Allowed, patient),
		ev(doc, "doctor", "records.read.documents", ap.Denied, patient),       // denial: hidden
		ev(doc, "system", "records.read.booking", ap.Allowed, patient),        // system read: hidden
		ev(doc, "doctor", "consent.grant", ap.Allowed, patient),               // not a read
		ev(doc, "doctor", "records.read.clinical_history", ap.Allowed, other), // another patient
		ev(ids.New(), "support", "records.read.booking", ap.Allowed, patient),
		ev(doc, "doctor", "records.read.documents", ap.Allowed, patient),
	}
	err := db.InTx(ctx, env.Pools.For("records"), pgx.TxOptions{}, func(tx pgx.Tx) error {
		for _, e := range events {
			if err := ap.Record(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mod.ChainBatch(ctx, 100); err != nil {
		t.Fatal(err)
	}
	got, next, err := mod.AccessLogFor(ctx, patient, "", 10)
	if err != nil || next != "" {
		t.Fatalf("%v next=%q", err, next)
	}
	if len(got) != 3 {
		t.Fatalf("want the 3 allowed reads by people, got %d: %+v", len(got), got)
	}
	for _, e := range got {
		if e.ActorRole != "doctor" && e.ActorRole != "support" {
			t.Fatalf("unexpected role %q", e.ActorRole)
		}
	}
	p1, c1, err := mod.AccessLogFor(ctx, patient, "", 2)
	if err != nil || len(p1) != 2 || c1 == "" {
		t.Fatalf("page 1: %v %d %q", err, len(p1), c1)
	}
	p2, c2, err := mod.AccessLogFor(ctx, patient, c1, 2)
	if err != nil || len(p2) != 1 || c2 != "" || p2[0].EventID == p1[0].EventID || p2[0].EventID == p1[1].EventID {
		t.Fatalf("page 2: %v %+v %q", err, p2, c2)
	}
	if _, _, err := mod.AccessLogFor(ctx, patient, "garbage", 2); err == nil {
		t.Fatal("a malformed cursor must be refused")
	}
}
