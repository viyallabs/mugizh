package consult_test

import (
	"context"
	"testing"
	"time"
)

// Review F11 (RET-12): vendor-facing participant references are replaced a day after the
// consult ends; open consults and join evidence are untouched.
func TestReviewF11ParticipantRefsExpire(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	done := f.confirm(t, "video")
	f.bothConnected(t, done)
	if _, err := f.m.SetOutcome(ctx, done.patientActor(), done.session, "technical_failure"); err != nil {
		t.Fatal(err)
	}
	open := f.confirm(t, "video")
	f.bothConnected(t, open)
	before := f.participantRef(t, done, "patient")
	openRef := f.participantRef(t, open, "patient")
	var events int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM consult.participant_event WHERE session_id=$1`, done.session).Scan(&events)

	if n, err := f.m.PurgeParticipantRefs(ctx, f.clock().Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("too early: %d %v", n, err)
	}
	if n, err := f.m.PurgeParticipantRefs(ctx, f.clock().Add(25*time.Hour)); err != nil || n != 2 {
		t.Fatalf("purge: %d %v", n, err)
	}
	if after := f.participantRef(t, done, "patient"); after == before {
		t.Fatal("the reference must be replaced")
	}
	if f.participantRef(t, open, "patient") != openRef {
		t.Fatal("an open consult keeps its references")
	}
	var after int
	_ = f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM consult.participant_event WHERE session_id=$1`, done.session).Scan(&after)
	if after != events || events == 0 {
		t.Fatalf("join evidence must stay: %d -> %d", events, after)
	}
	if n, _ := f.m.PurgeParticipantRefs(ctx, f.clock().Add(48*time.Hour)); n != 0 {
		t.Fatal("purged once only")
	}
}
