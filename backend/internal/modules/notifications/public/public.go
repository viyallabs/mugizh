// Package public is the notifications module's interface (S-141). Other modules enqueue
// messages with Sender.Send; clinical implements ReminderContentResolver.
//
// Rules the module enforces for every message (K-DP-09, K-MSG-01, K-MSG-03): no diagnosis or
// specialty in any text, medicine names only with P-MEDNAMES checked at send time, SMS only
// through registered templates, promotional messages only with P-NEWS.
package public

import (
	"context"
	"errors"
)

// Priority of a message. Critical items (medicine reminders, consult starting, security
// alerts) are sent first, may fall back to SMS, and are not held in quiet hours.
type Priority string

// Priorities.
const (
	Critical Priority = "critical"
	Normal   Priority = "normal"
)

// Category of a message. Service messages follow from the user's own actions; promotional
// ones need P-NEWS (none exist in v1.0).
type Category string

// Categories.
const (
	Service     Category = "service"
	Promotional Category = "promotional"
)

// VarType types a variable so it can be checked against the SMS registry.
type VarType string

// Variable types.
const (
	Numeric      VarType = "numeric"
	Alphanumeric VarType = "alphanumeric"
	Ref          VarType = "ref" // opaque identifier for deep links
	// Time is an instant (RFC 3339, UTC). It is formatted at send time in the recipient's time
	// zone with the pack layout, so callers never format dates for a user (K-ARC-04).
	Time VarType = "time"
)

// Var is one typed template variable.
type Var struct {
	Name  string
	Type  VarType
	Value string
}

// Request asks for one notification.
type Request struct {
	TemplateKey  string
	RecipientRef string // user ID
	Vars         []Var
	Priority     Priority // empty: the template's default
	Category     Category
	Reason       string // short code such as "booking.confirmed"; for the delivery log
	// DedupeKey makes Send idempotent: the same key returns the same dispatch ID.
	DedupeKey string
}

// Errors returned by Send.
var (
	ErrInvalid         = errors.New("notifications: invalid request")
	ErrUnknownTemplate = errors.New("notifications: unknown template")
	// ErrConsentRequired is returned (with a dispatch ID, recorded as blocked) when a
	// promotional message has no P-NEWS consent (TC-52).
	ErrConsentRequired = errors.New("notifications: consent required")
	// ErrPromotionalNotAllowed: promotional category without a promotional template.
	ErrPromotionalNotAllowed = errors.New("notifications: promotional category not allowed for this template")
)

// Sender enqueues a notification and returns its dispatch ID.
type Sender interface {
	Send(ctx context.Context, r Request) (dispatchID string, err error)
}

// ReminderContentResolver builds reminder wording at send time. Notifications defines it and
// clinical implements it, so scheduler-core never holds health data (K-ARC-02).
//
// neutralText never names a medicine, condition or specialty. namedText may name the medicine
// and is used only when allowMedicineNames is true (P-MEDNAMES checked now, K-DP-09);
// implementations should return "" for namedText when it is false. Any error makes
// notifications use the generic neutral wording.
type ReminderContentResolver interface {
	Resolve(ctx context.Context, occurrenceRef, activityExternalRef, typeKey, recipientRef string, allowMedicineNames bool) (neutralText, namedText string, err error)
}

// PushTarget is one device that can receive push. DeviceID lets the owner module drop a dead token.
type PushTarget struct {
	DeviceID string
	Platform string // android | ios
	Token    string
}
