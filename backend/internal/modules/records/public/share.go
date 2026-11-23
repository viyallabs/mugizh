package public

import (
	"context"
	"errors"
	"time"
)

// History sharing (S-168, S-169; consent-design §2, §7). A patient shares their history with one
// doctor for one consultation (tier T3). The doctor may ask for more time (tier T4), which
// only the patient's approval turns into access.

// Errors of the sharing flows.
var (
	// ErrDoctorNotVerified: the doctor is not Verified now (K-CS-01). Fail closed on any lookup error.
	ErrDoctorNotVerified = errors.New("records: the doctor is not verified")
	// ErrNoAppointment: the appointment is not the patient's confirmed appointment with this doctor.
	ErrNoAppointment = errors.New("records: no confirmed appointment with this doctor")
	// ErrExtensionNotAllowed: nothing to extend (no share, the patient stopped it, or it is not
	// close to ending yet).
	ErrExtensionNotAllowed = errors.New("records: this share cannot be extended now")
	// ErrRequestClosed: the extension request was already decided or has lapsed.
	ErrRequestClosed = errors.New("records: the extension request is closed")
)

// AppointmentFacts is what sharing needs to know about an appointment.
type AppointmentFacts struct {
	ClinicRef string
	// EndsAt is the end of the booked slot (UTC). The default share ends a configured time after it.
	EndsAt time.Time
}

// AppointmentReader is implemented at wiring time over the booking module's public interface.
// It must return ErrNoAppointment unless the appointment is CONFIRMED (not cancelled, not
// expired), belongs to this patient and is with this doctor.
type AppointmentReader interface {
	ConfirmedAppointment(ctx context.Context, patientRef, doctorRef, appointmentRef string) (AppointmentFacts, error)
}

// DoctorVerifier is satisfied by verification's public Reader.
type DoctorVerifier interface {
	IsVerified(ctx context.Context, professionalRef string) (bool, error)
}

// ShareInput is the patient's tap on the P-SHARE screen.
type ShareInput struct {
	DoctorRef      string
	AppointmentRef string
	// Until is a shorter expiry the patient chose. Nil means the default (a configured time after
	// the appointment ends). It may never be later than the default.
	Until *time.Time
	// TextVersion is the P-SHARE text version shown; it must be the version in force.
	TextVersion int
	// NoticeVersion is optional; the notice version in force from configuration is used when empty.
	NoticeVersion string
	RequestID     string
}

// ShareResult is the created (or already active) share.
type ShareResult struct {
	GrantID   string
	DoctorRef string
	ExpiresAt time.Time
}

// ExtensionRequest is a doctor's request for more time.
type ExtensionRequest struct {
	ID          string
	PatientRef  string
	DoctorRef   string
	ClinicRef   string
	Status      string // pending, approved, declined, expired
	RequestedAt time.Time
	// ExpiresAt is when the request lapses if the patient does not answer.
	ExpiresAt time.Time
	// ExtensionMax is the longest extra time the patient can allow.
	ExtensionMax time.Duration
}

// ApproveInput is the patient's answer "Allow".
type ApproveInput struct {
	// TextVersion is the P-SHARE text version shown; it must be the version in force.
	TextVersion   int
	NoticeVersion string
	// For is a shorter extension the patient chose; zero means the configured maximum.
	For       time.Duration
	RequestID string
}

// ShareWindowReader answers how long a doctor may read a patient's shared history. It is
// implemented by the records module; clinical's history view uses it for the expiry warning.
type ShareWindowReader interface {
	// HistoryAccessUntil returns the latest end of the doctor's active T3/T4 grants for the
	// patient. ok is false when there is none.
	HistoryAccessUntil(ctx context.Context, patientRef, doctorRef string) (until time.Time, ok bool, err error)
}
