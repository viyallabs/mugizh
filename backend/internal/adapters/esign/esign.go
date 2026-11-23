// Package esign is the electronic-signature adapter port (adapter-interfaces §12a, P09, K-ARC-03).
//
// Only the SHA-256 hash of the document ever leaves us, never the prescription content.
// A timeout or cancelled session leaves the prescription unsigned and not issued: there is
// no fallback (P09 fails closed). Until the lawyer decides the method (N5) only the fake in
// esign/fake exists, and production signing stays blocked.
package esign

import (
	"context"
	"time"
)

// StartRequest is what leaves our system to begin signing.
type StartRequest struct {
	SignerRef    string // opaque doctor reference
	DocumentHash []byte // SHA-256 of the canonical document (32 bytes)
	// Summary is a short display line for the signing screen. It must hold no patient data.
	Summary string
}

// Session is a signing session.
type Session struct {
	ID          string
	RedirectURL string // empty when the method needs no redirect
}

// Signature is the completed signature, stored with the prescription version (RET-02).
type Signature struct {
	Value       []byte
	Certificate []byte // certificate chain, may be empty
	SignedAt    time.Time
	Provider    string
}

// Verdict is the result of Verify.
type Verdict string

// Verify outcomes.
const (
	Valid   Verdict = "valid"
	Invalid Verdict = "invalid"
	Revoked Verdict = "revoked"
)

// Signer is the port. Implementations map vendor errors to internal/adapters errors and
// never log payloads (K-DP-04).
type Signer interface {
	StartSigning(ctx context.Context, r StartRequest) (Session, error)
	CompleteSigning(ctx context.Context, sessionID string) (Signature, error)
	Verify(ctx context.Context, documentHash []byte, sig Signature) (Verdict, error)
}
