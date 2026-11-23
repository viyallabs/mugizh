package verification

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// Round states: the workflow of one submission round.
const (
	roundDraft     = "draft"
	roundSubmitted = "submitted"
	roundChecking  = "checking"
	roundNeedsInfo = "needs_info"
	roundDecided   = "decided"
)

const caseCols = `id, subject_kind, subject_ref, status, round, round_state, council_code, registration_number, registration_key,
	registration_year, registered_name, qualifications, specialty_claim, specialty_confirmed, id_type, auto_check, manual_flags,
	info_requested, pending_outcome, pending_by, pending_reason, licence_expires_on, verified_at, re_verify_due_at, decided_at,
	submitted_at, created_at, updated_at`

// caseRow is one verification case as stored.
type caseRow struct {
	ID, Kind, Subject    string
	Status               public.Status
	Round                int
	RoundState           string
	Council, Number, Key string
	Year                 *int
	Name                 string
	Quals                []string
	Specialty            string
	SpecialtyConfirmed   bool
	IDType               string
	AutoCheck            string
	Flags                []string
	InfoRequested        string
	PendingOutcome       string
	PendingBy            *string
	PendingReason        string
	LicenceExp           *time.Time
	VerifiedAt, DueAt    *time.Time
	DecidedAt            *time.Time
	SubmittedAt          *time.Time
	CreatedAt, UpdatedAt time.Time
}

func scanCase(row pgx.Row) (*caseRow, error) {
	var c caseRow
	var st string
	err := row.Scan(&c.ID, &c.Kind, &c.Subject, &st, &c.Round, &c.RoundState, &c.Council, &c.Number, &c.Key, &c.Year, &c.Name,
		&c.Quals, &c.Specialty, &c.SpecialtyConfirmed, &c.IDType, &c.AutoCheck, &c.Flags, &c.InfoRequested, &c.PendingOutcome,
		&c.PendingBy, &c.PendingReason, &c.LicenceExp, &c.VerifiedAt, &c.DueAt, &c.DecidedAt, &c.SubmittedAt, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Status = public.Status(st)
	return &c, nil
}

// lockCaseByID reads a case with a row lock.
func lockCaseByID(ctx context.Context, tx pgx.Tx, id string) (*caseRow, error) {
	return scanCase(tx.QueryRow(ctx, `SELECT `+caseCols+` FROM verification_case WHERE id = $1 FOR UPDATE`, id))
}

// lockCaseBySubject reads a subject's case with a row lock.
func lockCaseBySubject(ctx context.Context, tx pgx.Tx, kind, subject string) (*caseRow, error) {
	return scanCase(tx.QueryRow(ctx, `SELECT `+caseCols+` FROM verification_case WHERE subject_kind = $1 AND subject_ref = $2 FOR UPDATE`, kind, subject))
}

// isApplication is true while the case is a first application: its status mirrors the
// round state. After a decision, a later round (re-verification) leaves the standing
// untouched until it is decided.
func (c *caseRow) isApplication() bool {
	switch c.Status {
	case public.StatusDraft, public.StatusSubmitted, public.StatusChecking, public.StatusNeedsInfo:
		return true
	}
	return false
}

// actor is who performs an action, for audit.
type actor struct{ Ref, Role string }

var system = actor{Ref: systemActor, Role: "system"}

func (m *Module) auditEvent(ctx context.Context, tx pgx.Tx, a actor, action string, c *caseRow, reason string) error {
	return ap.Record(ctx, tx, ap.Event{
		ActorRef: a.Ref, ActorRole: a.Role, Action: action, SubjectRef: c.Subject, ResourceType: "verification_case", ResourceRef: c.ID,
		ReasonCode: reason, Result: ap.Allowed, RequestID: httpx.RequestID(ctx), IPHash: httpx.IPHash(ctx), OccurredAt: m.now(),
	})
}

// setStatus changes the public status, audits it and announces it. A no-op when the
// status is unchanged. The caller persists the row in the same transaction.
func (m *Module) setStatus(ctx context.Context, tx pgx.Tx, a actor, c *caseRow, to public.Status, action, reason string) error {
	if c.Status == to {
		return nil
	}
	c.Status = to
	if err := m.auditEvent(ctx, tx, a, action, c, reason); err != nil {
		return err
	}
	return outbox.Emit(ctx, tx, outbox.Event{
		Type: public.EventStatusChanged, SubjectRef: c.Subject, OccurredAt: m.now().UTC(),
		Payload: map[string]string{public.PayloadProfessional: c.Subject, public.PayloadStatus: string(to), public.PayloadSubjectKind: c.Kind},
	})
}

// setRound moves the workflow of the current round. For a first application the status
// follows it (and is announced); for a re-verification round only the round state moves.
func (m *Module) setRound(ctx context.Context, tx pgx.Tx, a actor, c *caseRow, to, action, reason string) error {
	app := c.isApplication()
	c.RoundState = to
	if app {
		return m.setStatus(ctx, tx, a, c, public.Status(to), action, reason)
	}
	return m.auditEvent(ctx, tx, a, action, c, reason)
}

// save writes every mutable column of c. Rows are locked by the caller.
func (m *Module) save(ctx context.Context, tx pgx.Tx, c *caseRow) error {
	_, err := tx.Exec(ctx, `UPDATE verification_case SET status=$2, round=$3, round_state=$4, council_code=$5, registration_number=$6,
		registration_key=$7, registration_year=$8, registered_name=$9, qualifications=$10, specialty_claim=$11, specialty_confirmed=$12,
		id_type=$13, auto_check=$14, manual_flags=$15, info_requested=$16, pending_outcome=$17, pending_by=$18, pending_reason=$19,
		licence_expires_on=$20, verified_at=$21, re_verify_due_at=$22, decided_at=$23, submitted_at=$24, updated_at=$25 WHERE id=$1`,
		c.ID, string(c.Status), c.Round, c.RoundState, c.Council, c.Number, c.Key, c.Year, c.Name, nonNil(c.Quals), c.Specialty,
		c.SpecialtyConfirmed, c.IDType, c.AutoCheck, nonNil(c.Flags), c.InfoRequested, c.PendingOutcome, c.PendingBy, c.PendingReason,
		c.LicenceExp, c.VerifiedAt, c.DueAt, c.DecidedAt, c.SubmittedAt, m.now().UTC())
	if err != nil {
		return fmt.Errorf("verification: save case: %w", err)
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// effective returns the status the gate must act on at time now. A verified or
// re-verification-due professional whose licence has expired, or whose grace period has
// run out, is not verified even if the periodic job has not moved the case yet.
func (m *Module) effective(c *caseRow, now time.Time) public.Status {
	switch c.Status {
	case public.StatusVerified, public.StatusReVerifyDue:
		if c.LicenceExp != nil && !now.Before(*c.LicenceExp) {
			return public.StatusExpired
		}
		if c.DueAt == nil || !now.Before(c.DueAt.Add(m.cfg.ReverifyGrace)) {
			return public.StatusSuspended
		}
	}
	return c.Status
}

// Status implements public.Reader. It returns StatusNone when no case exists.
func (m *Module) Status(ctx context.Context, professionalRef string) (public.Status, error) {
	return m.statusOf(ctx, KindDoctor, professionalRef)
}

func (m *Module) statusOf(ctx context.Context, kind, ref string) (public.Status, error) {
	if !ids.Valid(ref) {
		return public.StatusNone, nil // no such subject: not verified
	}
	c, err := scanCase(m.pool.QueryRow(ctx, `SELECT `+caseCols+` FROM verification_case WHERE subject_kind = $1 AND subject_ref = $2`,
		kind, ref))
	if errors.Is(err, ErrNotFound) {
		return public.StatusNone, nil
	}
	if err != nil {
		return "", fmt.Errorf("verification: status: %w", err)
	}
	return m.effective(c, m.now()), nil
}

// IsVerified implements public.Reader: the K-CS-01 gate. Fails closed.
func (m *Module) IsVerified(ctx context.Context, professionalRef string) (bool, error) {
	st, err := m.Status(ctx, professionalRef)
	if err != nil {
		return false, err
	}
	return st == public.StatusVerified || st == public.StatusReVerifyDue, nil
}

// PrescriberFacts implements public.PrescriberReader: the registration details reviewers
// approved, for a doctor who is verified now. Fails closed.
func (m *Module) PrescriberFacts(ctx context.Context, professionalRef string) (public.PrescriberFacts, error) {
	ok, err := m.IsVerified(ctx, professionalRef)
	if err != nil {
		return public.PrescriberFacts{}, err
	}
	if !ok {
		return public.PrescriberFacts{}, public.ErrNotVerified
	}
	var f public.PrescriberFacts
	err = m.pool.QueryRow(ctx, `SELECT verified_registered_name, verified_qualifications, verified_council_code, verified_registration_number
		FROM verification_case WHERE subject_kind = $1 AND subject_ref = $2`, KindDoctor, professionalRef).
		Scan(&f.RegisteredName, &f.Qualifications, &f.CouncilCode, &f.RegistrationNumber)
	if err != nil {
		return public.PrescriberFacts{}, fmt.Errorf("verification: prescriber facts: %w", err)
	}
	if f.RegisteredName == "" || f.RegistrationNumber == "" || f.CouncilCode == "" {
		return public.PrescriberFacts{}, public.ErrNotVerified // verified before the snapshot existed: re-verify
	}
	return f, nil
}

var _ public.PrescriberReader = (*Module)(nil)
