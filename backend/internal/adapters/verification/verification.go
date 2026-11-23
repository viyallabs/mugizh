// Package verification is the professional-verification adapter port (adapter-interfaces
// §11, K-ARC-03). In the v1.0 beta only the manual source is used (S-111 deferred), so the
// module treats every result as advice for a human reviewer: no result ever verifies a
// professional on its own (E-VER-01, TC-42). Vendor implementations live in
// verification/<vendor>/ and are enabled only after the vendor register entry exists
// (K-DP-08).
package verification

import (
	"context"
)

// Outcome is the result of a registry lookup.
type Outcome string

// Registry lookup outcomes (adapter-interfaces §11). Anything else is treated by callers
// as Inconclusive.
const (
	Match        Outcome = "match"
	NoMatch      Outcome = "no_match"
	NotFound     Outcome = "not_found"
	Inconclusive Outcome = "inconclusive"
)

// RegistrationQuery is exactly what leaves our system for a register lookup: the council,
// the registration number and the name. Registration numbers repeat across councils, so
// the council is always required.
type RegistrationQuery struct {
	Council string
	Number  string
	Name    string
}

// RegistrationResult is a register answer.
type RegistrationResult struct {
	Outcome Outcome
	// RegisteredName is the name the register holds, when the source returns one. The
	// module compares it with the submitted name itself (fail closed).
	RegisteredName string
}

// DocumentQuery is a document check request. Never an identity document that the policy
// prohibits: those are rejected before any adapter is called.
type DocumentQuery struct {
	Type  string
	Image []byte
}

// DocumentResult is a document check answer.
type DocumentResult struct {
	Fields       map[string]string
	Authenticity float64 // 0 to 1
}

// Checker is the verification source port. Implementations map vendor errors to
// internal/adapters errors, have a timeout, and never log payloads (K-DP-04).
type Checker interface {
	CheckRegistration(ctx context.Context, q RegistrationQuery) (RegistrationResult, error)
	CheckDocument(ctx context.Context, q DocumentQuery) (DocumentResult, error)
}
