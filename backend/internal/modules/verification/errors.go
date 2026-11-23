package verification

import (
	"errors"
	"fmt"
)

// Errors. The HTTP layer maps them to problems; none carries personal data.
var (
	ErrNotFound         = errors.New("verification: not found")
	ErrInvalid          = errors.New("verification: invalid input")
	ErrCouncilRequired  = errors.New("verification: council is required")
	ErrProhibitedID     = errors.New("verification: input holds a prohibited identifier")
	ErrIDTypeNotAllowed = errors.New("verification: photo ID type is not accepted")
	ErrConsentRequired  = errors.New("verification: consent for verification is required")
	ErrState            = errors.New("verification: not allowed in the current state")
	ErrIncomplete       = errors.New("verification: submission is incomplete")
	ErrSelfReview       = errors.New("verification: a reviewer cannot act on their own case")
	ErrFourEyes         = errors.New("verification: a second, different reviewer must confirm")
	ErrReasonRequired   = errors.New("verification: a valid reason code is required")
	ErrEvidenceTooLarge = errors.New("verification: evidence is too large")
	ErrEvidenceType     = errors.New("verification: evidence type is not allowed")
	ErrEvidenceLimit    = errors.New("verification: too many evidence files")
	ErrEvidenceGone     = errors.New("verification: evidence has been deleted")
	// ErrEvidenceMalware: the scanner flagged the file; nothing is stored.
	ErrEvidenceMalware = errors.New("verification: file failed the malware scan")
	// ErrEvidenceScanUnavailable: the scanner failed or timed out; nothing is stored.
	ErrEvidenceScanUnavailable = errors.New("verification: malware scan unavailable")
)

// InvalidError names the invalid field and a stable code. It matches ErrInvalid.
type InvalidError struct{ Field, Code string }

func (e *InvalidError) Error() string {
	return fmt.Sprintf("verification: invalid %s: %s", e.Field, e.Code)
}

// Is makes errors.Is(err, ErrInvalid) true.
func (e *InvalidError) Is(target error) bool { return target == ErrInvalid }

func invalid(field, code string) error { return &InvalidError{Field: field, Code: code} }
