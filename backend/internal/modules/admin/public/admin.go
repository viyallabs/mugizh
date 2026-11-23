// Package public is the admin module's interface: just-in-time staff access (S-128) and
// the grievance queue (S-142). Other modules open a grievance or ask for staff access
// through it; they never read the admin schema.
package public

import (
	"context"
	"errors"
	"time"

	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
)

// Staff is the signed-in staff member making a call, taken from the session.
type Staff struct {
	Ref       string
	Role      string // support, ops, grievance_officer, security_owner
	RequestID string
}

// Errors.
var (
	ErrInvalid    = errors.New("admin: invalid input")
	ErrNotFound   = errors.New("admin: not found")
	ErrForbidden  = errors.New("admin: role not allowed for this action")
	ErrNotPending = errors.New("admin: request is not pending")
	ErrExpired    = errors.New("admin: request expired")
	// ErrRateLimited: a staff member made too many account lookups in the last hour (E-DP-16).
	ErrRateLimited = errors.New("admin: too many lookups")
	// ErrUnavailable: a dependency the action needs is not configured.
	ErrUnavailable = errors.New("admin: not available")
	// ErrSelfApproval: the approver must not be the requester (K-SEC-08).
	ErrSelfApproval = rp.ErrSelfApproval
)

// Reason codes: a closed list, no free-form "other" (access-control-design §5).
const (
	ReasonComplaint        = "complaint"
	ReasonRightsRequest    = "rights_request"
	ReasonPaymentDispute   = "payment_dispute"
	ReasonSecurityIncident = "security_incident"
	ReasonLegalOrder       = "legal_order"
)

// AccessRequestInput is what a staff member asks for.
type AccessRequestInput struct {
	PatientRef string
	Kinds      []rp.DataKind
	ReasonCode string
	TicketRef  string
	Duration   time.Duration // at most 4 hours
}

// AccessRequest is one request and its outcome.
type AccessRequest struct {
	ID                 string
	StaffRef           string
	StaffRole          string
	PatientRef         string
	Kinds              []rp.DataKind
	ReasonCode         string
	TicketRef          string
	Duration           time.Duration
	BreakGlass         bool
	Status             string // pending, approved, rejected, expired
	RequestedAt        time.Time
	DecidedAt          *time.Time
	ApproverRef        string
	GrantID            string
	GrantExpiresAt     *time.Time
	JustificationDueAt *time.Time
	JustifiedAt        *time.Time
}

// ReviewRow is one line of the monthly access review.
type ReviewRow struct {
	RequestID   string
	RequestedAt time.Time
	StaffRef    string
	StaffRole   string
	PatientRef  string
	Kinds       []string
	ReasonCode  string
	TicketRef   string
	ApproverRef string
	BreakGlass  bool
	Status      string
	GrantID     string
	Justified   bool
}

// GrievanceKind decides the legal timer.
type GrievanceKind string

// Queue item kinds and their timers (grievance-redressal.md).
const (
	KindComplaint         GrievanceKind = "complaint"           // acknowledge 24 h, resolve 7 days
	KindContentTakedown   GrievanceKind = "content_takedown"    // 36 h
	KindOrderTakedown     GrievanceKind = "order_takedown"      // 3 h
	KindIntimateImage     GrievanceKind = "intimate_image"      // 2 h
	KindLawfulRequest     GrievanceKind = "lawful_request"      // 72 h
	KindMedicalRecordCopy GrievanceKind = "medical_record_copy" // 72 h
	KindDataRights        GrievanceKind = "data_rights"         // 15 days
	// KindChildAccount is a CH-12 case: a doctor found the patient is a minor (TC-48). It uses
	// the complaint timers until a specific time is decided (decisions log D-42).
	KindChildAccount GrievanceKind = "child_account"
)

// Grievance is a queue item. The text is returned only to the officer who opens it.
type Grievance struct {
	ID             string
	Reference      string
	Kind           GrievanceKind
	Status         string // open, acknowledged, resolved
	SubmitterRef   string
	SourceRef      string
	ReceivedAt     time.Time
	AckDueAt       time.Time
	DueAt          time.Time
	AcknowledgedAt *time.Time
	ResolvedAt     *time.Time
	Outcome        string
}

// Alert is a timer alert from DueSoon.
type Alert struct {
	GrievanceID string
	Reference   string
	Kind        GrievanceKind
	Reason      string // ack_overdue, alert_80_percent, alert_10_days, breached, justification_overdue
	DueAt       time.Time
}

// Service is the interface other modules use.
type Service interface {
	// OpenGrievance records a patient's complaint and returns its reference and ack due time.
	OpenGrievance(ctx context.Context, submitterRef, text string) (Grievance, error)
	// RequestAccess asks for just-in-time staff access (needs a security owner's approval).
	RequestAccess(ctx context.Context, s Staff, in AccessRequestInput) (AccessRequest, error)
}
