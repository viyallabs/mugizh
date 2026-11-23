// Package public is the verification module's interface (S-109, S-118, K-CS-01).
//
// Directory, booking and clinical depend on it: only Verified professionals are listed,
// booked or can prescribe, and this is checked on the server through Reader on every such
// action. The answer fails closed: suspended, expired, rejected, unknown or lapsed
// (re-verification grace elapsed) all mean not verified.
package public

import (
	"context"
	"errors"
)

// Status is the standing of a professional's verification case.
type Status string

// Statuses (module-boundaries, verification). StatusNone means no case exists.
const (
	StatusNone        Status = "none"
	StatusDraft       Status = "draft"
	StatusSubmitted   Status = "submitted"
	StatusChecking    Status = "checking"
	StatusNeedsInfo   Status = "needs_info"
	StatusVerified    Status = "verified"
	StatusRejected    Status = "rejected"
	StatusSuspended   Status = "suspended"
	StatusExpired     Status = "expired"
	StatusReVerifyDue Status = "re_verify_due"
)

// Event names and payload keys. The event carries references and codes only.
const (
	EventStatusChanged  = "verification.status_changed"
	PayloadProfessional = "professionalRef"
	PayloadStatus       = "status"
	PayloadSubjectKind  = "subjectKind"
)

// ClinicReader is the clinic (facility) side of the gate: a clinic's standing is read the
// same way. Fails closed like Reader.
type ClinicReader interface {
	ClinicStatus(ctx context.Context, clinicRef string) (Status, error)
}

// Subject kinds carried in the status event (PayloadSubjectKind).
const (
	SubjectDoctor = "doctor"
	SubjectClinic = "clinic"
)

// Reader is what other modules call. Keep it small and stable.
type Reader interface {
	// Status returns the effective status. A professional whose licence has expired or
	// whose re-verification grace has run out is reported as expired or suspended even if
	// the periodic job has not yet moved the case.
	Status(ctx context.Context, professionalRef string) (Status, error)
	// IsVerified is the prescribing and listing gate. It returns (false, err) on any
	// error: callers must treat an error as not verified.
	IsVerified(ctx context.Context, professionalRef string) (bool, error)
}

// PrescriberFacts are the reviewed registration details printed on a prescription: the
// values approved at the last final "verified" decision, never an unreviewed draft.
type PrescriberFacts struct {
	RegisteredName     string
	Qualifications     []string
	CouncilCode        string
	RegistrationNumber string
}

// ErrNotVerified is returned by PrescriberFacts for anyone who is not verified now.
var ErrNotVerified = errors.New("verification: professional is not verified")

// PrescriberReader returns PrescriberFacts for a professional who is verified now
// (effective status, as IsVerified). Anything else is ErrNotVerified or an error.
type PrescriberReader interface {
	PrescriberFacts(ctx context.Context, professionalRef string) (PrescriberFacts, error)
}
