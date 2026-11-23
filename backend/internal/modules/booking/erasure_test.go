package booking_test

import (
	"context"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/booking"
	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

func erasureEvent(typ, req, subject string) outbox.Event {
	return outbox.Event{ID: ids.New(), Type: typ, SubjectRef: subject, Payload: map[string]string{"requestRef": req, "subjectRef": subject}}
}

func (f *fx) taskDone(t *testing.T, req string) []map[string]string {
	t.Helper()
	var out []map[string]string
	for _, m := range f.events(t, "rights.task_done") {
		if m["requestRef"] == req {
			out = append(out, m)
		}
	}
	return out
}

func TestKDP07_BookingErasureCancelsFutureKeepsPastRestricted(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	p := f.practice(t)
	slots := f.openSlots(t, p, booking.ModeInPerson)
	patient, other := ids.New(), ids.New()
	past := f.paid(t, p, patient, slots[2])
	future := f.paid(t, p, patient, slots[10])
	held, err := f.holdIn(p, patient, slots[12])
	if err != nil {
		t.Fatal(err)
	}
	otherAppt := f.paid(t, p, other, slots[14])
	f.setClock(past.StartsAt.Add(time.Hour)) // the first one is now in the past

	req := ids.New()
	ev := erasureEvent("rights.erasure_requested", req, patient)
	for i := 0; i < 2; i++ { // replay-safe
		if err := f.m.OnErasureRequested(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	state := func(id string) string {
		var s string
		if err := f.env.Owner.QueryRow(ctx, `SELECT state FROM booking.appointment WHERE id=$1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if state(future.ID) != "cancelled" || state(held.ID) != "cancelled" {
		t.Fatalf("future %s held %s", state(future.ID), state(held.ID))
	}
	if state(past.ID) != "confirmed" || state(otherAppt.ID) != "confirmed" {
		t.Fatal("past (kept) and other patients' appointments must be untouched")
	}
	if f.count(t, `SELECT count(*) FROM booking.appointment WHERE patient_ref=$1`, patient) != 3 {
		t.Fatal("appointment rows are kept (RET-04)")
	}
	if f.count(t, `SELECT count(*) FROM booking.appointment WHERE id=$1 AND cancelled_by='system' AND cancel_rule='RF04'`, future.ID) != 1 {
		t.Fatal("paid appointment: byWhom system with the full-refund rule")
	}
	cancelled := f.events(t, bp.EventCancelled)
	if len(cancelled) != 1 || cancelled[0]["byWhom"] != "system" || cancelled[0]["rule"] != "RF04" || cancelled[0]["appointmentRef"] != future.ID {
		t.Fatalf("appointment.cancelled for payments: %v", cancelled)
	}
	got := f.taskDone(t, req)
	if len(got) != 1 || got[0]["module"] != "booking" || got[0]["outcome"] != "restricted" || got[0]["retentionRule"] != "RET-04" || len(got[0]) != 4 {
		t.Fatalf("step 1 payload %v", got)
	}

	// Cancel: appointments are not restored, nothing reported.
	if err := f.m.OnErasureCancelled(ctx, erasureEvent("rights.erasure_cancelled", req, patient)); err != nil {
		t.Fatal(err)
	}
	if len(f.taskDone(t, req)) != 1 || state(future.ID) != "cancelled" {
		t.Fatal("cancel must change nothing")
	}

	// Step 2: reconfirmation tasks go, appointment rows stay; reported once.
	due := erasureEvent("rights.erasure_due", req, patient)
	for i := 0; i < 2; i++ {
		if err := f.m.OnErasureDue(ctx, due); err != nil {
			t.Fatal(err)
		}
	}
	if f.count(t, `SELECT count(*) FROM booking.reconfirmation_task t JOIN booking.appointment a ON a.id=t.appointment_id WHERE a.patient_ref=$1`, patient) != 0 {
		t.Fatal("reconfirmation tasks deleted")
	}
	if f.count(t, `SELECT count(*) FROM booking.appointment WHERE patient_ref=$1`, patient) != 3 {
		t.Fatal("appointment rows stay")
	}
	got = f.taskDone(t, req)
	if len(got) != 2 || got[1]["module"] != "booking" || got[1]["outcome"] != "erased" {
		t.Fatalf("step 2 payload %v", got)
	}
}

func TestKDP07_BookingErasureWithoutAppointmentsReportsNone(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	req, subject := ids.New(), ids.New()
	if err := f.m.OnErasureRequested(ctx, erasureEvent("rights.erasure_requested", req, subject)); err != nil {
		t.Fatal(err)
	}
	if err := f.m.OnErasureDue(ctx, erasureEvent("rights.erasure_due", req, subject)); err != nil {
		t.Fatal(err)
	}
	got := f.taskDone(t, req)
	if len(got) != 2 || got[0]["outcome"] != "none" || got[1]["outcome"] != "none" {
		t.Fatalf("%v", got)
	}
	if err := f.m.OnErasureRequested(ctx, outbox.Event{ID: ids.New(), Type: "rights.erasure_requested"}); err == nil {
		t.Fatal("event without refs must fail")
	}
}
