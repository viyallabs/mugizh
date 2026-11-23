package booking_test

import (
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/booking"
	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

func slotAfter(t *testing.T, f *fx, p dp.Practice, mode string, after time.Time) booking.SlotInfo {
	t.Helper()
	for _, s := range f.openSlots(t, p, mode) {
		if s.StartsAt.After(after) {
			return s
		}
	}
	t.Fatalf("no %s slot after %s", mode, after)
	return booking.SlotInfo{}
}

func (f *fx) reminderKinds(t *testing.T, appt string) []string {
	t.Helper()
	var out []string
	for _, e := range f.events(t, bp.EventReminderDue) {
		if e["appointmentRef"] == appt {
			out = append(out, e["kind"])
		}
	}
	return out
}

// S-157: the day-before and short reminders, and for online consults the "starting" notice,
// each fire once at their point; a late booking gets no immediate "reminder"; a reschedule
// re-arms them.
func TestS157AppointmentReminders(t *testing.T) {
	f := setup(t)
	p := f.practice(t)
	video := f.paid(t, p, ids.New(), slotAfter(t, f, p, booking.ModeVideo, t0.Add(30*time.Hour)))

	run := func(at time.Time) int {
		f.setClock(at)
		n, err := f.m.EmitDueReminders(bg, at)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := run(video.StartsAt.Add(-25 * time.Hour)); n != 0 {
		t.Fatalf("too early: %d", n)
	}
	run(video.StartsAt.Add(-24 * time.Hour))
	run(video.StartsAt.Add(-23 * time.Hour)) // a second run sends nothing new
	if got := f.reminderKinds(t, video.ID); len(got) != 1 || got[0] != bp.ReminderDayBefore {
		t.Fatalf("day before: %v", got)
	}
	run(video.StartsAt.Add(-2 * time.Hour))
	run(video.StartsAt.Add(-10 * time.Minute))
	run(video.StartsAt.Add(-5 * time.Minute))
	got := f.reminderKinds(t, video.ID)
	if len(got) != 3 || got[1] != bp.ReminderSoon || got[2] != bp.ReminderConsultStart {
		t.Fatalf("all three once, in order: %v", got)
	}
	if e := f.events(t, bp.EventReminderDue)[0]; e["patientRef"] != video.PatientRef || e["doctorRef"] != p.ProfessionalRef || e["slotStart"] == "" || e["mode"] != booking.ModeVideo {
		t.Fatalf("payload: %v", e)
	}

	// In person: no "starting" notice. Booked 90 minutes ahead: no reminders at all.
	f.setClock(t0)
	inPerson := f.paid(t, p, ids.New(), slotAfter(t, f, p, booking.ModeInPerson, t0.Add(30*time.Hour)))
	run(inPerson.StartsAt.Add(-24 * time.Hour))
	run(inPerson.StartsAt.Add(-2 * time.Hour))
	run(inPerson.StartsAt.Add(-10 * time.Minute))
	if got := f.reminderKinds(t, inPerson.ID); len(got) != 2 {
		t.Fatalf("in person: %v", got)
	}
	late := slotAfter(t, f, p, booking.ModeInPerson, inPerson.StartsAt)
	f.setClock(late.StartsAt.Add(-90 * time.Minute))
	lateAppt := f.paid(t, p, ids.New(), late)
	run(late.StartsAt.Add(-80 * time.Minute))
	run(late.StartsAt.Add(-10 * time.Minute))
	if got := f.reminderKinds(t, lateAppt.ID); len(got) != 0 {
		t.Fatalf("a booking made after the reminder points gets none: %v", got)
	}

	// A reschedule re-arms the reminders for the new time.
	f.setClock(t0)
	patient := ids.New()
	moving := f.paid(t, p, patient, slotAfter(t, f, p, booking.ModeInPerson, t0.Add(30*time.Hour)))
	run(moving.StartsAt.Add(-24 * time.Hour))
	f.setClock(moving.StartsAt.Add(-23 * time.Hour))
	target := slotAfter(t, f, p, booking.ModeInPerson, moving.StartsAt.Add(48*time.Hour))
	moved, err := f.m.Reschedule(bg, patient, moving.ID, target.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	run(moved.StartsAt.Add(-24 * time.Hour))
	if got := f.reminderKinds(t, moving.ID); len(got) != 2 || got[0] != bp.ReminderDayBefore || got[1] != bp.ReminderDayBefore {
		t.Fatalf("re-armed after a reschedule: %v", got)
	}
}
