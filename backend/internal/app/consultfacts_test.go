package app

import (
	"context"
	"errors"
	"testing"
	"time"

	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical/rules"
	cp "gitlab.com/viyallabs/mugizh/backend/internal/modules/consult/public"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	vp "gitlab.com/viyallabs/mugizh/backend/internal/modules/verification/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// Synthetic test values only (K-GLB-01: no country constants in shipped code).
const testZone, testCountry = "Etc/GMT-3", "ZZ"

type cfBooking struct {
	bp.Service
	info bp.Info
	err  error
}

func (b cfBooking) Get(context.Context, string) (bp.Info, error) { return b.info, b.err }

type cfSessions struct {
	f   cp.SessionFacts
	err error
}

func (s cfSessions) SessionFacts(context.Context, string) (cp.SessionFacts, error) { return s.f, s.err }

type cfProfiles struct {
	err       error
	changedAt time.Time // when name, date of birth or sex last changed
	changeErr error
}

func (p cfProfiles) ProfileIdentityChangedAt(context.Context, string) (time.Time, error) {
	return p.changedAt, p.changeErr
}

func (p cfProfiles) ProfileFacts(context.Context, string) (string, int, string, error) {
	if p.err != nil {
		return "", 0, "", p.err
	}
	return "Test Patient", 34, "f", nil
}

type cfPrescribers struct{ err error }

func (p cfPrescribers) PrescriberFacts(context.Context, string) (vp.PrescriberFacts, error) {
	if p.err != nil {
		return vp.PrescriberFacts{}, p.err
	}
	return vp.PrescriberFacts{RegisteredName: "Dr Test", Qualifications: []string{"MBBS", "MD"}, CouncilCode: "TC", RegistrationNumber: "T/1"}, nil
}

type cfPractices struct{ err error }

func (p cfPractices) GetPractice(context.Context, string) (dp.Practice, error) {
	if p.err != nil {
		return dp.Practice{}, p.err
	}
	return dp.Practice{TimeZone: testZone}, nil
}

// TC-44 support: the facts the rules engine sees at drafting and signing. Every gap must
// fail closed (block), never open.
func TestConsultFactsFailClosed(t *testing.T) {
	ctx := context.Background()
	appt := ids.New()
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	connected := start.Add(3 * time.Minute)
	info := func(mode, state string) bp.Info {
		return bp.Info{ID: appt, PatientRef: ids.New(), ProfessionalRef: ids.New(), ClinicRef: ids.New(), PracticeRef: ids.New(),
			Mode: mode, StartsAt: start, EndsAt: start.Add(30 * time.Minute), State: state}
	}
	live := cp.SessionFacts{AppointmentRef: appt, State: "both_connected", ModeAtSigning: cp.ModeVideo, ConsultAt: connected, BothConnected: true,
		PatientInIndia: true, PatientCountry: testCountry, IdentityConfirmed: true, IdentityConfirmedAt: connected.Add(time.Minute), Initiator: "patient", ExplicitConsent: true}
	build := func(i bp.Info, s cfSessions) consultFacts {
		return consultFacts{booking: cfBooking{info: i}, sessions: s, profiles: cfProfiles{}, changes: cfProfiles{changedAt: start.Add(-24 * time.Hour)},
			prescribers: cfPrescribers{}, practices: cfPractices{}}
	}

	// A live video consult with everything recorded: every fact present.
	f, err := build(info("video", "in_progress"), cfSessions{f: live}).Consult(ctx, appt)
	if err != nil || f.Mode != rules.ModeVideo || !f.IdentityConfirmed || f.PatientCountry != testCountry || !f.ExplicitConsent ||
		!f.ConsultAt.Equal(connected) || f.PatientTimeZone != testZone || f.PatientAgeYears != 34 || f.Doctor.RegistrationNumber != "T/1" ||
		f.Doctor.Qualification != "MBBS, MD" {
		t.Fatalf("live consult: %+v %v", f, err)
	}

	// Not prescribable: unknown, cancelled, expired or held appointments look missing.
	for _, st := range []string{"cancelled", "expired", "held", "pending_payment", "no_show"} {
		if _, err := build(info("video", st), cfSessions{f: live}).Consult(ctx, appt); !errors.Is(err, clinical.ErrConsultNotFound) {
			t.Fatalf("%s: %v", st, err)
		}
	}
	nf := consultFacts{booking: cfBooking{err: bp.ErrNotFound}}
	if _, err := nf.Consult(ctx, appt); !errors.Is(err, clinical.ErrConsultNotFound) {
		t.Fatalf("unknown appointment: %v", err)
	}
	if _, err := build(info("video", "confirmed"), cfSessions{err: cp.ErrNotFound}).Consult(ctx, appt); !errors.Is(err, clinical.ErrConsultNotFound) {
		t.Fatalf("online appointment without a session: %v", err)
	}
	cancelled := live
	cancelled.State = "cancelled"
	if _, err := build(info("video", "confirmed"), cfSessions{f: cancelled}).Consult(ctx, appt); !errors.Is(err, clinical.ErrConsultNotFound) {
		t.Fatalf("cancelled session: %v", err)
	}

	// Identity (P02) only counts when the consult took place and no minor was flagged.
	for name, mut := range map[string]func(*cp.SessionFacts){
		"never connected":    func(s *cp.SessionFacts) { s.BothConnected, s.State = false, "open" },
		"patient no-show":    func(s *cp.SessionFacts) { s.State, s.Outcome = "ended", cp.OutcomePatientNoShow },
		"technical failure":  func(s *cp.SessionFacts) { s.State, s.Outcome = "ended", cp.OutcomeTechnicalFailure },
		"converted":          func(s *cp.SessionFacts) { s.State, s.Outcome = "ended", cp.OutcomeConvertedInPerson },
		"minor flagged":      func(s *cp.SessionFacts) { s.MinorFlagged = true },
		"identity not given": func(s *cp.SessionFacts) { s.IdentityConfirmed = false },
	} {
		s := live
		mut(&s)
		f, err := build(info("video", "in_progress"), cfSessions{f: s}).Consult(ctx, appt)
		if err != nil || f.IdentityConfirmed {
			t.Fatalf("%s: identity must not count: %+v %v", name, f, err)
		}
	}
	// Review F3: a name, date-of-birth or sex change after the confirmation voids it; so does
	// an unknown answer or a confirmation without a time.
	for name, ch := range map[string]cfProfiles{
		"changed after":  {changedAt: connected.Add(2 * time.Minute)},
		"unknown answer": {changeErr: errors.New("db down")},
	} {
		c := build(info("video", "in_progress"), cfSessions{f: live})
		c.changes = ch
		if f, err := c.Consult(ctx, appt); err != nil || f.IdentityConfirmed {
			t.Fatalf("%s: identity must not count: %+v %v", name, f, err)
		}
	}
	noTime := live
	noTime.IdentityConfirmedAt = time.Time{}
	if f, _ := build(info("video", "in_progress"), cfSessions{f: noTime}).Consult(ctx, appt); f.IdentityConfirmed {
		t.Fatal("a confirmation without its time must not count")
	}
	ended := live
	ended.State, ended.Outcome = "ended", cp.OutcomeCompleted
	if f, _ := build(info("video", "completed"), cfSessions{f: ended}).Consult(ctx, appt); !f.IdentityConfirmed {
		t.Fatal("a completed consult keeps its identity confirmation")
	}

	// Mode at signing: a video call that dropped to audio is audio (E-RX-06).
	audio := live
	audio.ModeAtSigning = cp.ModeAudio
	if f, _ := build(info("video", "in_progress"), cfSessions{f: audio}).Consult(ctx, appt); f.Mode != rules.ModeAudio {
		t.Fatalf("dropped to audio: %s", f.Mode)
	}
	odd := live
	odd.ModeAtSigning = "text"
	if _, err := build(info("video", "in_progress"), cfSessions{f: odd}).Consult(ctx, appt); err == nil {
		t.Fatal("an unknown mode must be an error")
	}

	// No declaration: no country, so P10 blocks.
	abroad := live
	abroad.PatientInIndia, abroad.PatientCountry = false, ""
	if f, _ := build(info("video", "in_progress"), cfSessions{f: abroad}).Consult(ctx, appt); f.PatientCountry != "" {
		t.Fatal("no declaration, no country")
	}

	// In person: no identity step yet (D-39), so P02 blocks; the slot start is the consult time.
	f, err = build(info("in_person", "in_progress"), cfSessions{err: errors.New("must not be called")}).Consult(ctx, appt)
	if err != nil || f.Mode != rules.ModeInPerson || f.IdentityConfirmed || !f.ConsultAt.Equal(start) {
		t.Fatalf("in person: %+v %v", f, err)
	}

	// Missing profile, prescriber snapshot or practice: the fields stay empty, and the rules
	// (P03) and signing refuse; drafting still works.
	gaps := consultFacts{booking: cfBooking{info: info("video", "in_progress")}, sessions: cfSessions{f: live},
		profiles: cfProfiles{err: errors.New("no profile")}, changes: cfProfiles{changeErr: errors.New("no profile")}, prescribers: cfPrescribers{err: vp.ErrNotVerified}, practices: cfPractices{err: dp.ErrNotFound}}
	f, err = gaps.Consult(ctx, appt)
	if err != nil || f.PatientName != "" || f.PatientAgeYears != 0 || f.Doctor.Name != "" || f.PatientTimeZone != "" {
		t.Fatalf("gaps must stay empty: %+v %v", f, err)
	}
}
