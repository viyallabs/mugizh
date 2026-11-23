package verification

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
)

// ReverifyResult counts what one RunReverification pass changed.
type ReverifyResult struct {
	Expired   int // licence expiry passed
	Due       int // re-verification became due
	Suspended int // grace period passed without a new decision
}

// RunReverification is the periodic job (S-160, RG-07). Idempotent and safe to run at any
// frequency. In order:
//
//  1. a licence whose expiry date has been reached becomes "expired" at once (no grace
//     on an expired licence);
//  2. a verified case whose re_verify_due_at has passed becomes "re_verify_due";
//  3. a case still verified or due when re_verify_due_at plus the grace period has passed,
//     with no new decision, becomes "suspended".
//
// Suspension emits verification.status_changed with status "suspended"; booking cancels
// future appointments and refunds from that event (E-VER-06, TC-66). The gate fails
// closed even before this job runs (see effective).
func (m *Module) RunReverification(ctx context.Context, now time.Time) (ReverifyResult, error) {
	now = now.UTC()
	var res ReverifyResult
	steps := []struct {
		name  string
		query string
		args  []any
		to    public.Status
		act   string
		why   string
		count *int
		still func(c *caseRow) bool
	}{
		{"expiry", `SELECT id FROM verification_case WHERE status IN ('verified','re_verify_due') AND licence_expires_on IS NOT NULL AND licence_expires_on <= ($1::timestamptz AT TIME ZONE 'UTC')::date`,
			[]any{now}, public.StatusExpired, "verification.expired", "licence_expired", &res.Expired,
			func(c *caseRow) bool {
				return (c.Status == public.StatusVerified || c.Status == public.StatusReVerifyDue) && c.LicenceExp != nil && !now.Before(*c.LicenceExp)
			}},
		{"due", `SELECT id FROM verification_case WHERE status = 'verified' AND re_verify_due_at <= $1`,
			[]any{now}, public.StatusReVerifyDue, "verification.reverify_due", "scheduled", &res.Due,
			func(c *caseRow) bool {
				return c.Status == public.StatusVerified && c.DueAt != nil && !now.Before(*c.DueAt)
			}},
		{"grace", `SELECT id FROM verification_case WHERE status IN ('verified','re_verify_due') AND re_verify_due_at <= $1`,
			[]any{now.Add(-m.cfg.ReverifyGrace)}, public.StatusSuspended, "verification.suspended", "grace_elapsed", &res.Suspended,
			func(c *caseRow) bool {
				return (c.Status == public.StatusVerified || c.Status == public.StatusReVerifyDue) && c.DueAt != nil && !now.Before(c.DueAt.Add(m.cfg.ReverifyGrace))
			}},
	}
	var first error
	for _, st := range steps {
		rs, err := m.pool.Query(ctx, st.query, st.args...)
		if err != nil {
			return res, fmt.Errorf("verification: reverify %s: %w", st.name, err)
		}
		var caseIDs []string
		for rs.Next() {
			var id string
			if err := rs.Scan(&id); err != nil {
				rs.Close()
				return res, err
			}
			caseIDs = append(caseIDs, id)
		}
		rs.Close()
		if err := rs.Err(); err != nil {
			return res, err
		}
		for _, id := range caseIDs {
			changed := false
			err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
				changed = false
				c, err := lockCaseByID(ctx, tx, id)
				if err != nil {
					return err
				}
				if !st.still(c) {
					return nil // another run or a reviewer got there first
				}
				if err := m.setStatus(ctx, tx, system, c, st.to, st.act, st.why); err != nil {
					return err
				}
				changed = true
				return m.save(ctx, tx, c)
			})
			if err != nil {
				m.log.Error("verification.reverify_failed", logging.Err(err))
				if first == nil {
					first = err
				}
				continue
			}
			if changed {
				*st.count++
			}
		}
	}
	return res, first
}

// TriggerReverification starts re-verification after a complaint (RG-07): the case
// becomes "re_verify_due" with the due date set to now, so the grace period starts now and
// the professional must be re-verified within it or be suspended. Calling it on a case
// already due is a no-op.
func (m *Module) TriggerReverification(ctx context.Context, actorRef, subjectRef, reasonCode string) error {
	return m.triggerReverification(ctx, KindDoctor, actorRef, subjectRef, reasonCode)
}

func (m *Module) triggerReverification(ctx context.Context, kind, actorRef, subjectRef, reasonCode string) error {
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
		switch c.Status {
		case public.StatusReVerifyDue:
			return nil
		case public.StatusVerified:
		default:
			return ErrState
		}
		now := m.now().UTC()
		if c.DueAt == nil || c.DueAt.After(now) {
			c.DueAt = &now
		}
		if err := m.setStatus(ctx, tx, reviewer(actorRef), c, public.StatusReVerifyDue, "verification.reverify_triggered", reasonCode); err != nil {
			return err
		}
		return m.save(ctx, tx, c)
	})
}
