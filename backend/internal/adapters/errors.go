// Package adapters holds the common error model every provider adapter maps vendor
// errors to (adapter-interfaces §3, rule A6). Business code never sees a vendor code.
package adapters

import (
	"errors"
	"fmt"
)

// Kind is a common error kind.
type Kind string

// Error kinds (adapter-interfaces §3).
const (
	Timeout     Kind = "timeout"     // outcome unknown: check status before retrying money or messages
	Unavailable Kind = "unavailable" // vendor down or circuit open: use the fallback
	RateLimited Kind = "rate_limited"
	Rejected    Kind = "rejected" // business refusal (DND, template mismatch, declined)
	Invalid     Kind = "invalid"  // our request was wrong: alert, do not retry
	AuthFailed  Kind = "auth_failed"
	NotFound    Kind = "not_found"
)

// Error is an adapter error.
type Error struct {
	Kind   Kind
	Reason string // short code, e.g. TEMPLATE_MISMATCH; never vendor payloads or personal data
}

func (e *Error) Error() string { return fmt.Sprintf("adapter %s: %s", e.Kind, e.Reason) }

// E builds an adapter error.
func E(kind Kind, reason string) error { return &Error{Kind: kind, Reason: reason} }

// KindOf returns the kind of err, or "" when err is not an adapter error.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}
