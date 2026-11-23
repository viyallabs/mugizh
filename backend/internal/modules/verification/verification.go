// Package verification is the professional verification module (F17: S-109 orchestrator,
// S-110 doctor submission, S-118 prescribing gate, S-129 reviewer queue, S-160
// re-verification). It decides who may be listed, booked and prescribe (K-CS-01).
//
// Rules this package keeps:
//   - Nothing is auto-approved. Register lookups and document checks only add advice; a
//     human reviewer decides Verified (E-VER-01, TC-42, TC-45).
//   - A rejection, or a Verified decision over a failed automated check, needs a second,
//     different reviewer. Reviewers never decide their own case (TM-29).
//   - The gate fails closed (Status, IsVerified).
//   - ID images are encrypted per item, deleted 90 days after the final decision, and at
//     once when a prohibited identity document is received (AAD-04, RET-08).
//   - Every transition is audited in the same transaction and announced with
//     verification.status_changed through the outbox.
package verification

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/malwarescan"
	vadapter "gitlab.com/viyallabs/mugizh/backend/internal/adapters/verification"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Source returns the module's migrations.
func Source() migrate.Source {
	sub, _ := fs.Sub(migrations, "migrations")
	return migrate.Source{Module: "verification", Schema: "verification", FS: sub}
}

// Subject kinds: a doctor, or a clinic (facility, D-38), through the same orchestrator.
const (
	KindDoctor = "doctor"
	KindClinic = "clinic"
)

// Evidence kinds.
const (
	EvidenceRegistrationCertificate = "registration_certificate"
	EvidenceDegree                  = "degree"
	EvidencePhotoID                 = "photo_id"
	EvidenceVerificationPhoto       = "verification_photo"
	EvidenceEstablishmentProof      = "establishment_proof" // clinic only: proof the premises are the clinic's
)

// Decision outcomes a reviewer can record.
const (
	OutcomeVerified  = "verified"
	OutcomeRejected  = "rejected"
	OutcomeNeedsInfo = "needs_info"
)

// systemActor is the audit actor for jobs and automatic checks.
const systemActor = "system:verification"

// Config holds values from the country pack and the decisions log. Nothing here is a
// country literal in code: the pack supplies validators and the ID allow-list.
type Config struct {
	// ProhibitedIDValidators are the validator names from the pack
	// (people.prohibited_identifiers[].validator). Any input holding such a number is
	// rejected and nothing is stored (K-DP-05, TC-20).
	ProhibitedIDValidators []string
	// IDTypes is the photo-ID allow-list (data, not code). It must not include any
	// identity document the policy prohibits.
	IDTypes []string
	// EvidenceKinds are the allowed evidence kinds; RequiredEvidenceKinds must each be
	// present before a case can be submitted.
	EvidenceKinds         []string
	RequiredEvidenceKinds []string
	// ClinicEvidenceKinds and RequiredClinicEvidenceKinds are the same for a clinic
	// (facility) case (D-38). A clinic sends registration or establishment documents only,
	// never a personal photo ID. Required may be empty where a state does not require
	// registration (C24) [CONFIRM: launch state].
	ClinicEvidenceKinds         []string
	RequiredClinicEvidenceKinds []string
	// ReasonCodes is the allow-list of decision reason codes (mandatory on every decision).
	ReasonCodes []string
	// ConsentPurpose is the doctor consent needed before any data is taken (D-VERIFY).
	ConsentPurpose string

	MaxEvidenceBytes   int           // 10 MB
	MaxEvidencePerCase int           // per round
	EvidenceRetention  time.Duration // 90 days after the final decision (RET-08)
	CheckTimeout       time.Duration // register lookup timeout (adapter-interfaces §11: 15 s)
	ScanTimeout        time.Duration // malware scan of one evidence file (review M7)
	ReviewSLA          time.Duration // manual review target: 48 h (performance targets)
	ReverifyMonths     int           // 12 months (RG-07)
	ReverifyGrace      time.Duration // time after the due date before suspension [CONFIRM]
}

// DefaultConfig returns the documented defaults. ProhibitedIDValidators and IDTypes must
// still be set from the country pack.
func DefaultConfig() Config {
	return Config{
		EvidenceKinds:               []string{EvidenceRegistrationCertificate, EvidenceDegree, EvidencePhotoID, EvidenceVerificationPhoto},
		RequiredEvidenceKinds:       []string{EvidenceRegistrationCertificate, EvidencePhotoID},
		ClinicEvidenceKinds:         []string{EvidenceRegistrationCertificate, EvidenceEstablishmentProof},
		RequiredClinicEvidenceKinds: []string{EvidenceRegistrationCertificate},
		ReasonCodes: []string{
			"checks_passed", "register_confirmed_manually", "registration_invalid", "identity_mismatch", "documents_not_genuine",
			"not_eligible", "documents_unreadable", "id_type_not_accepted", "details_incomplete", "prohibited_id",
			"licence_suspended", "complaint", "register_status_changed",
		},
		ConsentPurpose:     "D-VERIFY",
		MaxEvidenceBytes:   10 << 20,
		MaxEvidencePerCase: 10,
		EvidenceRetention:  90 * 24 * time.Hour,
		CheckTimeout:       15 * time.Second,
		ScanTimeout:        30 * time.Second,
		ReviewSLA:          48 * time.Hour,
		ReverifyMonths:     12,
		ReverifyGrace:      14 * 24 * time.Hour,
	}
}

func (c Config) validate() error {
	if len(c.ProhibitedIDValidators) == 0 {
		return errors.New("verification: prohibited identifier validators are required (country pack)")
	}
	if len(c.IDTypes) == 0 {
		return errors.New("verification: photo ID allow-list is required (country pack)")
	}
	if len(c.EvidenceKinds) == 0 || len(c.ReasonCodes) == 0 || c.ConsentPurpose == "" {
		return errors.New("verification: incomplete config")
	}
	for _, k := range c.RequiredEvidenceKinds {
		if !slices.Contains(c.EvidenceKinds, k) {
			return fmt.Errorf("verification: required evidence kind %q is not an allowed kind", k)
		}
	}
	for _, k := range c.RequiredClinicEvidenceKinds {
		if !slices.Contains(c.ClinicEvidenceKinds, k) {
			return fmt.Errorf("verification: required clinic evidence kind %q is not an allowed kind", k)
		}
	}
	if c.MaxEvidenceBytes <= 0 || c.MaxEvidencePerCase <= 0 || c.EvidenceRetention <= 0 || c.CheckTimeout <= 0 || c.ScanTimeout <= 0 ||
		c.ReviewSLA <= 0 || c.ReverifyMonths <= 0 || c.ReverifyGrace < 0 {
		return errors.New("verification: incomplete config")
	}
	return nil
}

// Deps are the module's collaborators. Everything is an interface or a platform service.
type Deps struct {
	Crypto  *crypto.Service
	Blob    blobstore.Store
	Checker vadapter.Checker
	// Scanner checks every evidence file before it is encrypted and stored (review M7). A
	// scanner error rejects the upload: an unscanned file is never stored (fail closed).
	Scanner malwarescan.Scanner
	Consent rp.Gate // records gate: HasConsent for D-VERIFY
}

// Deleter is implemented by object stores that can delete an object. Deleting evidence
// uses it when present; the encryption key is destroyed either way, so the bytes are
// unreadable even if the store cannot delete (crypto-shredding).
type Deleter interface {
	Delete(ctx context.Context, key string) error
}

// Module is the verification module. It implements public.Reader.
type Module struct {
	pool *pgxpool.Pool
	cfg  Config
	d    Deps
	log  *slog.Logger
	now  func() time.Time
}

// New builds the module. pool must use the verification role.
func New(pool *pgxpool.Pool, cfg Config, d Deps, log *slog.Logger) (*Module, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if pool == nil || d.Crypto == nil || d.Blob == nil || d.Checker == nil || d.Scanner == nil || d.Consent == nil || log == nil {
		return nil, errors.New("verification: pool, crypto, blob store, checker, malware scanner, consent gate and logger are required")
	}
	return &Module{pool: pool, cfg: cfg, d: d, log: log, now: time.Now}, nil
}

// SetClock replaces the clock (tests).
func (m *Module) SetClock(now func() time.Time) { m.now = now }

var _ public.Reader = (*Module)(nil)
