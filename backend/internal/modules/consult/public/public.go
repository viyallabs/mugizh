// Package public is the consult module's interface (module-boundaries section 2). Clinical,
// booking and the composition root use only this package.
//
// Event payloads carry references and codes only (module-boundaries section 4).
package public

import (
	"context"
	"errors"
	"time"
)

// Event types consult emits.
const (
	EventBothConnected = "consult.both_connected" // {sessionRef, appointmentRef}
	EventEnded         = "consult.ended"          // {sessionRef, appointmentRef, outcome, endedAt, [flag]}
	EventMinorFlagged  = "consult.minor_flagged"  // {sessionRef, appointmentRef, patientRef}: the account is handled under CH-12
)

// Subscriber names for the outbox bus (one per handler, unique).
const (
	SubscriberAppointmentConfirmed   = "consult.on_appointment_confirmed"
	SubscriberAppointmentRescheduled = "consult.on_appointment_rescheduled"
	SubscriberAppointmentCancelled   = "consult.on_appointment_cancelled"
)

// Outcomes carried in consult.ended.
const (
	OutcomeCompleted         = "completed"
	OutcomeConvertedInPerson = "converted_in_person"
	OutcomePatientNoShow     = "patient_no_show"
	OutcomeDoctorNoShow      = "doctor_no_show"
	OutcomeTechnicalFailure  = "technical_failure"
	OutcomeMinorStopped      = "minor_stopped"
)

// Payload keys, flag and mode codes.
const (
	FlagShortCall          = "short_call" // consult.ended flag: completed in under the minimum duration
	PayloadSessionRef      = "sessionRef"
	PayloadAppointmentRef  = "appointmentRef"
	PayloadOutcome         = "outcome"
	PayloadEndedAt         = "endedAt"
	PayloadFlag            = "flag"
	PayloadPatientRef      = "patientRef"
	ModeVideo              = "video"
	ModeAudio              = "audio"
	ModeInPerson           = "in_person"
	ConsentTextTeleconsult = "TELE-CONSENT"
)

// ErrNotFound is returned when an appointment has no consult session (for example an
// in-person appointment, which has none).
var ErrNotFound = errors.New("consult: no session for this appointment")

// SessionFacts are the facts only consult owns, for clinical's ConsultFacts (P02, P10,
// E-RX-06). Opus composes them with booking (refs, zone), identity (ProfileFacts) and
// directory or verification (doctor facts).
type SessionFacts struct {
	AppointmentRef string
	SessionRef     string
	State          string // scheduled, open, both_connected, ended, failed_technical, no_show, cancelled
	Outcome        string // empty until ended
	BookedMode     string // video or audio
	// ModeAtSigning is the mode now: a video call that dropped to audio is audio, a call
	// converted to in-person is in_person (E-RX-06).
	ModeAtSigning string
	// ConsultAt is when the consultation began: both sides connected, else the slot start.
	ConsultAt     time.Time
	BothConnected bool
	// PatientInIndia is the patient's "I am in India now" declaration for this consult (P10).
	// PatientCountry is the country code from the pack that declaration stands for, or empty.
	PatientInIndia bool
	PatientCountry string
	// IdentityConfirmed: the doctor confirmed the patient's name and age (P02).
	IdentityConfirmed bool
	// IdentityConfirmedAt is when the doctor last confirmed it (zero if never). A profile change
	// of name, date of birth or sex after this time voids the confirmation (review F3).
	IdentityConfirmedAt time.Time
	// Initiator is who started the consult (patient in v1.0: patients book and start).
	Initiator string
	// ExplicitConsent is true when the teleconsultation consent was recorded for this consult.
	ExplicitConsent bool
	// MinorFlagged is true when the doctor found the patient to be a minor (TC-48).
	MinorFlagged bool
}

// FactsReader is what clinical's adapter calls.
type FactsReader interface {
	// SessionFacts returns the facts, or ErrNotFound when the appointment has no session.
	SessionFacts(ctx context.Context, appointmentRef string) (SessionFacts, error)
}
