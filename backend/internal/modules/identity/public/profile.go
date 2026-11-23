package public

import (
	"context"
	"time"
)

// ProfileReader is the identity module's read interface for the patient profile facts that
// clinical prints on a prescription (S-108, P02). The caller must already have been allowed
// to read this patient's data by records.Authorize (K-DP-02); this method does not decide.
type ProfileReader interface {
	// ProfileFacts returns the patient's name, completed age in years and sex. It returns an
	// error (never empty values) when the profile is missing or cannot be decrypted.
	ProfileFacts(ctx context.Context, userID string) (name string, ageYears int, sex string, err error)
}

// ProfileChanges tells when the patient's name, date of birth or sex last changed (review F3,
// P02): a doctor's identity confirmation made before that time no longer holds.
type ProfileChanges interface {
	ProfileIdentityChangedAt(ctx context.Context, userID string) (time.Time, error)
}
