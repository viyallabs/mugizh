package booking_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/booking"
	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	pp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

var bg = context.Background()

func isProblem(err error, p *problem.Problem) bool {
	var got *problem.Problem
	return errors.As(err, &got) && got.Code == p.Code
}

// ---- TC-22 / S-066: 100 parallel holds on one slot, exactly one wins ----

type race struct {
	wins, taken, other int
	next               int
	lat                []time.Duration
	wall               time.Duration
}

func (f *fx) race(t testing.TB, practiceID string, slot booking.SlotInfo, n int) race {
	t.Helper()
	var (
		mu    sync.Mutex
		res   race
		wg    sync.WaitGroup
		start = make(chan struct{})
	)
	res.lat = make([]time.Duration, 0, n)
	t0 := time.Now()
	for i := 0; i < n; i++ {
		patient := ids.New()
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s := time.Now()
			_, err := f.m.Hold(bg, booking.HoldInput{PatientRef: patient, PracticeID: practiceID, SlotID: slot.ID, Mode: slot.Mode, PatientTimeZone: practiceZone})
			d := time.Since(s)
			mu.Lock()
			defer mu.Unlock()
			res.lat = append(res.lat, d)
			var st *booking.SlotTakenError
			switch {
			case err == nil:
				res.wins++
			case errors.As(err, &st) && errors.Is(err, booking.ProblemSlotTaken):
				res.taken++
				if len(st.Next) > 0 {
					res.next++
				}
			default:
				res.other++
				t.Errorf("unexpected hold error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	res.wall = time.Since(t0)
	return res
}

func TestTC22ParallelHoldsOneWinner(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	slot := f.openSlots(t, p, booking.ModeInPerson)[3]
	r := f.race(t, p.ID, slot, 100)
	if r.wins != 1 || r.taken != 99 || r.other != 0 {
		t.Fatalf("wins=%d taken=%d other=%d", r.wins, r.taken, r.other)
	}
	if r.next != 99 {
		t.Fatalf("every loser must be offered the next open slots, got %d", r.next)
	}
	if n := f.count(t, `SELECT count(*) FROM booking.hold WHERE slot_id=$1`, slot.ID); n != 1 {
		t.Fatalf("holds on slot: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM booking.appointment WHERE slot_id=$1`, slot.ID); n != 1 {
		t.Fatalf("appointments on slot: %d", n)
	}
}

// TestS066HoldTimingReport runs the 100-parallel scenario 20 times and logs the latency of
// a single hold call and of the whole batch (docs/test/load-test-plan.md, S-066).
func TestS066HoldTimingReport(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	slots := f.openSlots(t, p, booking.ModeInPerson)
	var all, walls []time.Duration
	for i := 0; i < 20; i++ {
		r := f.race(t, p.ID, slots[i], 100)
		if r.wins != 1 || r.taken != 99 {
			t.Fatalf("run %d: wins=%d taken=%d", i, r.wins, r.taken)
		}
		all = append(all, r.lat...)
		walls = append(walls, r.wall)
	}
	stat := func(d []time.Duration) (time.Duration, time.Duration, time.Duration) {
		s := append([]time.Duration(nil), d...)
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		return s[0], s[len(s)/2], s[len(s)-1]
	}
	mn, md, mx := stat(all)
	t.Logf("S-066 per-call hold latency over 20 runs x 100 holds: min=%v median=%v max=%v", mn, md, mx)
	mn, md, mx = stat(walls)
	t.Logf("S-066 100-parallel batch wall time over 20 runs: min=%v median=%v max=%v", mn, md, mx)
}

// ---- TC-23: availability change never deletes booked slots ----

func TestTC23AvailabilityChangeKeepsBookings(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	patient := ids.New()
	slots := f.openSlots(t, p, booking.ModeInPerson)
	s := slots[10] // a day or two ahead
	a := f.paid(t, p, patient, s)

	// The doctor drops every in-person window: only video remains.
	var rules []booking.RuleInput
	for _, r := range weekly() {
		if r.Mode == booking.ModeVideo {
			rules = append(rules, r)
		}
	}
	conflicts, err := f.m.SetWeeklyTemplate(bg, docActor(p), p.ID, rules)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || conflicts[0].AppointmentID != a.ID {
		t.Fatalf("conflicts: %+v", conflicts)
	}
	got, _ := f.m.Mine(bg, patient, a.ID)
	if got.State != booking.StateConfirmed || !got.NeedsReschedule {
		t.Fatalf("booking must be kept and flagged: %+v", got)
	}
	if n := f.count(t, `SELECT count(*) FROM booking.slot WHERE id=$1 AND state='taken'`, s.ID); n != 1 {
		t.Fatal("the booked slot must not be deleted")
	}
	if n := f.count(t, `SELECT count(*) FROM booking.slot WHERE practice_id=$1 AND mode='in_person' AND state='open'`, p.ID); n != 0 {
		t.Fatalf("open in-person slots should be gone, %d left", n)
	}
	if ev := f.events(t, bp.EventRescheduleNeeded); len(ev) != 1 || ev[0]["patientRef"] != patient {
		t.Fatalf("reschedule_needed events: %v", ev)
	}
	// The doctor sees the conflict (through the records gate).
	f.gate.allow = func(r rp.Request) bool { return r.Kind == rp.KindBooking && r.BookingRef == a.ID }
	list, err := f.m.ListConflicts(bg, docActor(p), p.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list conflicts: %v %v", list, err)
	}
	// Restore the in-person windows: the flag clears and slots return.
	if _, err := f.m.SetWeeklyTemplate(bg, docActor(p), p.ID, weekly()); err != nil {
		t.Fatal(err)
	}
	got, _ = f.m.Mine(bg, patient, a.ID)
	if got.NeedsReschedule {
		t.Fatal("flag should clear when the booking fits again")
	}
	// A leave over that day flags it again, and the patient can then move it for free.
	loc, _ := time.LoadLocation(practiceZone)
	day := s.StartsAt.In(loc).Format("2006-01-02")
	if _, c, err := f.m.AddException(bg, adminActor(p.ClinicRef), p.ID, "leave", day, day); err != nil || len(c) != 1 {
		t.Fatalf("leave: %v %v", c, err)
	}
	other := f.openSlots(t, p, booking.ModeInPerson)[20]
	moved, err := f.m.Reschedule(bg, patient, a.ID, other.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if moved.RescheduleCount != 0 || moved.NeedsReschedule || !moved.StartsAt.Equal(other.StartsAt) {
		t.Fatalf("clinic-caused reschedule must be free: %+v", moved)
	}
	if n := f.count(t, `SELECT count(*) FROM booking.slot WHERE id=$1`, s.ID); n != 0 {
		t.Fatal("the old slot (inside the leave) should be removed, not reopened")
	}
}

func TestAvailabilityManagementPermissions(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	// Front desk can view but not change.
	if _, err := f.m.GetAvailability(bg, deskActor(p.ClinicRef), p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.SetWeeklyTemplate(bg, deskActor(p.ClinicRef), p.ID, weekly()); !isProblem(err, problem.Forbidden) {
		t.Fatalf("front desk write: %v", err)
	}
	// Another clinic's admin and another doctor get 404 (K-SEC-04).
	if _, err := f.m.GetAvailability(bg, adminActor(ids.New()), p.ID); !isProblem(err, problem.NotFound) {
		t.Fatalf("other clinic admin: %v", err)
	}
	other := f.practice(t)
	if _, err := f.m.SetWeeklyTemplate(bg, docActor(other), p.ID, weekly()); !isProblem(err, problem.NotFound) {
		t.Fatalf("other doctor: %v", err)
	}
	// A mode the practice does not offer is refused.
	bad := []booking.RuleInput{{Weekday: 1, StartLocal: "09:00", EndLocal: "10:00", SlotMinutes: 30, Mode: booking.ModeAudio}}
	if _, err := f.m.SetWeeklyTemplate(bg, docActor(p), p.ID, bad); !isProblem(err, booking.ProblemBadAvailability) {
		t.Fatalf("mode not offered: %v", err)
	}
}

func TestMaterialiseSlotsIdempotentAndHorizon(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	before := f.count(t, `SELECT count(*) FROM booking.slot WHERE practice_id=$1`, p.ID)
	if before < 30*5 {
		t.Fatalf("expected about 30 days of slots, got %d", before)
	}
	if n, err := f.m.MaterialiseSlots(bg, f.now()); err != nil || n != 0 {
		t.Fatalf("second run must add nothing: %d %v", n, err)
	}
	// Ten days later the horizon has moved: new slots appear, none are duplicated.
	f.advance(10 * 24 * time.Hour)
	n, err := f.m.MaterialiseSlots(bg, f.now())
	if err != nil || n == 0 {
		t.Fatalf("expected new slots: %d %v", n, err)
	}
	if d := f.count(t, `SELECT count(*) - count(DISTINCT start_at) FROM booking.slot WHERE practice_id=$1`, p.ID); d != 0 {
		t.Fatalf("duplicate slot starts: %d", d)
	}
	max := f.count(t, `SELECT count(*) FROM booking.slot WHERE practice_id=$1 AND start_at > $2`, p.ID, f.now().Add(31*24*time.Hour))
	if max != 0 {
		t.Fatalf("slots beyond the horizon: %d", max)
	}
}

// ---- TC-24 / S-123: patient abroad, times in UTC with zones, P10 ----

func TestTC24PatientAbroad(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	patient := ids.New()
	inP := f.openSlots(t, p, booking.ModeInPerson)[8]
	a, err := f.m.Hold(bg, booking.HoldInput{PatientRef: patient, PracticeID: p.ID, SlotID: inP.ID, Mode: booking.ModeInPerson, PatientTimeZone: abroadZone})
	if err != nil {
		t.Fatalf("in-person booking from abroad must be allowed: %v", err)
	}
	if a.PatientInIndiaConfirmed != nil {
		t.Fatal("no location confirmation is stored for in-person")
	}
	if a.StartsAt.Location() != time.UTC || a.PracticeTimeZone != practiceZone || a.PatientTimeZone != abroadZone {
		t.Fatalf("stored times and zones: %+v", a)
	}
	pl, _ := time.Parse(time.RFC3339, a.PracticeLocalStart())
	dl, _ := time.Parse(time.RFC3339, a.PatientLocalStart())
	if !pl.Equal(dl) || !pl.Equal(a.StartsAt) {
		t.Fatal("local renderings must be the same instant")
	}
	if _, po := pl.Zone(); po != 5*3600+1800 {
		t.Fatalf("practice offset %d", po)
	}
	if _, do := dl.Zone(); do != 4*3600 {
		t.Fatalf("patient offset %d", do)
	}
	if got, _ := f.m.Mine(bg, patient, a.ID); !got.StartsAt.Equal(a.StartsAt) {
		t.Fatal("round trip")
	}

	// Online consult without "I am in India now": refused (P10, RX_PATIENT_OUTSIDE_INDIA).
	vid := f.openSlots(t, p, booking.ModeVideo)[2]
	_, err = f.m.Hold(bg, booking.HoldInput{PatientRef: patient, PracticeID: p.ID, SlotID: vid.ID, Mode: booking.ModeVideo, PatientTimeZone: abroadZone})
	if !isProblem(err, booking.ProblemOutsideIndia) {
		t.Fatalf("video without confirmation: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM booking.slot WHERE id=$1 AND state='open'`, vid.ID); n != 1 {
		t.Fatal("a refused request must not take the slot")
	}
	va, err := f.m.Hold(bg, booking.HoldInput{PatientRef: patient, PracticeID: p.ID, SlotID: vid.ID, Mode: booking.ModeVideo, PatientTimeZone: abroadZone, PatientInIndiaConfirmed: true})
	if err != nil || va.PatientInIndiaConfirmed == nil || !*va.PatientInIndiaConfirmed {
		t.Fatalf("video with confirmation: %+v %v", va, err)
	}
	// Invalid zones are refused.
	for _, z := range []string{"", "Local", "Not/AZone"} {
		_, err = f.m.Hold(bg, booking.HoldInput{PatientRef: ids.New(), PracticeID: p.ID, SlotID: f.openSlots(t, p, booking.ModeInPerson)[0].ID, Mode: booking.ModeInPerson, PatientTimeZone: z})
		if !isProblem(err, booking.ProblemInvalidTimeZone) {
			t.Fatalf("zone %q: %v", z, err)
		}
	}
}

// ---- TC-25: doctor no-show on an online consult ----

func TestTC25DoctorNoShow(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	patient := ids.New()
	a := f.paid(t, p, patient, f.openSlots(t, p, booking.ModeVideo)[4])

	f.setClock(a.StartsAt.Add(5 * time.Minute))
	if err := f.m.MarkDoctorNoShow(bg, a.ID); !errors.Is(err, bp.ErrTooEarly) {
		t.Fatalf("before the timeout: %v", err)
	}
	f.setClock(a.StartsAt.Add(10 * time.Minute))
	if err := f.m.MarkDoctorNoShow(bg, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.m.MarkDoctorNoShow(bg, a.ID); err != nil { // idempotent
		t.Fatal(err)
	}
	ev := f.events(t, bp.EventNoShow)
	if len(ev) != 1 {
		t.Fatalf("one no_show event expected, got %v", ev)
	}
	e := ev[0]
	if e["appointmentRef"] != a.ID || e["byWhom"] != "doctor" || e["rule"] != "RF03" || e["policyVersion"] != testPolicy || e["mode"] != "online" {
		t.Fatalf("payload: %v", e)
	}
	got, _ := f.m.Mine(bg, patient, a.ID)
	if got.State != booking.StateNoShow || got.NoShowBy != "doctor" {
		t.Fatalf("state: %+v", got)
	}
}

// D-46, review F5/F6: an online consult's outcome reaches the appointment through
// consult.ended, and payments gets the event that applies the stored refund policy.
func TestConsultOutcomesReachTheAppointment(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	slots := f.openSlots(t, p, booking.ModeVideo)
	ended := func(appt, outcome string) outbox.Event {
		return outbox.Event{ID: ids.New(), Type: "consult.ended", Payload: map[string]string{"appointmentRef": appt, "outcome": outcome, "sessionRef": ids.New()}}
	}
	type want struct{ state, noShowBy, event, byWhom, rule string }
	cases := map[string]want{
		"completed":           {booking.StateCompleted, "", bp.EventCompleted, "", ""},
		"patient_no_show":     {booking.StateNoShow, "patient", bp.EventNoShow, "patient", ""},
		"doctor_no_show":      {booking.StateNoShow, "doctor", bp.EventNoShow, "doctor", "RF03"},
		"technical_failure":   {booking.StateCancelled, "", bp.EventCancelled, "system", "RF05"},
		"converted_in_person": {booking.StateCancelled, "", bp.EventCancelled, "system", "RF06"},
		"minor_stopped":       {booking.StateConfirmed, "", "", "", ""},
	}
	i := 0
	for outcome, w := range cases {
		patient := ids.New()
		a := f.paid(t, p, patient, slots[i])
		i++
		f.setClock(a.StartsAt.Add(20 * time.Minute))
		ev := ended(a.ID, outcome)
		for k := 0; k < 2; k++ { // replay-safe
			if err := f.m.OnConsultEnded(bg, ev); err != nil {
				t.Fatalf("%s: %v", outcome, err)
			}
		}
		got, _ := f.m.Mine(bg, patient, a.ID)
		if got.State != w.state || got.NoShowBy != w.noShowBy {
			t.Fatalf("%s: state %s by %q", outcome, got.State, got.NoShowBy)
		}
		if w.event == "" {
			continue
		}
		var mine []map[string]string
		for _, e := range f.events(t, w.event) {
			if e["appointmentRef"] == a.ID {
				mine = append(mine, e)
			}
		}
		if len(mine) != 1 || mine[0]["byWhom"] != w.byWhom || mine[0]["rule"] != w.rule {
			t.Fatalf("%s: events %v", outcome, mine)
		}
	}
	// Doctor no-show before booking's own timeout retries (an error), never applies early.
	a := f.paid(t, p, ids.New(), slots[i])
	f.setClock(a.StartsAt.Add(time.Minute))
	if err := f.m.OnConsultEnded(bg, ended(a.ID, "doctor_no_show")); !errors.Is(err, bp.ErrTooEarly) {
		t.Fatalf("too early: %v", err)
	}
	if err := f.m.OnConsultEnded(bg, ended(a.ID, "bogus")); err == nil {
		t.Fatal("unknown outcome must be refused")
	}
	if err := f.m.OnConsultEnded(bg, ended(ids.New(), "completed")); err != nil {
		t.Fatalf("unknown appointment is ignored: %v", err)
	}
}

// TM-13 (S-178): staff can never decide an online consult's outcome. "Completed" needs both
// sides connected and a patient no-show needs the doctor present (consult module, D-46,
// D-50), so a clinic cannot mark an online patient absent (no refund) or a call completed
// that never happened. Every mark on an online appointment is refused and nothing changes.
func TestTM13StaffCannotMarkOnlineConsults(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	for _, mode := range []string{booking.ModeVideo} { // audio takes the same branch (Mode != in_person); the fixture offers video only
		slots := f.openSlots(t, p, mode)
		a := f.paid(t, p, ids.New(), slots[3])
		f.setClock(a.StartsAt.Add(30 * time.Minute))
		for _, actor := range []booking.Actor{deskActor(p.ClinicRef), docActor(p)} {
			for _, mark := range []string{booking.MarkArrived, booking.MarkCompleted, booking.MarkPatientNoShow, booking.MarkDoctorNoShow} {
				if _, err := f.m.Mark(bg, actor, a.ID, mark); !isProblem(err, booking.ProblemOnlineOutcome) {
					t.Fatalf("%s %s by %s: want %v, got %v", mode, mark, actor.Role, booking.ProblemOnlineOutcome, err)
				}
			}
		}
		if n := f.count(t, `SELECT count(*) FROM booking.appointment WHERE id=$1 AND state='confirmed'`, a.ID); n != 1 {
			t.Fatalf("%s appointment must stay confirmed", mode)
		}
	}
	if ev := f.events(t, bp.EventNoShow); len(ev) != 0 {
		t.Fatalf("no no-show may be emitted: %v", ev)
	}
	if ev := f.events(t, bp.EventCompleted); len(ev) != 0 {
		t.Fatalf("no completion may be emitted: %v", ev)
	}
}

func TestStaffMarksAndDayView(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	slots := f.openSlots(t, p, booking.ModeInPerson)
	a1 := f.paid(t, p, ids.New(), slots[6])
	a2 := f.paid(t, p, ids.New(), slots[7])
	a3 := f.paid(t, p, ids.New(), slots[8])
	desk := deskActor(p.ClinicRef)

	f.setClock(a1.StartsAt.Add(1 * time.Minute))
	if st, err := f.m.Mark(bg, desk, a1.ID, booking.MarkArrived); err != nil || st != booking.StateInProgress {
		t.Fatalf("arrived: %s %v", st, err)
	}
	if st, err := f.m.Mark(bg, desk, a1.ID, booking.MarkCompleted); err != nil || st != booking.StateCompleted {
		t.Fatalf("completed: %s %v", st, err)
	}
	if ev := f.events(t, bp.EventCompleted); len(ev) != 1 {
		t.Fatalf("completed events: %v", ev)
	}
	// Patient no-show needs the grace time (E-BK-05).
	f.setClock(a2.StartsAt.Add(5 * time.Minute))
	if _, err := f.m.Mark(bg, desk, a2.ID, booking.MarkPatientNoShow); !isProblem(err, booking.ProblemMarkTooEarly) {
		t.Fatalf("grace: %v", err)
	}
	f.setClock(a2.StartsAt.Add(11 * time.Minute))
	if st, err := f.m.Mark(bg, desk, a2.ID, booking.MarkPatientNoShow); err != nil || st != booking.StateNoShow {
		t.Fatalf("patient no-show: %s %v", st, err)
	}
	f.setClock(a3.StartsAt.Add(11 * time.Minute))
	if _, err := f.m.Mark(bg, docActor(p), a3.ID, booking.MarkDoctorNoShow); err != nil {
		t.Fatal(err)
	}
	ev := f.events(t, bp.EventNoShow)
	if len(ev) != 2 || ev[0]["byWhom"] != "patient" || ev[1]["byWhom"] != "doctor" {
		t.Fatalf("no_show events: %v", ev)
	}
	if _, has := ev[0]["rule"]; has {
		t.Fatal("a patient no-show names no rule; payments decides (RF09 is blocked)")
	}
	if ev[1]["rule"] != "" { // in-person doctor no-show: RF03 applies to online only
		t.Fatalf("in-person doctor no-show must not name RF03: %v", ev[1])
	}
	// Other clinics and other doctors get 404; every mark is audited.
	if _, err := f.m.Mark(bg, deskActor(ids.New()), a3.ID, booking.MarkCompleted); !isProblem(err, problem.NotFound) {
		t.Fatalf("other clinic: %v", err)
	}
	if _, err := f.m.Mark(bg, booking.Actor{UserID: ids.New(), Role: ip.RoleDoctor}, a3.ID, booking.MarkCompleted); !isProblem(err, problem.NotFound) {
		t.Fatalf("other doctor: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM audit.audit_outbox WHERE action LIKE 'booking.mark_%' OR action = 'booking.no_show'`); n < 5 {
		t.Fatalf("marks must be audited, found %d", n)
	}

	// Day view: front desk sees booking fields through the gate; a doctor without a grant sees nothing.
	loc, _ := time.LoadLocation(practiceZone)
	day := a1.StartsAt.In(loc).Format("2006-01-02")
	f.gate.mu.Lock()
	f.gate.requests = nil
	f.gate.mu.Unlock()
	items, _, err := f.m.DayView(bg, desk, p.ID, day, 50, "")
	if err != nil || len(items) == 0 {
		t.Fatalf("day view: %v %v", items, err)
	}
	f.gate.mu.Lock()
	for _, r := range f.gate.requests {
		if r.Kind != rp.KindBooking || r.BookingRef == "" || r.Actor.Role != ip.RoleFrontDesk {
			t.Errorf("gate request: %+v", r)
		}
	}
	f.gate.mu.Unlock()
	items, _, err = f.m.DayView(bg, docActor(p), p.ID, day, 50, "") // default fake denies the doctor (no T1)
	if err != nil || len(items) != 0 {
		t.Fatalf("a doctor without access must see no rows: %v %v", items, err)
	}
	if _, _, err := f.m.DayView(bg, deskActor(ids.New()), p.ID, day, 50, ""); !isProblem(err, problem.NotFound) {
		t.Fatalf("other clinic day view: %v", err)
	}
}

// ---- TC-26 / E-BK-07: reconfirmation ----

func TestTC26ReconfirmationNotDoneIsListedAndPatientWarned(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	slots := f.openSlots(t, p, booking.ModeInPerson)
	patient := ids.New()
	a := f.paid(t, p, patient, slots[30]) // several days ahead
	b := f.paid(t, p, ids.New(), slots[31])

	if got, _ := f.m.UnconfirmedReconfirmations(bg, f.now()); len(got) != 0 {
		t.Fatalf("nothing is due yet: %v", got)
	}
	// The clinic reconfirms b.
	if err := f.m.Reconfirm(bg, deskActor(p.ClinicRef), b.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.m.Reconfirm(bg, deskActor(ids.New()), a.ID); !isProblem(err, problem.NotFound) {
		t.Fatalf("other clinic: %v", err)
	}
	f.setClock(a.StartsAt.Add(-23 * time.Hour)) // the day before: due
	got, err := f.m.UnconfirmedReconfirmations(bg, f.now())
	if err != nil || len(got) != 1 || got[0].AppointmentID != a.ID || got[0].PatientRef != patient {
		t.Fatalf("unconfirmed: %v %v", got, err)
	}
	n, err := f.m.SweepReconfirmations(bg, f.now())
	if err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if n, _ := f.m.SweepReconfirmations(bg, f.now()); n != 0 {
		t.Fatal("the warning is sent once per task")
	}
	ev := f.events(t, bp.EventReconfirmationLapsed)
	if len(ev) != 1 || ev[0]["appointmentRef"] != a.ID || ev[0]["patientRef"] != patient || ev[0]["clinicRef"] != p.ClinicRef {
		t.Fatalf("events: %v", ev)
	}
	list, _, err := f.m.ListReconfirmations(bg, deskActor(p.ClinicRef), p.ClinicRef, 20, "")
	if err != nil || len(list) != 1 || list[0].PatientRef != "" || list[0].State != "overdue" {
		t.Fatalf("clinic list (no patient identity): %v %v", list, err)
	}
	if _, _, err := f.m.ListReconfirmations(bg, deskActor(ids.New()), p.ClinicRef, 20, ""); !isProblem(err, problem.NotFound) {
		t.Fatalf("other clinic list: %v", err)
	}
	// The patient is offered a full refund: cancelling is attributed to the clinic, RF04.
	prev, err := f.m.PreviewCancel(bg, patient, a.ID)
	if err != nil || prev.ByWhom != "clinic" || prev.RefundMinor != fee {
		t.Fatalf("preview: %+v %v", prev, err)
	}
	if _, _, err := f.m.Cancel(bg, patient, a.ID, fee, ""); err != nil {
		t.Fatal(err)
	}
	c := f.events(t, bp.EventCancelled)
	if len(c) != 1 || c[0]["byWhom"] != "clinic" || c[0]["rule"] != "RF04" || c[0]["mode"] != "in_person" || c[0]["minutesBeforeSlot"] == "" {
		t.Fatalf("cancel payload: %v", c)
	}
}

func TestReconfirmationSkippedWhenBookedTheSameDay(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	f.paid(t, p, ids.New(), f.openSlots(t, p, booking.ModeInPerson)[0]) // starts within hours
	if n := f.count(t, `SELECT count(*) FROM booking.reconfirmation_task`); n != 0 {
		t.Fatalf("no day-before task for a same-day booking: %d", n)
	}
}

// ---- TC-28 / TC-29: capture after the hold expired is never confirmed ----

func TestTC28CaptureAfterHoldExpiry(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	slots := f.openSlots(t, p, booking.ModeInPerson)
	patient := ids.New()
	a, err := f.holdIn(p, patient, slots[5])
	if err != nil {
		t.Fatal(err)
	}
	co, err := f.m.Checkout(bg, patient, a.ID)
	if err != nil || co.CheckoutURL == "" || co.AmountMinor != fee {
		t.Fatalf("checkout: %+v %v", co, err)
	}
	if got, _ := f.m.Mine(bg, patient, a.ID); got.State != booking.StatePendingPayment || got.OrderRef != co.OrderRef {
		t.Fatalf("state: %+v", got)
	}
	// 16 minutes pass; the expiry job releases the slot and tells payments.
	f.advance(16 * time.Minute)
	if n, err := f.m.ExpireHolds(bg, f.now()); err != nil || n != 1 {
		t.Fatalf("expire: %d %v", n, err)
	}
	if ev := f.events(t, bp.EventExpired); len(ev) != 1 || ev[0]["appointmentRef"] != a.ID || ev[0]["policyVersion"] != testPolicy {
		t.Fatalf("expired events: %v", ev)
	}
	if n := f.count(t, `SELECT count(*) FROM booking.slot WHERE id=$1 AND state='open'`, slots[5].ID); n != 1 {
		t.Fatal("slot must reopen")
	}
	// The delayed capture arrives: NOT confirmed; expired is emitted again so it is refunded.
	f.capture(t, a.ID, co.OrderRef)
	got, _ := f.m.Mine(bg, patient, a.ID)
	if got.State != booking.StateExpired {
		t.Fatalf("a late capture must not confirm: %s", got.State)
	}
	if ev := f.events(t, bp.EventExpired); len(ev) != 2 {
		t.Fatalf("a late capture must emit appointment.expired: %v", ev)
	}
	if ev := f.events(t, bp.EventConfirmed); len(ev) != 0 {
		t.Fatal("confirmed must never be emitted")
	}
	// Someone else can now book the slot.
	if _, err := f.holdIn(p, ids.New(), slots[5]); err != nil {
		t.Fatal(err)
	}
}

func TestTC29CaptureBeforeExpiryJobRuns(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	patient := ids.New()
	a, _ := f.holdIn(p, patient, f.openSlots(t, p, booking.ModeInPerson)[5])
	co, _ := f.m.Checkout(bg, patient, a.ID)
	f.advance(16 * time.Minute) // the job has not run yet
	f.capture(t, a.ID, co.OrderRef)
	got, _ := f.m.Mine(bg, patient, a.ID)
	if got.State != booking.StateExpired || len(f.events(t, bp.EventExpired)) != 1 || len(f.events(t, bp.EventConfirmed)) != 0 {
		t.Fatalf("state=%s expired=%v", got.State, f.events(t, bp.EventExpired))
	}
	// Replaying the same event changes nothing (idempotent).
	e := outbox.Event{ID: ids.New(), Type: "payment.captured", Payload: map[string]string{"orderRef": co.OrderRef, "appointmentRef": a.ID}}
	for i := 0; i < 2; i++ {
		if err := f.m.OnPaymentCaptured(bg, e); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.events(t, bp.EventExpired)); n != 2 {
		t.Fatalf("same event id replayed must be a no-op, got %d expired events", n)
	}
}

func TestCaptureConfirmsAndEmitsConfirmedPayload(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	patient := ids.New()
	s := f.openSlots(t, p, booking.ModeVideo)[3]
	a := f.paid(t, p, patient, s)
	ev := f.events(t, bp.EventConfirmed)
	if len(ev) != 1 {
		t.Fatalf("confirmed events: %v", ev)
	}
	want := map[string]string{"appointmentRef": a.ID, "patientRef": patient, "doctorRef": p.ProfessionalRef, "clinicRef": p.ClinicRef, "practiceRef": p.ID,
		"mode": "video", "slotStart": s.StartsAt.Format(time.RFC3339), "slotEnd": s.EndsAt.Format(time.RFC3339)}
	for k, v := range want {
		if ev[0][k] != v {
			t.Errorf("%s: got %q want %q", k, ev[0][k], v)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM booking.hold`); n != 0 {
		t.Fatal("the hold is gone once confirmed")
	}
	// Duplicate capture of a confirmed appointment changes nothing.
	f.capture(t, a.ID, f.pay.orders[a.ID])
	if len(f.events(t, bp.EventConfirmed)) != 1 || len(f.events(t, bp.EventExpired)) != 0 {
		t.Fatal("duplicate capture must be a no-op")
	}
	// Confirmation is only ever from a capture: a held appointment is not confirmed by anything else.
	h, _ := f.holdIn(p, ids.New(), f.openSlots(t, p, booking.ModeInPerson)[9])
	if h.State != booking.StateHeld {
		t.Fatal(h.State)
	}
}

func TestUnlistedPracticeRefusedAtHoldCheckoutAndConfirm(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	slots := f.openSlots(t, p, booking.ModeInPerson)
	// A hold, then the doctor is unlisted: checkout refused.
	patient := ids.New()
	a, _ := f.holdIn(p, patient, slots[4])
	up := p
	up.Listed = false
	f.dir.set(up)
	if _, err := f.m.Checkout(bg, patient, a.ID); !isProblem(err, booking.ProblemPracticeGone) {
		t.Fatalf("checkout of unlisted: %v", err)
	}
	if _, err := f.holdIn(p, ids.New(), slots[5]); !isProblem(err, problem.NotFound) {
		t.Fatalf("hold of unlisted: %v", err)
	}
	if _, _, err := f.m.ListOpenSlots(bg, p.ID, time.Time{}, time.Time{}, 10, ""); !isProblem(err, problem.NotFound) {
		t.Fatalf("slots of unlisted: %v", err)
	}
	// Listed at checkout, unlisted when the payment lands: not confirmed, cancelled RF04.
	f.dir.set(p)
	b, _ := f.holdIn(p, patient, slots[6])
	co, err := f.m.Checkout(bg, patient, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.dir.set(up)
	f.capture(t, b.ID, co.OrderRef)
	got, _ := f.m.Mine(bg, patient, b.ID)
	if got.State != booking.StateCancelled || got.CancelledBy != "system" {
		t.Fatalf("confirm of unlisted: %+v", got)
	}
	c := f.events(t, bp.EventCancelled)
	if len(c) != 1 || c[0]["rule"] != "RF04" || c[0]["byWhom"] != "system" || len(f.events(t, bp.EventConfirmed)) != 0 {
		t.Fatalf("events: %v", c)
	}
}

// ---- TC-66 / E-VER-06: suspension cancels future bookings ----

func TestTC66SuspensionCancelsPaidFutureBookings(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	other := f.practice(t)
	slots := f.openSlots(t, p, booking.ModeInPerson)
	var mine []booking.Appointment
	for i := 6; i < 9; i++ {
		mine = append(mine, f.paid(t, p, ids.New(), slots[i]))
	}
	keep := f.paid(t, other, ids.New(), f.openSlots(t, other, booking.ModeInPerson)[6])
	unpaid, _ := f.holdIn(p, ids.New(), slots[12])

	e := outbox.Event{ID: ids.New(), Type: "verification.status_changed", Payload: map[string]string{"professionalRef": p.ProfessionalRef, "status": "suspended", "subjectKind": "professional"}}
	f.dir.set(dpUnlisted(p))
	for i := 0; i < 2; i++ { // delivered twice: idempotent
		if err := f.m.OnVerificationStatusChanged(bg, e); err != nil {
			t.Fatal(err)
		}
	}
	ev := f.events(t, bp.EventCancelled)
	if len(ev) != 3 {
		t.Fatalf("three cancelled events expected, got %d: %v", len(ev), ev)
	}
	seen := map[string]bool{}
	for _, c := range ev {
		seen[c["appointmentRef"]] = true
		if c["byWhom"] != "system" || c["rule"] != "RF04" || c["policyVersion"] != testPolicy || c["mode"] != "in_person" || c["minutesBeforeSlot"] == "" || c["clinicRef"] != p.ClinicRef || c["patientRef"] == "" {
			t.Errorf("payload: %v", c)
		}
	}
	for _, a := range mine {
		if !seen[a.ID] {
			t.Errorf("appointment %s not cancelled", a.ID)
		}
		got, _ := f.m.Mine(bg, a.PatientRef, a.ID)
		if got.State != booking.StateCancelled || got.CancelledBy != "system" || got.CancelRule != "RF04" {
			t.Errorf("state: %+v", got)
		}
	}
	if got, _ := f.m.Mine(bg, keep.PatientRef, keep.ID); got.State != booking.StateConfirmed {
		t.Fatal("another doctor's booking must be untouched")
	}
	if got, _ := f.m.Mine(bg, unpaid.PatientRef, unpaid.ID); got.State != booking.StateCancelled {
		t.Fatalf("an unpaid hold is released too: %s", got.State)
	}
	// No new holds for the suspended doctor.
	if _, err := f.holdIn(p, ids.New(), slots[20]); !isProblem(err, problem.NotFound) {
		t.Fatalf("hold after suspension: %v", err)
	}
	// A status that is not a suspension changes nothing.
	ok := outbox.Event{ID: ids.New(), Type: "verification.status_changed", Payload: map[string]string{"professionalRef": other.ProfessionalRef, "status": "verified"}}
	if err := f.m.OnVerificationStatusChanged(bg, ok); err != nil {
		t.Fatal(err)
	}
}

// ---- E-SEC-04: at most 2 active holds per patient ----

func TestESEC04MaxTwoActiveHolds(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	slots := f.openSlots(t, p, booking.ModeInPerson)
	patient := ids.New()
	for i := 0; i < 2; i++ {
		if _, err := f.holdIn(p, patient, slots[i+3]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.holdIn(p, patient, slots[8]); !isProblem(err, booking.ProblemTooManyHolds) {
		t.Fatalf("third hold: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM booking.slot WHERE id=$1 AND state='open'`, slots[8].ID); n != 1 {
		t.Fatal("a refused hold must leave the slot open")
	}
	// Re-holding a slot the patient already holds is a replay, not a third hold.
	if a, err := f.holdIn(p, patient, slots[3]); err != nil || a.State != booking.StateHeld {
		t.Fatalf("replay: %v", err)
	}
	// After the first two expire the patient can hold again.
	f.advance(16 * time.Minute)
	if _, err := f.holdIn(p, patient, slots[8]); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	// Parallel requests cannot beat the limit.
	q := ids.New()
	var wg sync.WaitGroup
	var mu sync.Mutex
	okN := 0
	for i := 10; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.holdIn(p, q, slots[i]); err == nil {
				mu.Lock()
				okN++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if okN != 2 {
		t.Fatalf("parallel holds by one patient: %d succeeded, want 2", okN)
	}
}

func TestExpiredHoldDoesNotBlockSlotBeforeJobRuns(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	s := f.openSlots(t, p, booking.ModeInPerson)[5]
	if _, err := f.holdIn(p, ids.New(), s); err != nil {
		t.Fatal(err)
	}
	if _, err := f.holdIn(p, ids.New(), s); !errors.Is(err, booking.ProblemSlotTaken) {
		t.Fatalf("live hold blocks: %v", err)
	}
	f.advance(16 * time.Minute)
	if _, err := f.holdIn(p, ids.New(), s); err != nil {
		t.Fatalf("dead hold must not block: %v", err)
	}
}

// ---- consent, fee, policy, pay-at-clinic, ownership ----

func TestPCareConsentRequired(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	patient := ids.New()
	f.gate.noCare[patient] = true
	s := f.openSlots(t, p, booking.ModeInPerson)[3]
	if _, err := f.holdIn(p, patient, s); !isProblem(err, problem.ConsentRequired) {
		t.Fatalf("no consent: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM booking.appointment`); n != 0 {
		t.Fatal("nothing must be created without consent")
	}
}

func TestFeeAndPolicyVersionStoredWithBooking(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	patient := ids.New()
	a, err := f.holdIn(p, patient, f.openSlots(t, p, booking.ModeInPerson)[4])
	if err != nil {
		t.Fatal(err)
	}
	if a.FeeMinor != fee || a.Currency != "TST" || a.FeeVersion != "1" || a.PolicyVersion != testPolicy {
		t.Fatalf("copied values: %+v", a)
	}
	// The doctor changes the fee after the hold: the booking keeps the old one.
	np := p
	np.FeeAmount, np.FeeVersion = fee*2, "2"
	f.dir.set(np)
	if _, err := f.m.Checkout(bg, patient, a.ID); err != nil {
		t.Fatal(err)
	}
	if len(f.pay.calls) != 1 || f.pay.calls[0].Amount != fee || f.pay.calls[0].Currency != "TST" || f.pay.calls[0].Clinic != p.ClinicRef {
		t.Fatalf("order must use the fee at hold time: %+v", f.pay.calls)
	}
	got, _ := f.m.Mine(bg, patient, a.ID)
	if got.FeeMinor != fee || got.FeeVersion != "1" {
		t.Fatalf("fee changed under the booking: %+v", got)
	}
	// The policy version is stored on the row (K-PAY-05) and a new booking gets the new fee.
	if n := f.count(t, `SELECT count(*) FROM booking.appointment WHERE policy_version=$1`, testPolicy); n != 1 {
		t.Fatal("policy version not stored")
	}
	b, _ := f.holdIn(np, ids.New(), f.openSlots(t, p, booking.ModeInPerson)[5])
	if b.FeeMinor != fee*2 || b.FeeVersion != "2" {
		t.Fatalf("new booking: %+v", b)
	}
	// A clinic with paused payments takes no new paid bookings.
	f.pay.mu.Lock()
	f.pay.paused[p.ClinicRef] = true
	f.pay.mu.Unlock()
	if _, err := f.holdIn(p, ids.New(), f.openSlots(t, p, booking.ModeInPerson)[6]); !isProblem(err, booking.ProblemPaymentsPaused) {
		t.Fatalf("paused: %v", err)
	}
}

func TestPayAtClinicConfirmsInPersonOnly(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	f.pay.mu.Lock()
	f.pay.mode[p.ClinicRef] = pp.PayAtClinic
	f.pay.mu.Unlock()
	patient := ids.New()
	a, err := f.holdIn(p, patient, f.openSlots(t, p, booking.ModeInPerson)[8])
	if err != nil || a.State != booking.StateConfirmed {
		t.Fatalf("pay at clinic in person: %+v %v", a, err)
	}
	if len(f.pay.calls) != 0 {
		t.Fatal("no order for a pay-at-clinic booking")
	}
	if ev := f.events(t, bp.EventConfirmed); len(ev) != 1 {
		t.Fatalf("confirmed: %v", ev)
	}
	if _, err := f.holdIn(p, patient, f.openSlots(t, p, booking.ModeVideo)[3]); !isProblem(err, booking.ProblemPayAtClinicOnline) {
		t.Fatalf("online with pay at clinic: %v", err)
	}
	if co, err := f.m.Checkout(bg, patient, a.ID); err != nil || co.CheckoutURL != "" || co.Appointment.State != booking.StateConfirmed {
		t.Fatalf("checkout of confirmed: %+v %v", co, err)
	}
	// Cancelling an unpaid-by-us booking previews no refund and emits cancelled (T1 revoke).
	prev, err := f.m.PreviewCancel(bg, patient, a.ID)
	if err != nil || prev.Paid || prev.RefundMinor != 0 || len(f.refund.calls) != 0 {
		t.Fatalf("preview: %+v %v", prev, err)
	}
}

func TestOwnershipOthersGet404(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	patient, intruder := ids.New(), ids.New()
	slots := f.openSlots(t, p, booking.ModeInPerson)
	a := f.paid(t, p, patient, slots[8])
	h, _ := f.holdIn(p, patient, slots[9])
	checks := map[string]error{}
	_, checks["mine"] = f.m.Mine(bg, intruder, a.ID)
	_, checks["checkout"] = f.m.Checkout(bg, intruder, h.ID)
	_, checks["preview"] = f.m.PreviewCancel(bg, intruder, a.ID)
	_, _, checks["cancel"] = f.m.Cancel(bg, intruder, a.ID, fee, "")
	_, checks["reschedule"] = f.m.Reschedule(bg, intruder, a.ID, slots[10].ID, "")
	_, _, checks["unknown"] = f.m.Cancel(bg, patient, ids.New(), 0, "")
	for name, err := range checks {
		if !isProblem(err, problem.NotFound) {
			t.Errorf("%s: want 404, got %v", name, err)
		}
	}
	if got, _ := f.m.Mine(bg, patient, a.ID); got.State != booking.StateConfirmed {
		t.Fatal("an intruder's cancel must change nothing")
	}
	if list, _, _ := f.m.ListMine(bg, intruder, 10, ""); len(list) != 0 {
		t.Fatal("intruder sees nothing")
	}
	if owner, found, err := f.m.PatientOf(bg, a.ID); err != nil || !found || owner != patient {
		t.Fatalf("PatientOf: %s %v %v", owner, found, err)
	}
	if _, found, _ := f.m.PatientOf(bg, ids.New()); found {
		t.Fatal("unknown appointment")
	}
}

// ---- S-144: cancel with the refund shown first; reschedule ----

func TestCancelShowsRefundBeforeConfirming(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	patient := ids.New()
	a := f.paid(t, p, patient, f.openSlots(t, p, booking.ModeVideo)[5])

	prev, err := f.m.PreviewCancel(bg, patient, a.ID)
	if err != nil || prev.RefundMinor != fee || prev.Rule != "RF07" || !prev.Paid || prev.ByWhom != "patient" {
		t.Fatalf("preview: %+v %v", prev, err)
	}
	if f.refund.calls[0] != "patient/video" {
		t.Fatalf("preview mode: %v", f.refund.calls)
	}
	// A cancel that does not carry the outcome the patient was shown is refused.
	if _, _, err := f.m.Cancel(bg, patient, a.ID, 0, ""); !isProblem(err, booking.ProblemRefundChanged) {
		t.Fatalf("wrong acknowledgement: %v", err)
	}
	if got, _ := f.m.Mine(bg, patient, a.ID); got.State != booking.StateConfirmed {
		t.Fatal("must stay confirmed")
	}
	// If the previewer is down, nothing is cancelled.
	f.refund.mu.Lock()
	f.refund.fail = true
	f.refund.mu.Unlock()
	if _, _, err := f.m.Cancel(bg, patient, a.ID, fee, ""); !isProblem(err, booking.ProblemPreviewDown) {
		t.Fatalf("previewer down: %v", err)
	}
	f.refund.mu.Lock()
	f.refund.fail = false
	f.refund.mu.Unlock()
	_, got, err := f.m.Cancel(bg, patient, a.ID, fee, "req1")
	if err != nil || got.State != booking.StateCancelled || got.CancelledBy != "patient" {
		t.Fatalf("cancel: %+v %v", got, err)
	}
	c := f.events(t, bp.EventCancelled)
	if len(c) != 1 {
		t.Fatalf("events: %v", c)
	}
	e := c[0]
	if e["byWhom"] != "patient" || e["mode"] != "online" || e["consultMode"] != "video" || e["policyVersion"] != testPolicy || e["minutesBeforeSlot"] == "" || e["patientRef"] != patient {
		t.Fatalf("payload: %v", e)
	}
	if _, has := e["rule"]; has {
		t.Fatal("a patient cancellation names no rule; payments applies the stored policy")
	}
	if n := f.count(t, `SELECT count(*) FROM booking.slot WHERE id=$1 AND state='open'`, a.SlotID); n != 1 {
		t.Fatal("slot must reopen")
	}
	if _, _, err := f.m.Cancel(bg, patient, a.ID, fee, ""); !isProblem(err, booking.ProblemCannotCancel) {
		t.Fatalf("second cancel: %v", err)
	}
}

func TestCancelLateAndUnpaid(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	patient := ids.New()
	a := f.paid(t, p, patient, f.openSlots(t, p, booking.ModeInPerson)[0]) // starts in 30 minutes
	prev, err := f.m.PreviewCancel(bg, patient, a.ID)
	if err != nil || prev.RefundMinor != 0 || prev.Rule != "RF08" {
		t.Fatalf("late preview: %+v %v", prev, err)
	}
	if _, _, err := f.m.Cancel(bg, patient, a.ID, 0, ""); err != nil {
		t.Fatal(err)
	}
	// Cancelling a pending_payment hold releases the slot and emits expired (a late capture is refunded).
	b, _ := f.holdIn(p, patient, f.openSlots(t, p, booking.ModeInPerson)[6])
	if _, err := f.m.Checkout(bg, patient, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, got, err := f.m.Cancel(bg, patient, b.ID, 0, ""); err != nil || got.State != booking.StateCancelled {
		t.Fatalf("cancel unpaid: %+v %v", got, err)
	}
	ev := f.events(t, bp.EventExpired)
	if len(ev) != 1 || ev[0]["appointmentRef"] != b.ID {
		t.Fatalf("expired: %v", ev)
	}
	// A capture arriving afterwards is not confirmed and is refunded through expired.
	f.capture(t, b.ID, f.pay.orders[b.ID])
	if got, _ := f.m.Mine(bg, patient, b.ID); got.State != booking.StateCancelled || len(f.events(t, bp.EventExpired)) != 2 {
		t.Fatal("late capture after cancel")
	}
}

func TestCancelByStaffIsFullRefundRule(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	a := f.paid(t, p, ids.New(), f.openSlots(t, p, booking.ModeInPerson)[7])
	if err := f.m.CancelByStaff(bg, deskActor(ids.New()), a.ID); !isProblem(err, problem.NotFound) {
		t.Fatalf("other clinic: %v", err)
	}
	if err := f.m.CancelByStaff(bg, docActor(p), a.ID); err != nil {
		t.Fatal(err)
	}
	c := f.events(t, bp.EventCancelled)
	if len(c) != 1 || c[0]["byWhom"] != "doctor" || c[0]["rule"] != "RF04" {
		t.Fatalf("events: %v", c)
	}
}

func TestRescheduleRules(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	patient := ids.New()
	slots := f.openSlots(t, p, booking.ModeInPerson)
	a := f.paid(t, p, patient, slots[8])
	// Different mode is not allowed: a video slot for an in-person booking.
	if _, err := f.m.Reschedule(bg, patient, a.ID, f.openSlots(t, p, booking.ModeVideo)[5].ID, ""); !isProblem(err, booking.ProblemModeNotOffered) {
		t.Fatalf("other mode: %v", err)
	}
	moved, err := f.m.Reschedule(bg, patient, a.ID, slots[12].ID, "r1")
	if err != nil || moved.RescheduleCount != 1 || !moved.StartsAt.Equal(slots[12].StartsAt) || moved.State != booking.StateConfirmed {
		t.Fatalf("reschedule: %+v %v", moved, err)
	}
	if moved.FeeMinor != a.FeeMinor || moved.PolicyVersion != a.PolicyVersion || moved.OrderRef != a.OrderRef {
		t.Fatal("payment and policy stay with the appointment")
	}
	if n := f.count(t, `SELECT count(*) FROM booking.slot WHERE id=$1 AND state='open'`, slots[8].ID); n != 1 {
		t.Fatal("old slot reopens")
	}
	if ev := f.events(t, bp.EventRescheduled); len(ev) != 1 || ev[0]["slotStart"] != slots[12].StartsAt.Format(time.RFC3339) {
		t.Fatalf("events: %v", ev)
	}
	if len(f.events(t, bp.EventCancelled)) != 0 {
		t.Fatal("a reschedule is not a cancellation: no refund")
	}
	// Only one free reschedule.
	if _, err := f.m.Reschedule(bg, patient, a.ID, slots[14].ID, ""); !isProblem(err, booking.ProblemRescheduleLimit) {
		t.Fatalf("second: %v", err)
	}
	// A taken slot is refused with next slots offered.
	other := f.paid(t, p, ids.New(), slots[20])
	_, err = f.m.Reschedule(bg, other.PatientRef, other.ID, slots[12].ID, "")
	var st *booking.SlotTakenError
	if !errors.As(err, &st) || len(st.Next) == 0 {
		t.Fatalf("taken slot: %v", err)
	}
	// Too close to the start.
	near := f.paid(t, p, ids.New(), slots[0])
	if _, err := f.m.Reschedule(bg, near.PatientRef, near.ID, slots[30].ID, ""); !isProblem(err, booking.ProblemCannotReschedule) {
		t.Fatalf("too close: %v", err)
	}
}

func TestEarliestSlotsImplementsDirectorySource(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	q := f.practice(t)
	empty := ids.New()
	got, err := f.m.EarliestSlots(bg, []string{p.ID, q.ID, empty, "not-a-uuid"}, t0)
	if err != nil || len(got) != 2 {
		t.Fatalf("earliest: %v %v", got, err)
	}
	first := f.openSlots(t, p, booking.ModeInPerson)[0]
	if !got[p.ID].Equal(first.StartsAt) {
		t.Fatalf("earliest %v want %v", got[p.ID], first.StartsAt)
	}
	// Taking that slot moves the earliest forward.
	if _, err := f.holdIn(p, ids.New(), first); err != nil {
		t.Fatal(err)
	}
	got, _ = f.m.EarliestSlots(bg, []string{p.ID}, t0)
	if !got[p.ID].After(first.StartsAt) {
		t.Fatalf("earliest after hold: %v", got)
	}
	var _ bp.Service = f.m
}

func dpUnlisted(p dp.Practice) dp.Practice { p.Listed = false; return p }

// E-SEC-04: pay-at-clinic bookings confirm at once, so they are capped separately
// (default 3 upcoming per patient) to stop slot squatting without paying.
func TestESEC04PayAtClinicCap(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	f.pay.mu.Lock()
	f.pay.mode[p.ClinicRef] = pp.PayAtClinic
	f.pay.mu.Unlock()
	patient := ids.New()
	slots := f.openSlots(t, p, booking.ModeInPerson)
	for i := 0; i < 3; i++ {
		if _, err := f.holdIn(p, patient, slots[10+i]); err != nil {
			t.Fatalf("booking %d: %v", i, err)
		}
	}
	if _, err := f.holdIn(p, patient, slots[14]); !isProblem(err, booking.ProblemTooManyHolds) {
		t.Fatalf("fourth upcoming pay-at-clinic booking: %v", err)
	}
}

// ---- review H3: the licence is checked at the source, not only in the read model ----

func TestReviewH3HoldRefusedWhenVerifierSaysNo(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	slots := f.openSlots(t, p, booking.ModeInPerson)
	f.ver.bad[p.ProfessionalRef] = true // directory still lists the practice
	if _, err := f.holdIn(p, ids.New(), slots[0]); !isProblem(err, booking.ProblemPracticeGone) {
		t.Fatalf("hold: %v", err)
	}
	f.ver.bad = map[string]bool{}
	f.ver.err = context.DeadlineExceeded
	if _, err := f.holdIn(p, ids.New(), slots[0]); !isProblem(err, booking.ProblemPracticeGone) {
		t.Fatalf("hold on verifier error must fail closed: %v", err)
	}
}

func TestReviewH3RescheduleRefusedWhenVerifierSaysNo(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	slots := f.openSlots(t, p, booking.ModeInPerson)
	patient := ids.New()
	a := f.paid(t, p, patient, slots[6])
	f.ver.bad[p.ProfessionalRef] = true
	if _, err := f.m.Reschedule(bg, patient, a.ID, slots[8].ID, ""); !isProblem(err, booking.ProblemPracticeGone) {
		t.Fatalf("reschedule: %v", err)
	}
}

func TestReviewH3CaptureCancelsWhenVerifierSaysNo(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	slots := f.openSlots(t, p, booking.ModeInPerson)
	patient := ids.New()
	b, _ := f.holdIn(p, patient, slots[6])
	co, err := f.m.Checkout(bg, patient, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.ver.bad[p.ProfessionalRef] = true
	f.capture(t, b.ID, co.OrderRef)
	got, _ := f.m.Mine(bg, patient, b.ID)
	if got.State != booking.StateCancelled || got.CancelledBy != "system" {
		t.Fatalf("%+v", got)
	}
	c := f.events(t, bp.EventCancelled)
	if len(c) != 1 || c[0]["rule"] != "RF04" || c[0]["byWhom"] != "system" || len(f.events(t, bp.EventConfirmed)) != 0 {
		t.Fatalf("events: %v", c)
	}
}
