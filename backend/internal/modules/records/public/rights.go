package public

import (
	"errors"
	"time"
)

// RightsType is the kind of data-rights request (data-rights-flows §2).
type RightsType string

// Request types.
const (
	RightsAccess     RightsType = "access"
	RightsExport     RightsType = "export"
	RightsCorrection RightsType = "correction"
	RightsErasure    RightsType = "erasure"
	RightsNomination RightsType = "nomination"
)

// RightsStatus is the state of a request. Erasure walks received, in_progress (step 1
// tasks running), restricted (step 1 done, RET-18 hold running), finalising (hold ended,
// step 2 tasks running) and completed; or cancelled (undone inside the window).
type RightsStatus string

// Request states.
const (
	StatusReceived   RightsStatus = "received"
	StatusInProgress RightsStatus = "in_progress"
	StatusRestricted RightsStatus = "restricted"
	StatusFinalising RightsStatus = "finalising"
	StatusCompleted  RightsStatus = "completed"
	StatusRejected   RightsStatus = "rejected"
	StatusCancelled  RightsStatus = "cancelled"
)

// Caller is who makes a rights request, taken from the authenticated session.
type Caller struct {
	UserRef    string
	StepUpAt   *time.Time // last step-up; nil when none
	Restricted bool       // session on a not yet re-verified device
	Role       string     // acting role (doctor, patient, ...)
	ClinicRef  string
	RequestID  string
	Client     string
	IPHash     string
}

// ReportItem is one line of the erasure report: what happened to which data and why it
// is kept (E-DP-03). Codes only; the app turns them into words (K-GLB-02).
type ReportItem struct {
	Module        string
	Action        string // restricted, deleted, erased_after_hold, erased, none
	RetentionRule string // RET-02, RET-05, RET-18 ...
	Until         *time.Time
}

// RightsRequest is a patient's data-rights request.
type RightsRequest struct {
	ID             string
	SubjectRef     string
	Type           RightsType
	Status         RightsStatus
	ReceivedAt     time.Time
	AckDueAt       time.Time
	DueAt          time.Time
	AcknowledgedAt *time.Time
	CompletedAt    *time.Time
	Outcome        string
	UndoUntil      *time.Time
	HoldEndsAt     *time.Time
	// ExportExpiresAt is when a finished export stops being offered.
	ExportExpiresAt *time.Time
	Report          []ReportItem
}

// Errors of the rights flows.
var (
	// ErrStepUpRequired: export and erasure need a step-up within the last five minutes.
	ErrStepUpRequired = errors.New("records: step-up required")
	// ErrNotFound: no such request, document or link for this caller (others' resources
	// are indistinguishable from missing ones, K-SEC-04).
	ErrNotFound = errors.New("records: not found")
	// ErrRestricted: the account is under erasure.
	ErrRestricted = errors.New("records: account is restricted")
	// ErrUndoClosed: the undo window has ended.
	ErrUndoClosed = errors.New("records: undo window has ended")
	// ErrConflict: the request is not in a state that allows this.
	ErrConflict = errors.New("records: request state conflict")
	// ErrLinkInvalid: unknown link, or a link bound to another user.
	ErrLinkInvalid = errors.New("records: link invalid")
	// ErrLinkExpired: expired or already used link.
	ErrLinkExpired = errors.New("records: link expired or used")
	// ErrDenied: the gate refused the read (the caller maps Hide to 404, else 403).
	ErrDenied = errors.New("records: access denied")
	// ErrFileType: not a PDF, JPEG or PNG by content.
	ErrFileType = errors.New("records: file type not allowed")
	// ErrQuota: the patient has reached the document count or total size limit.
	ErrQuota = errors.New("records: document quota reached")
	// ErrTooLarge: over the size limit.
	ErrTooLarge = errors.New("records: file too large")
	// ErrMalware: the scanner flagged the file.
	ErrMalware = errors.New("records: file failed the malware scan")
	// ErrScanUnavailable: the scanner failed or timed out; the file is not stored.
	ErrScanUnavailable = errors.New("records: malware scan unavailable")
	// ErrProhibitedID: the text holds a number the pack prohibits.
	ErrProhibitedID = errors.New("records: text contains a prohibited identifier")
)

// DocumentInfo is an upload's metadata.
type DocumentInfo struct {
	ID          string
	Title       string
	ContentType string
	SizeBytes   int
	CreatedAt   time.Time
}

// FileLink is an issued download link. Token is shown once and never stored.
type FileLink struct {
	Token     string
	ExpiresAt time.Time
}
