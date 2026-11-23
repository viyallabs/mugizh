package verification

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

// Clinic (facility) verification (D-38, S-117 part 2). A clinic is verified through the
// same orchestrator as a doctor: same rounds, same four-eyes rules, same evidence
// pipeline and the same status event, with subject_kind = clinic and the clinic ID as the
// subject reference. The clinic's own administrator edits and submits the case; nobody
// else can reach it (others get not-found).
//
// Field mapping, so no new columns are needed:
//
//	CouncilCode        -> the registering authority (for example the state clinical
//	                      establishments authority); free text, normalised like a council
//	RegistrationNumber -> the clinic's registration or establishment number
//	RegistrationYear   -> year of registration
//	RegisteredName     -> the clinic's legal name
//
// A clinic has no qualifications, specialty or photo ID. There is no register adapter for
// clinics in v1.0, so only the duplicate check runs and a reviewer confirms the documents
// by hand [CONFIRM: HFR or state register lookup later].

// ClinicActor is the clinic administrator acting, built from the principal. ClinicRef is
// the principal's acting clinic (scope); the requested clinic must equal it.
type ClinicActor struct {
	UserID    string
	ClinicRef string
}

func (a ClinicActor) actor() actor { return actor{Ref: a.UserID, Role: "clinic_admin"} }

// scope checks the clinic admin's scope. Any other clinic looks like a missing one (K-SEC-04).
func (a ClinicActor) scope(clinicRef string) error {
	if validRef(a.UserID) != nil || validRef(clinicRef) != nil || a.ClinicRef == "" || a.ClinicRef != clinicRef {
		return ErrNotFound
	}
	return nil
}

// ClinicDraftInput is what a clinic admin enters.
type ClinicDraftInput struct {
	AuthorityName      string // the registering authority
	RegistrationNumber string
	RegistrationYear   int
	LegalName          string
}

var reClinicName = regexp.MustCompile(`^[\p{L}\p{N}\p{M} .,'&()/\-]{2,120}$`)

func (m *Module) validateClinicDraft(in ClinicDraftInput, now time.Time) (normalised, error) {
	var n normalised
	for _, t := range []string{in.AuthorityName, in.RegistrationNumber, in.LegalName} {
		if scrub.ContainsProhibitedID(t, m.cfg.ProhibitedIDValidators) {
			return n, ErrProhibitedID
		}
	}
	// Reuse the doctor rules for the shared fields.
	d, err := m.validateDraft(DraftInput{CouncilCode: in.AuthorityName, RegistrationNumber: in.RegistrationNumber, RegistrationYear: in.RegistrationYear}, now)
	if err != nil {
		var inv *InvalidError
		if errors.As(err, &inv) && inv.Field == "councilCode" {
			return n, invalid("authorityName", inv.Code)
		}
		return n, err
	}
	n = d
	if name := strings.Join(strings.Fields(in.LegalName), " "); name != "" {
		if !reClinicName.MatchString(name) {
			return n, invalid("legalName", "format")
		}
		n.name = name
	}
	return n, nil
}

// SaveClinicDraft creates or updates the clinic's draft. The admin must hold D-VERIFY.
func (m *Module) SaveClinicDraft(ctx context.Context, a ClinicActor, clinicRef string, in ClinicDraftInput) (*Case, error) {
	if err := a.scope(clinicRef); err != nil {
		return nil, err
	}
	n, err := m.validateClinicDraft(in, m.now())
	if err != nil {
		return nil, err
	}
	if err := m.requireConsent(ctx, a.UserID); err != nil {
		return nil, err
	}
	c, err := m.saveDraft(ctx, KindClinic, clinicRef, a.actor(), n)
	if err != nil {
		return nil, err
	}
	return m.doctorView(ctx, c)
}

// GetClinicCase returns the clinic's own case to its admin.
func (m *Module) GetClinicCase(ctx context.Context, a ClinicActor, clinicRef string) (*Case, error) {
	if err := a.scope(clinicRef); err != nil {
		return nil, err
	}
	c, err := scanCase(m.pool.QueryRow(ctx, `SELECT `+caseCols+` FROM verification_case WHERE subject_kind = $1 AND subject_ref = $2`, KindClinic, clinicRef))
	if err != nil {
		return nil, err
	}
	return m.doctorView(ctx, c)
}

// UploadClinicEvidence stores one document for the clinic's draft (same pipeline and
// per-item key as a doctor's evidence).
func (m *Module) UploadClinicEvidence(ctx context.Context, a ClinicActor, clinicRef string, in EvidenceInput) (*EvidenceMeta, error) {
	if err := a.scope(clinicRef); err != nil {
		return nil, err
	}
	return m.uploadEvidence(ctx, KindClinic, clinicRef, a.actor(), in)
}

// DeleteClinicEvidence withdraws one of the clinic's uploads while the case is editable.
func (m *Module) DeleteClinicEvidence(ctx context.Context, a ClinicActor, clinicRef, evidenceID string) error {
	if err := a.scope(clinicRef); err != nil {
		return err
	}
	return m.deleteEvidence(ctx, KindClinic, clinicRef, a.actor(), evidenceID)
}

// SubmitClinic sends the clinic's draft for review. The admin must hold D-VERIFY.
func (m *Module) SubmitClinic(ctx context.Context, a ClinicActor, clinicRef string) (*Case, error) {
	if err := a.scope(clinicRef); err != nil {
		return nil, err
	}
	if err := m.requireConsent(ctx, a.UserID); err != nil {
		return nil, err
	}
	caseID, err := m.submit(ctx, KindClinic, clinicRef, a.actor())
	if err != nil {
		return nil, err
	}
	if err := m.RunChecks(ctx, caseID); err != nil {
		m.log.Warn("verification.checks_deferred", logging.Err(err))
	}
	return m.GetClinicCase(ctx, a, clinicRef)
}

// SuspendClinic suspends a verified clinic at once; directory delists all its practices and
// booking cancels its future appointments from the status event. Reinstatement needs a new
// verification decision.
func (m *Module) SuspendClinic(ctx context.Context, actorRef, clinicRef, reasonCode string) error {
	return m.suspend(ctx, KindClinic, actorRef, clinicRef, reasonCode)
}

// TriggerClinicReverification starts re-verification of a clinic (for example after a
// complaint), like TriggerReverification for a doctor.
func (m *Module) TriggerClinicReverification(ctx context.Context, actorRef, clinicRef, reasonCode string) error {
	return m.triggerReverification(ctx, KindClinic, actorRef, clinicRef, reasonCode)
}

// ClinicStatus implements public.ClinicReader. It returns StatusNone when no case exists.
func (m *Module) ClinicStatus(ctx context.Context, clinicRef string) (public.Status, error) {
	return m.statusOf(ctx, KindClinic, clinicRef)
}

var _ public.ClinicReader = (*Module)(nil)
