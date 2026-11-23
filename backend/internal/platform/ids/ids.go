// Package ids creates and checks opaque identifiers (API standards §1: UUIDv7, never
// sequential, never meaningful). Shared value utility (module rule B7).
package ids

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
)

// New returns a new UUIDv7 string. It panics only if the system random source fails,
// which is unrecoverable for a service that issues identifiers.
func New() string {
	id, err := uuid.NewV7()
	if err != nil {
		panic(fmt.Sprintf("ids: random source failed: %v", err))
	}
	return id.String()
}

// Valid reports whether s is a canonical UUID string (any version). Used to reject
// malformed IDs before they reach the database.
func Valid(s string) bool {
	if len(s) != 36 {
		return false
	}
	_, err := uuid.Parse(s)
	return err == nil
}

// Token returns n random bytes hex-encoded. Used for opaque secrets (access tokens,
// verification references). n must be at least 16 (128 bits, review R-09).
func Token(n int) (string, error) {
	if n < 16 {
		return "", fmt.Errorf("ids: token too short: %d bytes", n)
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("ids: random source failed: %w", err)
	}
	return hex.EncodeToString(b), nil
}
