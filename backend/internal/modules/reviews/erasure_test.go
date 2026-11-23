package reviews_test

import (
	"context"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/reviews"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// K-DP-07, D-32: step 1 deletes the subject's reviews (fixing the aggregate) and visit rows and reports
// "deleted" once even when the event arrives twice; step 2 sweeps again; cancel is a no-op.
func TestKDP07_ErasureDeletesReviewsAndFixesTheAggregate(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	doctor := ids.New()
	mine := f.completed(t, doctor, f.clock().Add(-time.Hour))
	theirs := f.completed(t, doctor, f.clock().Add(-time.Hour))
	if _, err := f.m.Create(ctx, mine.actor(), mine.appt, reviews.CreateInput{Rating: 5, Text: strp("Helpful")}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Create(ctx, theirs.actor(), theirs.appt, reviews.CreateInput{Rating: 3}); err != nil {
		t.Fatal(err)
	}
	// A second visit of mine, not reviewed: its visit row goes too.
	again := visit{appt: ids.New(), patient: mine.patient, doctor: ids.New()}
	f.register(again, "completed")
	if err := f.m.OnAppointmentCompleted(ctx, completedEvent(again, f.clock())); err != nil {
		t.Fatal(err)
	}
	if n, sum := f.aggregate(t, doctor); n != 2 || sum != 8 {
		t.Fatalf("before: %d %d", n, sum)
	}

	req := ids.New()
	ev := func(typ string) outbox.Event {
		return outbox.Event{ID: ids.New(), Type: typ, SubjectRef: mine.patient, Payload: map[string]string{"requestRef": req, "subjectRef": mine.patient}}
	}
	step1 := ev("rights.erasure_requested")
	for i := 0; i < 2; i++ {
		if err := f.m.OnErasureRequested(ctx, step1); err != nil {
			t.Fatal(err)
		}
	}
	if f.count(t, `SELECT count(*) FROM reviews.review WHERE patient_ref=$1`, mine.patient) != 0 ||
		f.count(t, `SELECT count(*) FROM reviews.visit WHERE patient_ref=$1`, mine.patient) != 0 {
		t.Fatal("the subject's reviews and visits are deleted")
	}
	if f.count(t, `SELECT count(*) FROM reviews.review WHERE patient_ref=$1`, theirs.patient) != 1 {
		t.Fatal("another patient's review stays")
	}
	if n, sum := f.aggregate(t, doctor); n != 1 || sum != 3 {
		t.Fatalf("aggregate after erasure: %d %d", n, sum)
	}
	if err := f.m.OnErasureCancelled(ctx, ev("rights.erasure_cancelled")); err != nil {
		t.Fatal(err)
	}
	// Something written for the subject since step 1 (a late completion event) is swept by step 2.
	late := visit{appt: ids.New(), patient: mine.patient, doctor: doctor}
	if err := f.m.OnAppointmentCompleted(ctx, completedEvent(late, f.clock())); err != nil {
		t.Fatal(err)
	}
	if err := f.m.OnErasureDue(ctx, ev("rights.erasure_due")); err != nil {
		t.Fatal(err)
	}
	if err := f.m.OnErasureDue(ctx, ev("rights.erasure_due")); err != nil { // nothing left: "none"
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM reviews.visit WHERE patient_ref=$1`, mine.patient) != 0 {
		t.Fatal("step 2 sweeps")
	}
	outcomes := map[string]int{}
	for _, g := range f.events(t, "rights.task_done") {
		if g["requestRef"] != req {
			continue
		}
		if g["module"] != "reviews" {
			t.Fatalf("module: %v", g)
		}
		outcomes[g["outcome"]]++
	}
	if outcomes["deleted"] != 1 || outcomes["erased"] != 1 || outcomes["none"] != 1 || len(outcomes) != 3 {
		t.Fatalf("task reports: %v", outcomes)
	}
	if err := f.m.OnErasureRequested(ctx, outbox.Event{ID: ids.New(), Payload: map[string]string{"requestRef": "x"}}); err == nil {
		t.Fatal("malformed event must be refused")
	}
}
