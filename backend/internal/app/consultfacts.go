package app

import (
	"context"
	"errors"
	"strings"

	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical/rules"
	cp "gitlab.com/viyallabs/mugizh/backend/internal/modules/consult/public"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	vp "gitlab.com/viyallabs/mugizh/backend/internal/modules/verification/public"
)

// consultFacts composes clinical.ConsultFacts from the modules that own each fact
// (replaces the earlier fail-closed stub). Every gap fails closed, never open:
//
//   - booking: refs and mode; only confirmed, in-progress or completed appointments exist
//     for prescribing;
//   - consult (remote only): mode at signing (E-RX-06), consult time, the "in India"
//     declaration (P10), consent (P04) and identity confirmation (P02). Identity counts
//     only when the consult actually took place: both sides connected and the call is live
//     or ended "completed", no minor was flagged (TC-48), and the patient has not changed
//     their name, date of birth or sex since the doctor confirmed them (review F3);
//   - in person: no identity confirmation step exists yet, so P02 blocks (D-39);
//   - identity: name, age and sex. A missing or unconfirmed profile leaves them empty, so
//     P03 blocks the check and signing refuses (drafting still works);
//   - verification: the reviewed registration snapshot. Missing means signing refuses;
//   - patient time zone: the practice's zone (D-40);
//   - follow-up and prior drugs: not supplied yet, so every consult is a first consult with
//     no refills (the stricter reading, D-41).
//
// The profile is read inside the server only; clinical runs records.Authorize (and audits)
// before any of it reaches the doctor (K-DP-02).
type consultFacts struct {
	booking     bp.Service
	sessions    cp.FactsReader
	profiles    ip.ProfileReader
	changes     ip.ProfileChanges
	prescribers vp.PrescriberReader
	practices   dp.Reader
}

func (a consultFacts) Consult(ctx context.Context, appt string) (clinical.ConsultFacts, error) {
	info, err := a.booking.Get(ctx, appt)
	if errors.Is(err, bp.ErrNotFound) {
		return clinical.ConsultFacts{}, clinical.ErrConsultNotFound
	}
	if err != nil {
		return clinical.ConsultFacts{}, err
	}
	switch info.State {
	case "confirmed", "in_progress", "completed":
	default:
		return clinical.ConsultFacts{}, clinical.ErrConsultNotFound
	}
	f := clinical.ConsultFacts{AppointmentRef: info.ID, PatientRef: info.PatientRef, DoctorRef: info.ProfessionalRef, ClinicRef: info.ClinicRef}

	switch info.Mode {
	case cp.ModeInPerson:
		f.Mode, f.ConsultAt = rules.ModeInPerson, info.StartsAt
	case cp.ModeVideo, cp.ModeAudio:
		s, err := a.sessions.SessionFacts(ctx, appt)
		if errors.Is(err, cp.ErrNotFound) {
			return clinical.ConsultFacts{}, clinical.ErrConsultNotFound
		}
		if err != nil {
			return clinical.ConsultFacts{}, err
		}
		if s.State == "cancelled" {
			return clinical.ConsultFacts{}, clinical.ErrConsultNotFound
		}
		switch s.ModeAtSigning {
		case cp.ModeVideo:
			f.Mode = rules.ModeVideo
		case cp.ModeAudio:
			f.Mode = rules.ModeAudio
		case cp.ModeInPerson:
			f.Mode = rules.ModeInPerson
		default:
			return clinical.ConsultFacts{}, errors.New("consult facts: unknown consult mode")
		}
		f.ConsultAt, f.PatientCountry = s.ConsultAt, s.PatientCountry
		f.Initiator, f.ExplicitConsent = s.Initiator, s.ExplicitConsent
		happened := s.BothConnected && (s.State == "both_connected" || (s.State == "ended" && s.Outcome == cp.OutcomeCompleted))
		f.IdentityConfirmed = s.IdentityConfirmed && !s.MinorFlagged && happened
		// Review F3: a change of the patient's name, date of birth or sex after the confirmation
		// voids it (an unknown answer counts as a change).
		if f.IdentityConfirmed {
			changed, err := a.changes.ProfileIdentityChangedAt(ctx, info.PatientRef)
			if err != nil || s.IdentityConfirmedAt.IsZero() || changed.After(s.IdentityConfirmedAt) {
				f.IdentityConfirmed = false
			}
		}
	default:
		return clinical.ConsultFacts{}, errors.New("consult facts: unknown appointment mode")
	}

	if p, err := a.practices.GetPractice(ctx, info.PracticeRef); err == nil {
		f.PatientTimeZone = p.TimeZone
	}
	if name, age, sex, err := a.profiles.ProfileFacts(ctx, info.PatientRef); err == nil {
		f.PatientName, f.PatientAgeYears, f.PatientSex = name, age, sex
	}
	if pf, err := a.prescribers.PrescriberFacts(ctx, info.ProfessionalRef); err == nil {
		f.Doctor = clinical.DoctorFacts{Name: pf.RegisteredName, Qualification: strings.Join(pf.Qualifications, ", "),
			RegistrationNumber: pf.RegistrationNumber, Council: pf.CouncilCode}
	}
	return f, nil
}
