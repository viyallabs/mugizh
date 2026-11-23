package public

import (
	"errors"
	"time"
)

// Caregiver access (v1.1, phase 1; consent-design E-FAM-02, purpose P-CAREGIVER). A patient invites a person they
// trust with a one-time code; the person accepts it in their own patient-app session. The link is view-only and
// limited to the scopes the patient chose. Reads go through the same gate as every other non-self read (K-DP-02).

// Tier and data kinds of the caregiver gate.
const (
	// Caregiver is the tier of a read allowed by an active caregiver link.
	Caregiver Tier = "caregiver"
	// KindReminders: the patient's medicine and visit reminders (scope "reminders").
	KindReminders DataKind = "reminders"
	// KindPrescriptions: signed prescriptions only, never consultation notes (scope "prescriptions").
	KindPrescriptions DataKind = "prescriptions"
)

// Caregiver scopes are a closed set. Reminders and appointments are the defaults; prescriptions and documents are
// sensitive and are given only when the patient explicitly adds them (E-FAM-02, TC-47).
const (
	ScopeReminders     = "reminders"
	ScopeAppointments  = "appointments"
	ScopePrescriptions = "prescriptions"
	ScopeDocuments     = "documents"
)

// DefaultCaregiverScopes are given when the patient names none.
func DefaultCaregiverScopes() []string { return []string{ScopeReminders, ScopeAppointments} }

// ScopeSensitive reports whether a scope needs the patient's explicit choice and a fresh step-up.
func ScopeSensitive(s string) bool { return s == ScopePrescriptions || s == ScopeDocuments }

// CaregiverInvitation is a new invitation. Code is shown once and never stored.
type CaregiverInvitation struct {
	ID        string
	Code      string
	Scopes    []string
	ExpiresAt time.Time
	Label     string
}

// CaregiverLink is a patient's link with one caregiver.
type CaregiverLink struct {
	ID           string
	PatientRef   string
	CaregiverRef string
	Scopes       []string
	Status       string // active, revoked
	CreatedAt    time.Time
	RevokedAt    *time.Time
	// Label is the patient's own note for the invitation (max 40 characters). Only the patient is ever given it.
	Label string
}

// Errors of the caregiver flows.
var (
	// ErrCaregiverLimit: 5 active caregivers per patient, or 5 patients per caregiver [CONFIRM].
	ErrCaregiverLimit = errors.New("records: caregiver limit reached")
	// ErrAlreadyLinked: the caregiver already has an active link with this patient.
	ErrAlreadyLinked = errors.New("records: already a caregiver of this patient")
	// ErrRedeemLimited: too many wrong codes in the last hour.
	ErrRedeemLimited = errors.New("records: too many wrong codes")
	// ErrCountryNotConfirmed: the caregiver did not confirm being in the country the pack allows.
	ErrCountryNotConfirmed = errors.New("records: caregiver must confirm being in the country")
)

// EventCaregiverLinked is emitted when a caregiver accepts an invitation: subject = the patient, payload linkRef.
// The notifications module tells the patient neutrally; the event carries no name and no scopes.
const EventCaregiverLinked = "caregiving.linked"
