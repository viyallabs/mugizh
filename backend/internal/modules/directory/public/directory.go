// Package public is the directory module's interface (module-boundaries §2): what
// booking and others may use. Only Verified doctors at Verified clinics are ever
// returned (K-CS-01); anything else is "not found".
package public

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned for an unknown practice and for one that is not listed
// (doctor or clinic not Verified, suspended, or the practice switched off). The two cases
// are deliberately indistinguishable.
var ErrNotFound = errors.New("directory: practice not found")

// Practice is a doctor's offer at a clinic.
type Practice struct {
	ID              string
	ProfessionalRef string
	ClinicRef       string
	// BranchRef is the branch (location) the practice belongs to. A clinic's main branch has
	// the same ID as the clinic.
	BranchRef string
	Modes     []string // in_person, video, audio
	FeeAmount int64    // minor units
	Currency  string   // from the country pack
	// FeeVersion changes whenever FeeAmount changes. Booking copies (FeeAmount, Currency,
	// FeeVersion) at hold time so a later fee change never touches an existing booking.
	FeeVersion string
	// TimeZone is the branch's IANA zone (K-ARC-04).
	TimeZone string
	// Listed is true for every Practice returned by Reader; unlisted ones are errors.
	Listed bool
}

// Reader is what booking depends on.
type Reader interface {
	// GetPractice returns a listed practice, or ErrNotFound.
	GetPractice(ctx context.Context, id string) (Practice, error)
}

// AvailabilitySource is the hook the booking module will implement (S-162, S-119). Until
// it is injected, the "next available slot" filter is refused and availability has no
// effect on ranking: directory never invents availability.
type AvailabilitySource interface {
	// EarliestSlots returns, for each practice that has an open slot, the start of its
	// earliest open slot at or after from. Practices without a slot are absent.
	EarliestSlots(ctx context.Context, practiceIDs []string, from time.Time) (map[string]time.Time, error)
}

// Branch is one location of a clinic (S-130, E-GEO-02).
type Branch struct {
	ID        string
	ClinicRef string
	TimeZone  string
	Active    bool
}

// BranchReader lets booking check that a branch belongs to a clinic before it closes it.
// It answers for inactive branches too, so a clinic can still close one; unknown is ErrNotFound.
type BranchReader interface {
	GetBranch(ctx context.Context, branchRef string) (Branch, error)
}

// Event types the module emits.
const EventPracticeUpdated = "practice.updated"

// NameReader returns a professional's public display name (S-157: appointment messages name
// the doctor; never the specialty, K-DP-09). It answers for any professional with a profile,
// listed or not, so a patient is told about their own appointment even after a suspension.
type NameReader interface {
	DisplayName(ctx context.Context, professionalRef string) (string, error)
}
