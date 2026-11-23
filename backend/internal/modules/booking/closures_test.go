package booking_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/booking"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// closureDay is the local date used in these tests: the day after t0 (a Wednesday), in the
// practice zone. Its in-person slots are 09:00 to 11:30 local, every 30 minutes.
const (
	closureDay  = "2026-11-18"
	nextDay     = "2026-11-19"
	previousDay = "2026-11-17"
)

// practiceIn registers a listed practice at a branch of a clinic, in a given zone.
func (f *fx) practiceIn(t testing.TB, clinic, branch, zone string) dp.Practice {
	t.Helper()
	p := dp.Practice{ID: ids.New(), ProfessionalRef: ids.New(), ClinicRef: clinic, BranchRef: branch, Modes: []string{booking.ModeInPerson, booking.ModeVideo},
		FeeAmount: fee, Currency: "TST", FeeVersion: "1", TimeZone: zone, Listed: true}
	f.dir.set(p)
	if _, err := f.m.SetWeeklyTemplate(context.Background(), docActor(p), p.ID, weekly()); err != nil {
		t.Fatal(err)
	}
	return p
}

// slotOn returns the n-th (0 based) open in-person slot of a practice on a local date.
func (f *fx) slotOn(t testing.TB, p dp.Practice, date string, n int) booking.SlotInfo {
	t.Helper()
	got := f.slotsOn(t, p, date)
	if n >= len(got) {
		t.Fatalf("practice %s has %d open in-person slots on %s, want index %d", p.ID, len(got), date, n)
	}
	return got[n]
}

func (f *fx) slotsOn(t testing.TB, p dp.Practice, date string) []booking.SlotInfo {
	t.Helper()
	loc, err := time.LoadLocation(p.TimeZone)
	if err != nil {
		t.Fatal(err)
	}
	var out []booking.SlotInfo
	for _, s := range f.openSlots(t, p, booking.ModeInPerson) {
		if s.StartsAt.In(loc).Format("2006-01-02") == date {
			out = append(out, s)
		}
	}
	return out
}

func (f *fx) state(t testing.TB, apptID string) string {
	t.Helper()
	var s string
	if err := f.env.Owner.QueryRow(context.Background(), `SELECT state FROM booking.appointment WHERE id=$1`, apptID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fx) eventsFor(t testing.TB, typ, appt string) int {
	t.Helper()
	n := 0
	for _, e := range f.events(t, typ) {
		if e["appointmentRef"] == appt {
			n++
		}
	}
	return n
}

func wantProb(t testing.TB, err error, code string, status int) {
	t.Helper()
	var p *problem.Problem
	if !errors.As(err, &p) || p.Code != code || p.Status != status {
		t.Fatalf("want problem %s/%d, got %v", code, status, err)
	}
}

// ---- E-BK-06 / S-145: close the clinic for a day, cancel mode ----

func TestEBK06ClosureCancelsOnlyThatDayOfThatClinic(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	clinicA, clinicB := ids.New(), ids.New()
	pa1, pa2 := f.practiceAt(t, clinicA), f.practiceAt(t, clinicA)
	pb := f.practiceAt(t, clinicB)
	admin := adminActor(clinicA)

	apptA1 := f.paid(t, pa1, ids.New(), f.slotOn(t, pa1, closureDay, 1))
	apptA2 := f.paid(t, pa2, ids.New(), f.slotOn(t, pa2, closureDay, 2))
	other := f.paid(t, pa1, ids.New(), f.slotOn(t, pa1, nextDay, 0))  // next day: not affected
	apptB := f.paid(t, pb, ids.New(), f.slotOn(t, pb, closureDay, 1)) // other clinic, same day: not affected

	c, err := f.m.CloseClinic(ctx, admin, clinicA, booking.ClosureInput{Date: closureDay, Mode: booking.ClosureCancel})
	if err != nil {
		t.Fatal(err)
	}
	if c.State != booking.ClosureDone || c.CancelledCount != 2 || c.Mode != booking.ClosureCancel || c.Date != closureDay || c.BranchRef != "" || c.CompletedAt == nil {
		t.Fatalf("closure: %+v", c)
	}
	for _, a := range []string{apptA1.ID, apptA2.ID} {
		if f.state(t, a) != booking.StateCancelled {
			t.Fatalf("%s should be cancelled", a)
		}
	}
	if f.state(t, other.ID) != booking.StateConfirmed || f.state(t, apptB.ID) != booking.StateConfirmed {
		t.Fatal("an appointment outside the closure was touched")
	}
	// Cancelled by the clinic through the normal event: payments refunds from the stored
	// policy (RF04), notifications tells the patient. Booking sends and refunds nothing itself.
	ev := f.events(t, "appointment.cancelled")
	if len(ev) != 2 {
		t.Fatalf("cancelled events: %v", ev)
	}
	for _, e := range ev {
		if e["byWhom"] != "clinic" || e["rule"] != booking.RuleClinicOrDoctorCancelled || e["policyVersion"] != testPolicy || e["clinicRef"] != clinicA {
			t.Fatalf("cancel payload: %v", e)
		}
	}
	if n := len(f.events(t, "appointment.reschedule_needed")); n != 0 {
		t.Fatalf("cancel mode must not ask patients to reschedule as well: %d", n)
	}
	// The day's slots are gone for clinic A (including the cancelled ones); other days and B stay.
	if n := len(f.slotsOn(t, pa1, closureDay)) + len(f.slotsOn(t, pa2, closureDay)); n != 0 {
		t.Fatalf("slots left on the closed day: %d", n)
	}
	if len(f.slotsOn(t, pa1, nextDay)) == 0 || len(f.slotsOn(t, pa1, previousDay)) == 0 {
		t.Fatal("slots of other days were removed")
	}
	if len(f.slotsOn(t, pb, closureDay)) == 0 {
		t.Fatal("another clinic lost its slots")
	}
	if n := f.count(t, `SELECT count(*) FROM booking.reconfirmation_task WHERE appointment_id = ANY($1) AND state IN ('pending','overdue')`, []string{apptA1.ID, apptA2.ID}); n != 0 {
		t.Fatalf("reconfirmation tasks still open: %d", n)
	}
	// Audited: the closure and each cancellation.
	if f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action='booking.clinic_closure.create'`) != 1 {
		t.Fatal("closure not audited")
	}
	if f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action='booking.cancel'`) < 2 {
		t.Fatal("cancellations not audited")
	}
	// The closed day cannot be booked.
	if _, err := f.holdIn(pa1, ids.New(), f.slotOn(t, pa1, nextDay, 1)); err != nil {
		t.Fatalf("next day should still book: %v", err)
	}
	// New slot generation respects the closure too.
	if _, err := f.m.MaterialiseSlots(ctx, f.now()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.slotsOn(t, pa1, closureDay)); n != 0 {
		t.Fatalf("materialising brought the closed day back: %d", n)
	}
}

func TestEBK06ClosureIsIdempotent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	clinic := ids.New()
	p := f.practiceAt(t, clinic)
	admin := adminActor(clinic)
	a := f.paid(t, p, ids.New(), f.slotOn(t, p, closureDay, 1))

	in := booking.ClosureInput{Date: closureDay, Mode: booking.ClosureCancel}
	c1, err := f.m.CloseClinic(ctx, admin, clinic, in)
	if err != nil {
		t.Fatal(err)
	}
	events := len(f.events(t, "appointment.cancelled"))
	audits := f.count(t, `SELECT count(*) FROM audit.audit_outbox`)
	outbox := f.count(t, `SELECT count(*) FROM booking.event_outbox`)
	// Same again, from the same admin and from another admin of the clinic.
	for _, who := range []booking.Actor{admin, adminActor(clinic)} {
		c2, err := f.m.CloseClinic(ctx, who, clinic, in)
		if err != nil {
			t.Fatal(err)
		}
		if c2.ID != c1.ID || c2.CancelledCount != 1 || c2.State != booking.ClosureDone {
			t.Fatalf("second call: %+v", c2)
		}
	}
	if len(f.events(t, "appointment.cancelled")) != events || f.count(t, `SELECT count(*) FROM audit.audit_outbox`) != audits ||
		f.count(t, `SELECT count(*) FROM booking.event_outbox`) != outbox || f.count(t, `SELECT count(*) FROM booking.clinic_closure`) != 1 {
		t.Fatal("a repeated closure changed something")
	}
	if f.eventsFor(t, "appointment.cancelled", a.ID) != 1 {
		t.Fatal("cancelled more than once")
	}
	// The other choice for the same day is refused and changes nothing.
	_, err = f.m.CloseClinic(ctx, admin, clinic, booking.ClosureInput{Date: closureDay, Mode: booking.ClosureReschedule})
	wantProb(t, err, "booking.closure_mode_conflict", 409)
	// Concurrent identical requests: one closure, each appointment cancelled once.
	f2 := setup(t)
	clinic2 := ids.New()
	p2 := f2.practiceAt(t, clinic2)
	var appts []string
	for i := 0; i < 4; i++ {
		appts = append(appts, f2.paid(t, p2, ids.New(), f2.slotOn(t, p2, closureDay, 0)).ID)
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f2.m.CloseClinic(ctx, adminActor(clinic2), clinic2, in); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if f2.count(t, `SELECT count(*) FROM booking.clinic_closure`) != 1 {
		t.Fatal("parallel requests made several closures")
	}
	for _, id := range appts {
		if f2.state(t, id) != booking.StateCancelled || f2.eventsFor(t, "appointment.cancelled", id) != 1 {
			t.Fatalf("appointment %s: state %s, cancel events %d", id, f2.state(t, id), f2.eventsFor(t, "appointment.cancelled", id))
		}
	}
	if f2.count(t, `SELECT cancelled_count FROM booking.clinic_closure`) != 4 {
		t.Fatal("cancelled count is off")
	}
}

// ---- crash safety: a closure that stopped half way is finished by running it again ----

func TestEBK06ClosureResumesAfterAFailureAndNeverDoubleCancels(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	clinic := ids.New()
	p := f.practiceAt(t, clinic)
	admin := adminActor(clinic)
	var appts []string
	for i := 0; i < 3; i++ {
		appts = append(appts, f.paid(t, p, ids.New(), f.slotOn(t, p, closureDay, i)).ID)
	}
	// One appointment cannot be cancelled right now (a database fault), the rest can.
	exec := func(sql string) {
		t.Helper()
		if _, err := f.env.Owner.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(fmt.Sprintf(`CREATE FUNCTION booking.fail_one() RETURNS trigger AS $$ BEGIN
		IF NEW.id = '%s'::uuid AND NEW.state = 'cancelled' THEN RAISE EXCEPTION 'injected fault'; END IF; RETURN NEW; END $$ LANGUAGE plpgsql`, appts[1]))
	exec(`CREATE TRIGGER fail_one BEFORE UPDATE ON booking.appointment FOR EACH ROW EXECUTE FUNCTION booking.fail_one()`)

	c, err := f.m.CloseClinic(ctx, admin, clinic, booking.ClosureInput{Date: closureDay, Mode: booking.ClosureCancel})
	if err != nil {
		t.Fatalf("the closure is recorded even when processing stops: %v", err)
	}
	if c.State != booking.ClosureProcessing || c.CancelledCount != 2 || c.CompletedAt != nil {
		t.Fatalf("half done: %+v", c)
	}
	if f.state(t, appts[0]) != booking.StateCancelled || f.state(t, appts[2]) != booking.StateCancelled || f.state(t, appts[1]) != booking.StateConfirmed {
		t.Fatal("the failure spread to other appointments")
	}
	// The closed day's slots are already gone, so nobody can book into it meanwhile.
	if n := len(f.slotsOn(t, p, closureDay)); n != 0 {
		t.Fatalf("open slots on a closing day: %d", n)
	}
	// Still failing: the sweep reports nothing finished and leaves the rest alone.
	if n, err := f.m.ResumeClosures(ctx); err != nil || n != 0 {
		t.Fatalf("sweep while failing: %d %v", n, err)
	}
	exec(`DROP TRIGGER fail_one ON booking.appointment`)
	if n, err := f.m.ResumeClosures(ctx); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	for _, id := range appts {
		if f.state(t, id) != booking.StateCancelled || f.eventsFor(t, "appointment.cancelled", id) != 1 {
			t.Fatalf("appointment %s: %s, %d cancel events", id, f.state(t, id), f.eventsFor(t, "appointment.cancelled", id))
		}
	}
	got, err := f.m.ListClosures(ctx, admin, clinic, 10)
	if err != nil || len(got) != 1 || got[0].State != booking.ClosureDone || got[0].CancelledCount != 3 {
		t.Fatalf("list: %+v %v", got, err)
	}
	// Running it again, even in parallel, changes nothing.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = f.m.ProcessClosure(ctx, c.ID) }()
	}
	wg.Wait()
	if n, _ := f.m.ResumeClosures(ctx); n != 0 || len(f.events(t, "appointment.cancelled")) != 3 {
		t.Fatalf("rerun changed things: %d / %d events", n, len(f.events(t, "appointment.cancelled")))
	}
}

func TestEBK06ProcessingPicksUpClosureRecordedBeforeACrash(t *testing.T) {
	// A crash between recording the closure and cancelling leaves a 'processing' row and
	// untouched appointments. ResumeClosures does the rest.
	f := setup(t)
	ctx := context.Background()
	clinic := ids.New()
	p := f.practiceAt(t, clinic)
	a := f.paid(t, p, ids.New(), f.slotOn(t, p, closureDay, 1))
	b := f.paid(t, p, ids.New(), f.slotOn(t, p, closureDay, 2))
	id := ids.New()
	if _, err := f.env.Owner.Exec(ctx, `INSERT INTO booking.clinic_closure (id, clinic_ref, closure_date, mode, created_by, created_at) VALUES ($1,$2,$3::date,'cancel',$4,now())`,
		id, clinic, closureDay, ids.New()); err != nil {
		t.Fatal(err)
	}
	// One was already cancelled by hand before the sweep ran: it is left alone.
	if err := f.m.CancelByStaff(ctx, adminActor(clinic), a.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := f.m.ResumeClosures(ctx); err != nil || n != 1 {
		t.Fatalf("resume: %d %v", n, err)
	}
	if f.state(t, b.ID) != booking.StateCancelled || f.eventsFor(t, "appointment.cancelled", a.ID) != 1 || f.eventsFor(t, "appointment.cancelled", b.ID) != 1 {
		t.Fatal("resume did not finish exactly once")
	}
	if f.count(t, `SELECT cancelled_count FROM booking.clinic_closure WHERE id=$1`, id) != 1 {
		t.Fatal("the count should only include what the closure cancelled")
	}
}

// ---- reschedule mode ----

func TestEBK06ClosureRescheduleModeAsksPatientsToPickANewTime(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	clinic := ids.New()
	p := f.practiceAt(t, clinic)
	admin := adminActor(clinic)
	pat := ids.New()
	a := f.paid(t, p, pat, f.slotOn(t, p, closureDay, 1))
	later := f.paid(t, p, ids.New(), f.slotOn(t, p, nextDay, 1))

	c, err := f.m.CloseClinic(ctx, admin, clinic, booking.ClosureInput{Date: closureDay, Mode: booking.ClosureReschedule})
	if err != nil {
		t.Fatal(err)
	}
	if c.State != booking.ClosureDone || c.FlaggedCount != 1 || c.CancelledCount != 0 {
		t.Fatalf("closure: %+v", c)
	}
	if f.state(t, a.ID) != booking.StateConfirmed {
		t.Fatal("reschedule mode must not cancel")
	}
	if n := len(f.events(t, "appointment.cancelled")); n != 0 {
		t.Fatalf("cancelled events: %d", n)
	}
	ev := f.events(t, "appointment.reschedule_needed")
	if len(ev) != 1 || ev[0]["appointmentRef"] != a.ID || ev[0]["patientRef"] != pat || ev[0]["clinicRef"] != clinic {
		t.Fatalf("reschedule_needed: %v", ev)
	}
	got, err := f.m.Mine(ctx, pat, a.ID)
	if err != nil || !got.NeedsReschedule {
		t.Fatalf("appointment: %+v %v", got, err)
	}
	if later2, _ := f.m.Mine(ctx, later.PatientRef, later.ID); later2.NeedsReschedule {
		t.Fatal("a different day was flagged")
	}
	if n := len(f.slotsOn(t, p, closureDay)); n != 0 {
		t.Fatalf("slots left on the closed day: %d", n)
	}
	// The patient moves for free (no reschedule used, no notice rule) to a day that is open.
	moved, err := f.m.Reschedule(ctx, pat, a.ID, f.slotOn(t, p, nextDay, 3).ID, "")
	if err != nil || moved.RescheduleCount != 0 || moved.NeedsReschedule || moved.State != booking.StateConfirmed {
		t.Fatalf("reschedule: %+v %v", moved, err)
	}
	// Again: nothing new is emitted or changed.
	n := len(f.events(t, "appointment.reschedule_needed"))
	if _, err := f.m.CloseClinic(ctx, admin, clinic, booking.ClosureInput{Date: closureDay, Mode: booking.ClosureReschedule}); err != nil {
		t.Fatal(err)
	}
	if len(f.events(t, "appointment.reschedule_needed")) != n || f.count(t, `SELECT count(*) FROM booking.clinic_closure`) != 1 {
		t.Fatal("repeat changed something")
	}
}

// ---- appointments that must not be touched ----

func TestEBK06ClosureLeavesStartedFinishedAndPastAppointmentsAlone(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	clinic := ids.New()
	p := f.practiceAt(t, clinic)
	admin := adminActor(clinic)
	slots := f.slotsOn(t, p, closureDay) // 09:00, 09:30, 10:00, 10:30, 11:00, 11:30 local
	past := f.paid(t, p, ids.New(), slots[0])
	done := f.paid(t, p, ids.New(), slots[1])
	started := f.paid(t, p, ids.New(), slots[2])
	future := f.paid(t, p, ids.New(), slots[3])

	// It is 09:40 local on the closed day (04:10 UTC).
	f.setClock(time.Date(2026, 11, 18, 4, 10, 0, 0, time.UTC))
	if _, err := f.m.Mark(ctx, admin, done.ID, booking.MarkCompleted); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Mark(ctx, admin, started.ID, booking.MarkArrived); err != nil {
		t.Fatal(err)
	}
	hold, err := f.holdIn(p, ids.New(), slots[4])
	if err != nil {
		t.Fatal(err)
	}

	c, err := f.m.CloseClinic(ctx, admin, clinic, booking.ClosureInput{Date: closureDay, Mode: booking.ClosureCancel})
	if err != nil {
		t.Fatal(err)
	}
	if c.State != booking.ClosureDone || c.CancelledCount != 2 {
		t.Fatalf("closure: %+v", c)
	}
	for id, want := range map[string]string{past.ID: booking.StateConfirmed, done.ID: booking.StateCompleted, started.ID: booking.StateInProgress,
		future.ID: booking.StateCancelled, hold.ID: booking.StateCancelled} {
		if got := f.state(t, id); got != want {
			t.Errorf("appointment %s: %s, want %s", id, got, want)
		}
	}
	if n := len(f.events(t, "appointment.cancelled")); n != 1 {
		t.Fatalf("only the confirmed future appointment is a cancellation event: %d", n)
	}
	if f.eventsFor(t, "appointment.cancelled", started.ID)+f.eventsFor(t, "appointment.cancelled", done.ID)+f.eventsFor(t, "appointment.cancelled", past.ID) != 0 {
		t.Fatal("a started, finished or past appointment was cancelled")
	}
	// A closure for a date that is already over changes nothing.
	f.setClock(time.Date(2026, 11, 20, 4, 0, 0, 0, time.UTC))
	before := len(f.events(t, "appointment.cancelled"))
	if _, err := f.m.CloseClinic(ctx, admin, clinic, booking.ClosureInput{Date: "2026-11-19", Mode: booking.ClosureReschedule}); err != nil {
		t.Fatal(err)
	}
	if len(f.events(t, "appointment.cancelled")) != before || f.state(t, past.ID) != booking.StateConfirmed {
		t.Fatal("closing a past date touched appointments")
	}
}

// ---- S-130: branches (own zone, own closure) ----

func TestS130ClosureOfOneBranchAndOfTheWholeClinicUseEachBranchZone(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	clinic, branchX, branchY := ids.New(), ids.New(), ids.New()
	px := f.practiceIn(t, clinic, branchX, practiceZone) // UTC+5:30
	py := f.practiceIn(t, clinic, branchY, abroadZone)   // UTC+4
	admin := adminActor(clinic)

	// K-ARC-04: an appointment at a branch carries that branch's zone.
	ax := f.paid(t, px, ids.New(), f.slotOn(t, px, closureDay, 1))
	ay := f.paid(t, py, ids.New(), f.slotOn(t, py, closureDay, 1))
	ayNext := f.paid(t, py, ids.New(), f.slotOn(t, py, nextDay, 1))
	if ax.PracticeTimeZone != practiceZone || ay.PracticeTimeZone != abroadZone {
		t.Fatalf("zones: %s %s", ax.PracticeTimeZone, ay.PracticeTimeZone)
	}
	yBefore := len(f.slotsOn(t, py, closureDay))

	// Close branch X only.
	c, err := f.m.CloseClinic(ctx, admin, clinic, booking.ClosureInput{BranchRef: branchX, Date: closureDay, Mode: booking.ClosureCancel})
	if err != nil || c.BranchRef != branchX || c.CancelledCount != 1 {
		t.Fatalf("branch closure: %+v %v", c, err)
	}
	if f.state(t, ax.ID) != booking.StateCancelled || f.state(t, ay.ID) != booking.StateConfirmed {
		t.Fatal("a branch closure reached the other branch")
	}
	if len(f.slotsOn(t, px, closureDay)) != 0 || len(f.slotsOn(t, py, closureDay)) != yBefore {
		t.Fatal("slots of the wrong branch were removed")
	}
	// Close the whole clinic: branch Y's own local day goes (the Dubai date, not the Kolkata one).
	c2, err := f.m.CloseClinic(ctx, admin, clinic, booking.ClosureInput{Date: closureDay, Mode: booking.ClosureReschedule})
	if err != nil || c2.BranchRef != "" || c2.ID == c.ID || c2.FlaggedCount != 1 {
		t.Fatalf("clinic closure: %+v %v", c2, err)
	}
	if n := len(f.slotsOn(t, py, closureDay)); n != 0 {
		t.Fatalf("branch Y still has %d slots on its closed local day", n)
	}
	if len(f.slotsOn(t, py, previousDay)) == 0 || len(f.slotsOn(t, py, nextDay)) == 0 {
		t.Fatal("branch Y lost slots of other local days")
	}
	got, _ := f.m.Mine(ctx, ayNext.PatientRef, ayNext.ID)
	if got.NeedsReschedule || f.state(t, ayNext.ID) != booking.StateConfirmed {
		t.Fatal("the next local day was affected")
	}
	if g, _ := f.m.Mine(ctx, ay.PatientRef, ay.ID); !g.NeedsReschedule {
		t.Fatal("branch Y's appointment on the closed day should be asked to reschedule")
	}
}

// ---- who may close ----

func TestS145OnlyTheClinicAdminOfThatClinicMayCloseIt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	clinicA, clinicB, branchB := ids.New(), ids.New(), ids.New()
	pa := f.practiceAt(t, clinicA)
	f.practiceIn(t, clinicB, branchB, practiceZone)
	appt := f.paid(t, pa, ids.New(), f.slotOn(t, pa, closureDay, 1))
	in := booking.ClosureInput{Date: closureDay, Mode: booking.ClosureCancel}

	for name, a := range map[string]booking.Actor{
		"admin of another clinic": adminActor(clinicB),
		"doctor":                  docActor(pa),
		"patient":                 {UserID: ids.New(), Role: "patient"},
		"admin without a clinic":  {UserID: ids.New(), Role: "clinic_admin"},
	} {
		if _, err := f.m.CloseClinic(ctx, a, clinicA, in); err == nil {
			t.Errorf("%s closed the clinic", name)
		} else {
			wantProb(t, err, "resource.not_found", 404)
		}
		if _, err := f.m.ListClosures(ctx, a, clinicA, 10); err == nil {
			t.Errorf("%s listed closures", name)
		}
	}
	// Front desk of the clinic may read but not close (a closure cancels paid appointments).
	desk := deskActor(clinicA)
	_, err := f.m.CloseClinic(ctx, desk, clinicA, in)
	wantProb(t, err, "auth.forbidden", 403)
	if _, err := f.m.ListClosures(ctx, desk, clinicA, 10); err != nil {
		t.Fatalf("front desk read: %v", err)
	}
	// A branch of another clinic, and an unknown one, answer 404 for the clinic's own admin.
	for _, b := range []string{branchB, ids.New(), "not-a-uuid"} {
		_, err := f.m.CloseClinic(ctx, adminActor(clinicA), clinicA, booking.ClosureInput{BranchRef: b, Date: closureDay, Mode: booking.ClosureCancel})
		wantProb(t, err, "resource.not_found", 404)
	}
	_, err = f.m.CloseClinic(ctx, adminActor(clinicA), "not-a-uuid", in)
	wantProb(t, err, "resource.not_found", 404)
	if f.state(t, appt.ID) != booking.StateConfirmed || f.count(t, `SELECT count(*) FROM booking.clinic_closure`) != 0 {
		t.Fatal("a refused request changed something")
	}
	// Closure lists show only the clinic's own.
	if _, err := f.m.CloseClinic(ctx, adminActor(clinicA), clinicA, in); err != nil {
		t.Fatal(err)
	}
	if l, err := f.m.ListClosures(ctx, adminActor(clinicB), clinicB, 10); err != nil || len(l) != 0 {
		t.Fatalf("another clinic sees %d closures (%v)", len(l), err)
	}
}

func TestS145ClosureValidation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	clinic := ids.New()
	f.practiceAt(t, clinic)
	admin := adminActor(clinic)
	for name, in := range map[string]booking.ClosureInput{
		"unknown mode": {Date: closureDay, Mode: "delete"},
		"no mode":      {Date: closureDay},
		"bad date":     {Date: "2026-13-45", Mode: booking.ClosureCancel},
		"no date":      {Mode: booking.ClosureCancel},
		"long past":    {Date: "2026-01-01", Mode: booking.ClosureCancel},
		"too far":      {Date: "2028-01-01", Mode: booking.ClosureCancel},
	} {
		_, err := f.m.CloseClinic(ctx, admin, clinic, in)
		if err == nil {
			t.Errorf("%s accepted", name)
			continue
		}
		wantProb(t, err, "booking.invalid_closure", 422)
	}
	if f.count(t, `SELECT count(*) FROM booking.clinic_closure`) != 0 {
		t.Fatal("a refused closure was stored")
	}
}

// ---- HTTP: roles and ownership on the route ----

func TestS145HTTPCloseClinic(t *testing.T) {
	h := newHTTP(t)
	clinic, other := ids.New(), ids.New()
	p := h.practiceAt(t, clinic)
	h.practiceAt(t, other)
	h.paid(t, p, ids.New(), h.slotOn(t, p, closureDay, 1))
	h.auth["admin"] = proPrincipal(ids.New(), "clinic_admin", clinic)
	h.auth["otheradmin"] = proPrincipal(ids.New(), "clinic_admin", other)
	h.auth["desk"] = proPrincipal(ids.New(), "front_desk", clinic)
	h.auth["pat"] = patientPrincipal(ids.New())
	path := "/v1/clinics/" + clinic + "/closures"
	body := map[string]any{"date": closureDay, "mode": "cancel"}

	for tok, want := range map[string]int{"pat": 403, "desk": 403, "otheradmin": 404} {
		if st, _ := h.do(t, "POST", path, tok, body); st != want {
			t.Errorf("%s: %d, want %d", tok, st, want)
		}
	}
	st, out := h.do(t, "POST", path, "admin", body)
	if st != 200 || out["state"] != "done" || out["cancelledCount"] != float64(1) || out["date"] != closureDay || out["mode"] != "cancel" {
		t.Fatalf("close: %d %v", st, out)
	}
	st, again := h.do(t, "POST", path, "admin", body)
	if st != 200 || again["closureId"] != out["closureId"] {
		t.Fatalf("again: %d %v", st, again)
	}
	if st, _ := h.do(t, "POST", path, "admin", map[string]any{"date": closureDay, "mode": "reschedule"}); st != 409 {
		t.Errorf("other mode: %d", st)
	}
	if st, o := h.do(t, "POST", path, "admin", map[string]any{"date": closureDay, "mode": "cancel", "branchId": ids.New()}); st != 404 {
		t.Errorf("unknown branch: %d %v", st, o)
	}
	if st, _ := h.do(t, "POST", path, "admin", map[string]any{"date": closureDay, "mode": "nope"}); st != 400 && st != 422 {
		t.Errorf("bad mode: %d", st)
	}
	st, list := h.do(t, "GET", path, "desk", nil)
	if items, _ := list["items"].([]any); st != 200 || len(items) != 1 {
		t.Fatalf("front desk list: %d %v", st, list)
	}
	if st, _ := h.do(t, "GET", path, "otheradmin", nil); st != 404 {
		t.Errorf("another clinic's list: %d", st)
	}
	if st, _ := h.do(t, "GET", path, "pat", nil); st != 403 {
		t.Errorf("patient list: %d", st)
	}
}
