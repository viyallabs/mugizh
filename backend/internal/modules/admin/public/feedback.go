package public

import "time"

// User is the signed-in person sending feedback, taken from the session.
type User struct {
	Ref       string
	Role      string // the acting role as the session resolved it (any signed-in role)
	RequestID string
}

// Feedback kinds, platforms and statuses (S-189).
const (
	FeedbackKindFeedback = "feedback"
	FeedbackKindBug      = "bug"

	FeedbackNew     = "new"
	FeedbackTriaged = "triaged"
	FeedbackClosed  = "closed"
)

// FeedbackDiagnostics is the closed set of codes a user may attach, only after ticking
// "attach diagnostics". There is no free text, no identifier, no log and no screenshot.
type FeedbackDiagnostics struct {
	OSVersion     string `json:"osVersion,omitempty"`
	NetworkType   string `json:"networkType,omitempty"` // wifi, cellular, none, unknown
	LastErrorCode string `json:"lastErrorCode,omitempty"`
}

// FeedbackInput is what the user sends. Diagnostics is nil unless the user opted in.
type FeedbackInput struct {
	Kind        string
	Message     string
	Screen      string
	AppVersion  string
	Platform    string
	Diagnostics *FeedbackDiagnostics
}

// FeedbackReceipt is the answer to a submission.
type FeedbackReceipt struct {
	ID         string
	Reference  string
	ReceivedAt time.Time
}

// FeedbackItem is one of the user's own items, with their own message.
type FeedbackItem struct {
	ID          string
	Reference   string
	Kind        string
	Message     string
	Screen      string
	AppVersion  string
	Platform    string
	Diagnostics *FeedbackDiagnostics
	Status      string
	ReceivedAt  time.Time
}

// FeedbackTriage is an item as staff see it: never the message.
type FeedbackTriage struct {
	ID          string
	Reference   string
	Kind        string
	Screen      string
	AppVersion  string
	Platform    string
	Diagnostics *FeedbackDiagnostics
	Status      string
	ReceivedAt  time.Time
}
