package verification

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/httpx"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// reviewer is the audit actor for a reviewer action.
func reviewer(ref string) actor { return actor{Ref: ref, Role: "reviewer"} }

// QueueItem is one row of the reviewer queue.
type QueueItem struct {
	CaseID         string
	SubjectRef     string
	SubjectKind    string
	Status         public.Status
	SubmittedAt    time.Time
	DueBy          time.Time // submitted + review target (48 h)
	Overdue        bool
	AutoCheck      string // passed | failed | "" (checks not recorded yet)
	AwaitingSecond bool   // a first reviewer proposed a decision; a different reviewer must confirm
	Reverification bool   // a lapsed professional starting again, not a first application
}

// ListQueue lists cases waiting for a human decision, oldest first. A reviewer never sees
// their own case in the queue. cursor is empty for the first page.
func (m *Module) ListQueue(ctx context.Context, reviewerRef string, limit int, cursor string) ([]QueueItem, string, error) {
	if err := validRef(reviewerRef); err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var afterT time.Time
	afterID := "00000000-0000-0000-0000-000000000000"
	if cursor != "" {
		us, id, ok := strings.Cut(cursor, ".")
		n, err := strconv.ParseInt(us, 10, 64)
		if !ok || err != nil || !ids.Valid(id) {
			return nil, "", invalid("cursor", "format")
		}
		afterT, afterID = time.UnixMicro(n).UTC(), id
	} else {
		afterT = time.Unix(0, 0).UTC()
	}
	rs, err := m.pool.Query(ctx, `SELECT id, subject_ref, subject_kind, status, submitted_at, auto_check, pending_outcome
		FROM verification_case
		WHERE round_state IN ('submitted','checking') AND submitted_at IS NOT NULL AND subject_ref <> $1 AND (submitted_at, id) > ($2, $3)
		ORDER BY submitted_at, id LIMIT $4`, reviewerRef, afterT, afterID, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("verification: queue: %w", err)
	}
	defer rs.Close()
	var out []QueueItem
	now := m.now()
	for rs.Next() {
		var q QueueItem
		var st, pending string
		if err := rs.Scan(&q.CaseID, &q.SubjectRef, &q.SubjectKind, &st, &q.SubmittedAt, &q.AutoCheck, &pending); err != nil {
			return nil, "", err
		}
		q.Status = public.Status(st)
		q.AwaitingSecond = pending != ""
		q.Reverification = q.Status != public.StatusSubmitted && q.Status != public.StatusChecking
		q.DueBy = q.SubmittedAt.Add(m.cfg.ReviewSLA)
		q.Overdue = now.After(q.DueBy)
		out = append(out, q)
	}
	if err := rs.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = strconv.FormatInt(last.SubmittedAt.UnixMicro(), 10) + "." + last.CaseID
	}
	// Review (Low): a reviewer reading the queue (doctor and clinic references and status) is
	// audited like opening a case. One event per page; no list goes into the audit trail.
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return ap.Record(ctx, tx, ap.Event{ActorRef: reviewerRef, ActorRole: "reviewer", Action: "verification.queue_listed",
			ResourceType: "verification_queue", Result: ap.Allowed, RequestID: httpx.RequestID(ctx), IPHash: httpx.IPHash(ctx), OccurredAt: m.now()})
	})
	if err != nil {
		return nil, "", fmt.Errorf("verification: audit queue: %w", err)
	}
	return out, next, nil
}

// OpenCase returns the full case for a reviewer and audits the access.
func (m *Module) OpenCase(ctx context.Context, reviewerRef, caseID string) (*Case, error) {
	if validRef(reviewerRef) != nil || validRef(caseID) != nil {
		return nil, ErrNotFound
	}
	c, err := scanCase(m.pool.QueryRow(ctx, `SELECT `+caseCols+` FROM verification_case WHERE id = $1`, caseID))
	if err != nil {
		return nil, err
	}
	if c.Subject == reviewerRef {
		return nil, ErrSelfReview
	}
	if err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return m.auditEvent(ctx, tx, reviewer(reviewerRef), "verification.case_opened", c, "")
	}); err != nil {
		return nil, err
	}
	return m.reviewerView(ctx, c)
}

// DecisionInput is a reviewer's decision. The reason code is mandatory.
type DecisionInput struct {
	CaseID      string
	ReviewerRef string
	Outcome     string // verified | rejected | needs_info
	ReasonCode  string
	// LicenceExpiresOn is the end of the registration's validity, when the register
	// shows one (verified only). Re-verification is due at the earlier of this and 12
	// months after the decision (RG-07).
	LicenceExpiresOn *time.Time
	// SpecialtyConfirmed is true when a recognised postgraduate qualification supports the
	// claimed specialty (RG-04); otherwise profiles show general practice.
	SpecialtyConfirmed bool
}

// DecisionResult says what the decision did.
type DecisionResult struct {
	Status         public.Status
	Final          bool
	AwaitingSecond bool // recorded as a proposal; a different reviewer must confirm
}

// RecordDecision records a reviewer's decision (S-129).
//
// Four-eyes (TM-29): a rejection, and a Verified decision over a failed automated check
// (an override), is only a proposal until a second, different reviewer records the same
// outcome. The same reviewer cannot confirm their own proposal, and no reviewer decides
// a case about themselves.
func (m *Module) RecordDecision(ctx context.Context, in DecisionInput) (*DecisionResult, error) {
	if validRef(in.ReviewerRef) != nil || validRef(in.CaseID) != nil {
		return nil, ErrNotFound
	}
	switch in.Outcome {
	case OutcomeVerified, OutcomeRejected, OutcomeNeedsInfo:
	default:
		return nil, invalid("outcome", "unknown")
	}
	if !slices.Contains(m.cfg.ReasonCodes, in.ReasonCode) {
		return nil, ErrReasonRequired
	}
	var res DecisionResult
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		res = DecisionResult{}
		c, err := lockCaseByID(ctx, tx, in.CaseID)
		if err != nil {
			return err
		}
		if c.Subject == in.ReviewerRef {
			return ErrSelfReview
		}
		if c.RoundState != roundChecking {
			return ErrState // not submitted, checks not recorded yet, or already decided
		}
		now := m.now().UTC()
		a := reviewer(in.ReviewerRef)
		override := in.Outcome == OutcomeVerified && c.AutoCheck != "passed"
		needsSecond := in.Outcome == OutcomeRejected || override

		if in.Outcome == OutcomeVerified && in.LicenceExpiresOn != nil && !in.LicenceExpiresOn.After(now) {
			return invalid("licenceExpiresOn", "past")
		}

		switch {
		case !needsSecond:
			return m.finalise(ctx, tx, c, a, in, false, nil, &res)
		case c.PendingOutcome == in.Outcome && c.PendingBy != nil && *c.PendingBy != in.ReviewerRef:
			var proposed string
			if err := tx.QueryRow(ctx, `SELECT id FROM decision WHERE case_id = $1 AND round = $2 AND stage = 'proposed' ORDER BY created_at DESC, id DESC LIMIT 1`,
				c.ID, c.Round).Scan(&proposed); err != nil {
				return err
			}
			return m.finalise(ctx, tx, c, a, in, override, &proposed, &res)
		case c.PendingOutcome == in.Outcome && c.PendingBy != nil && *c.PendingBy == in.ReviewerRef:
			return ErrFourEyes
		default:
			// First proposal, or a different outcome than the pending one: this reviewer's
			// view replaces it and a different reviewer must still confirm.
			if _, err := tx.Exec(ctx, `INSERT INTO decision (id, case_id, subject_ref, round, reviewer_ref, outcome, stage, reason_code, override, created_at)
				VALUES ($1,$2,$3,$4,$5,$6,'proposed',$7,$8,$9)`, ids.New(), c.ID, c.Subject, c.Round, in.ReviewerRef, in.Outcome, in.ReasonCode, override, now); err != nil {
				return err
			}
			ref := in.ReviewerRef
			c.PendingOutcome, c.PendingBy, c.PendingReason = in.Outcome, &ref, in.ReasonCode
			if err := m.auditEvent(ctx, tx, a, "verification.decision_proposed", c, in.ReasonCode); err != nil {
				return err
			}
			res = DecisionResult{Status: m.effective(c, now), AwaitingSecond: true}
			return m.save(ctx, tx, c)
		}
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// finalise applies a final decision inside the transaction.
func (m *Module) finalise(ctx context.Context, tx pgx.Tx, c *caseRow, a actor, in DecisionInput, override bool, confirms *string, res *DecisionResult) error {
	now := m.now().UTC()
	if _, err := tx.Exec(ctx, `INSERT INTO decision (id, case_id, subject_ref, round, reviewer_ref, outcome, stage, reason_code, override, confirms_id, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,'final',$7,$8,$9,$10)`, ids.New(), c.ID, c.Subject, c.Round, in.ReviewerRef, in.Outcome, in.ReasonCode, override, confirms, now); err != nil {
		return err
	}
	c.PendingOutcome, c.PendingBy, c.PendingReason = "", nil, ""

	switch in.Outcome {
	case OutcomeNeedsInfo:
		c.InfoRequested = in.ReasonCode
		if err := m.setRound(ctx, tx, a, c, roundNeedsInfo, "verification.info_requested", in.ReasonCode); err != nil {
			return err
		}
	case OutcomeVerified:
		c.VerifiedAt, c.DecidedAt = &now, &now
		c.InfoRequested = ""
		c.SpecialtyConfirmed = in.SpecialtyConfirmed
		c.LicenceExp = nil
		due := now.AddDate(0, m.cfg.ReverifyMonths, 0)
		if in.LicenceExpiresOn != nil {
			exp := time.Date(in.LicenceExpiresOn.Year(), in.LicenceExpiresOn.Month(), in.LicenceExpiresOn.Day(), 0, 0, 0, 0, time.UTC)
			c.LicenceExp = &exp
			if exp.Before(due) {
				due = exp // re_verify_due_at = min(licence expiry, verified_at + 12 months)
			}
		}
		c.DueAt = &due
		c.RoundState = roundDecided
		if err := m.statusOrAudit(ctx, tx, a, c, public.StatusVerified, "verification.decided", in.ReasonCode); err != nil {
			return err
		}
		// Snapshot what the reviewers approved: prescriptions print these, never a later
		// re-verification draft (PrescriberFacts).
		if _, err := tx.Exec(ctx, `UPDATE verification_case SET verified_registered_name = registered_name, verified_council_code = council_code,
			verified_registration_number = registration_number, verified_qualifications = qualifications WHERE id = $1`, c.ID); err != nil {
			return err
		}
	case OutcomeRejected:
		c.DecidedAt = &now
		c.InfoRequested = ""
		to := c.Status
		switch {
		case c.isApplication():
			to = public.StatusRejected
		case c.Status == public.StatusReVerifyDue || c.Status == public.StatusVerified:
			to = public.StatusSuspended // a failed re-verification suspends at once (RG-07)
		}
		c.RoundState = roundDecided
		if err := m.statusOrAudit(ctx, tx, a, c, to, "verification.decided", in.ReasonCode); err != nil {
			return err
		}
	}
	if in.Outcome != OutcomeNeedsInfo {
		// The decision is final: ID images are deleted 90 days from now (RET-08).
		if _, err := tx.Exec(ctx, `UPDATE evidence SET purge_after = $3 WHERE case_id = $1 AND round = $2 AND state = 'stored'`,
			c.ID, c.Round, now.Add(m.cfg.EvidenceRetention)); err != nil {
			return err
		}
	}
	res.Status, res.Final = m.effective(c, now), true
	return m.save(ctx, tx, c)
}

// statusOrAudit changes the status (audited and announced) or, when it is unchanged,
// only audits the decision.
func (m *Module) statusOrAudit(ctx context.Context, tx pgx.Tx, a actor, c *caseRow, to public.Status, action, reason string) error {
	if c.Status == to {
		return m.auditEvent(ctx, tx, a, action, c, reason)
	}
	return m.setStatus(ctx, tx, a, c, to, action, reason)
}

// ReportProhibitedID is the reviewer-checklist action for an identity document that the
// policy does not accept (AAD-04, TC-21): the file is hard-deleted at once, not after 90
// days, the deletion is audited, and the doctor is asked for another ID. It is not a
// rejection and needs no second reviewer.
func (m *Module) ReportProhibitedID(ctx context.Context, reviewerRef, caseID, evidenceID string) error {
	if validRef(reviewerRef) != nil || validRef(caseID) != nil || validRef(evidenceID) != nil {
		return ErrNotFound
	}
	c, err := scanCase(m.pool.QueryRow(ctx, `SELECT `+caseCols+` FROM verification_case WHERE id = $1`, caseID))
	if err != nil {
		return err
	}
	if c.Subject == reviewerRef {
		return ErrSelfReview
	}
	if c.RoundState != roundChecking {
		return ErrState
	}
	e, err := scanEvidence(m.pool.QueryRow(ctx, `SELECT `+evidenceCols+` FROM evidence WHERE id = $1 AND case_id = $2 AND round = $3`, evidenceID, caseID, c.Round))
	if err != nil {
		return err
	}
	if e.State != "stored" {
		return ErrEvidenceGone
	}
	// Key and object first, so the document is unreadable before anything else happens.
	if err := m.destroyEvidence(ctx, e, "prohibited_id"); err != nil {
		return err
	}
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		c, err := lockCaseByID(ctx, tx, caseID)
		if err != nil {
			return err
		}
		now := m.now().UTC()
		a := reviewer(reviewerRef)
		if err := markEvidence(ctx, tx, e.ID, "deleted", "prohibited_id", now); err != nil {
			return err
		}
		if err := m.auditEvent(ctx, tx, a, "verification.prohibited_id_deleted", c, "prohibited_id"); err != nil {
			return err
		}
		if c.RoundState != roundChecking {
			return nil // already moved on; the document is gone and the deletion is audited
		}
		if _, err := tx.Exec(ctx, `INSERT INTO decision (id, case_id, subject_ref, round, reviewer_ref, outcome, stage, reason_code, created_at)
			VALUES ($1,$2,$3,$4,$5,'prohibited_id_received','final','prohibited_id',$6)`, ids.New(), c.ID, c.Subject, c.Round, reviewerRef, now); err != nil {
			return err
		}
		c.InfoRequested = "prohibited_id"
		c.PendingOutcome, c.PendingBy, c.PendingReason = "", nil, ""
		if err := m.setRound(ctx, tx, a, c, roundNeedsInfo, "verification.info_requested", "prohibited_id"); err != nil {
			return err
		}
		return m.save(ctx, tx, c)
	})
}

// Suspend suspends a verified professional at once (RG-07, E-VER-03): for example when
// the register shows a suspension. Listing, booking and prescribing stop through the
// gate and the status event. It only ever reduces access, so it needs no second reviewer;
// reinstatement needs a new verification decision.
func (m *Module) Suspend(ctx context.Context, actorRef, subjectRef, reasonCode string) error {
	return m.suspend(ctx, KindDoctor, actorRef, subjectRef, reasonCode)
}

func (m *Module) suspend(ctx context.Context, kind, actorRef, subjectRef, reasonCode string) error {
	if validRef(actorRef) != nil || validRef(subjectRef) != nil {
		return ErrNotFound
	}
	if !slices.Contains(m.cfg.ReasonCodes, reasonCode) {
		return ErrReasonRequired
	}
	if actorRef == subjectRef {
		return ErrSelfReview
	}
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		c, err := lockCaseBySubject(ctx, tx, kind, subjectRef)
		if err != nil {
			return err
		}
		if c.Status != public.StatusVerified && c.Status != public.StatusReVerifyDue {
			return ErrState
		}
		if _, err := tx.Exec(ctx, `INSERT INTO decision (id, case_id, subject_ref, round, reviewer_ref, outcome, stage, reason_code, created_at)
			VALUES ($1,$2,$3,$4,$5,'suspended','final',$6,$7)`, ids.New(), c.ID, c.Subject, c.Round, actorRef, reasonCode, m.now().UTC()); err != nil {
			return err
		}
		if err := m.setStatus(ctx, tx, reviewer(actorRef), c, public.StatusSuspended, "verification.suspended", reasonCode); err != nil {
			return err
		}
		return m.save(ctx, tx, c)
	})
}
