// Package public is the records module's interface: purpose consent, access permissions
// (tiers T1 to T4 and staff just-in-time grants) and the single enforcement gate
// records.authorize (S-097, K-DP-02, consent-design §3, access-control-design §3).
//
// Every read of a patient's data by anyone other than the patient goes through
// Authorize, from HTTP handlers and from workers alike. There is no bypass flag.
package public

import (
	"context"
	"errors"
	"time"
)

// Tier of an access permission (consent-texts A2) or a staff grant (K-SEC-08).
type Tier string

const (
	T1CurrentCare  Tier = "T1_current_care"
	T2OwnRecords   Tier = "T2_own_records"
	T3HistoryShare Tier = "T3_history_share"
	T4Extension    Tier = "T4_extension"
	JITStaff       Tier = "jit_staff"
	BreakGlass     Tier = "break_glass"
	Self           Tier = "self" // the patient reading their own data (no grant, no audit)
)

// DataKind is the kind of patient data a read touches.
type DataKind string

const (
	KindBooking         DataKind = "booking"          // booking fields: name, time, mode, status
	KindIdentityBasic   DataKind = "identity_basic"   // name, age, sex, phone for the consult
	KindClinicalCurrent DataKind = "clinical_current" // notes and prescription of the current consult
	KindClinicalOwn     DataKind = "clinical_own"     // records authored by this doctor or clinic
	KindClinicalHistory DataKind = "clinical_history" // other doctors' records
	KindDocuments       DataKind = "documents"        // the patient's uploads
	KindPayment         DataKind = "payment"          // payment and refund records
)

// Actor is who is asking. Built by the identity module from the authenticated session.
type Actor struct {
	Ref       string // user ID
	Role      string // role used for this action: patient, doctor, clinic_admin, front_desk, support, ...
	ClinicRef string // clinic scope of the role, for clinic roles and doctors
	// Restricted is true for a session on a not yet re-verified device (E-DP-22). The gate
	// refuses every read for it, including the patient's own (review L8).
	Restricted bool
}

// Request describes one read.
type Request struct {
	Actor      Actor
	PatientRef string
	Kind       DataKind
	// BookingRef scopes T1 (the appointment the read belongs to). Required for T1 kinds.
	BookingRef string
	// OwnerRef is the doctor or clinic that authored the record (for T2 checks).
	OwnerRef string
	// Purpose, if set, is a purpose consent that must also be granted (e.g. "P-REMIND").
	Purpose      string
	ResourceType string
	ResourceRef  string
	RequestID    string
	Client       string
	IPHash       string
}

// Decision is the gate's answer.
type Decision struct {
	Allowed bool
	Tier    Tier
	GrantID string
	// Reason is a code when denied: role_not_allowed, no_relationship, no_active_grant,
	// grant_expired, out_of_scope, purpose_not_granted.
	Reason string
	// Hide is true when the actor has no relationship with the patient at all, so the
	// API must answer 404 rather than 403 (K-SEC-04, E-SEC-03).
	Hide bool
}

// ErrNeededConsent is returned when someone tries to withdraw a needed purpose; that is
// done by closing the account (consent-design §5).
var ErrNeededConsent = errors.New("records: needed consent can only be withdrawn by closing the account")

// ErrSelfApproval is returned when a staff member approves their own access (K-SEC-08).
var ErrSelfApproval = errors.New("records: approver must not be the requester")

// ErrNotRevocable is returned when the patient tries to revoke access the law requires
// (T1 current care, T2 treating doctor records).
var ErrNotRevocable = errors.New("records: this access is required by law and cannot be revoked")

// ErrInvalid is returned for malformed input.
var ErrInvalid = errors.New("records: invalid input")

// ConsentChange records a grant or withdrawal of a purpose.
type ConsentChange struct {
	SubjectRef    string
	GivenByRef    string // equal to SubjectRef for adults
	PurposeID     string // e.g. "P-CARE"
	TextVersion   int    // must equal the version in force (country pack)
	NoticeVersion string // e.g. "PN-1.0"
	Channel       string // app, web, paper
	RequestID     string
	// ActorRole is the acting role written to the audit event; empty means "patient" (review F5:
	// a doctor's D-VERIFY consent is recorded as the doctor's act).
	ActorRole string
}

// ConsentState is the current value of one purpose.
type ConsentState struct {
	PurposeID   string
	Granted     bool
	TextVersion int
	UpdatedAt   time.Time
}

// Grant is an access permission (IDs, tiers, scopes and times only; never health data).
type Grant struct {
	ID         string
	PatientRef string
	GranteeRef string
	ClinicRef  string
	Tier       Tier
	ScopeRef   string     // T1: booking ID; JIT: ticket ref
	Kinds      []DataKind // JIT scope; empty for patient tiers (tier rules apply)
	Basis      string
	StartsAt   time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	ApprovedBy string
	ReasonCode string
}

// JITRequest asks for staff access (access-control-design §5).
type JITRequest struct {
	StaffRef    string
	StaffRole   string
	PatientRef  string
	Kinds       []DataKind
	ReasonCode  string // complaint, rights_request, payment_dispute, security_incident, legal_order
	TicketRef   string
	ApproverRef string
	Duration    time.Duration // at most 4 hours
	BreakGlass  bool
}

// Gate is the interface other modules use.
type Gate interface {
	Authorize(ctx context.Context, r Request) (Decision, error)
	HasConsent(ctx context.Context, subjectRef, purposeID string) (bool, error)
}
