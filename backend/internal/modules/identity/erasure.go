package identity

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// Erasure consumers (K-DP-07, D-32; data inventory section 4.1, RET-01 and RET-18).
//
// Step 1 closes the account at once: every session and device is revoked, push tokens are
// removed, and the account is marked closed, which frees the phone blind index for a new
// account (the unique index covers active accounts only). Registration data (encrypted
// phone, profile, roles, device keys) is kept restricted under the RET-18 legal-minimum
// hold; closed accounts cannot sign in. Reported as "restricted" with RET-18.
// Step 2 (hold ended) deletes the account's rows; the patient key destruction by records
// makes anything left in backups unreadable. Reported as "erased".
// Cancel inside the undo window reopens the account when no new account has taken the
// phone number meanwhile. Revoked sessions and devices are NOT restored: the person
// signs in again and binds the device again (K-SEC-03).

var reErasureRef = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func erasureRefs(e outbox.Event) (req, subject string, err error) {
	req, subject = e.Payload["requestRef"], e.Payload["subjectRef"]
	if !reErasureRef.MatchString(req) || !reErasureRef.MatchString(subject) {
		return "", "", fmt.Errorf("identity: erasure event needs requestRef and subjectRef")
	}
	return req, subject, nil
}

func reportTask(ctx context.Context, tx pgx.Tx, subject, req, outcome, rule string) error {
	p := map[string]string{"requestRef": req, "module": "identity", "outcome": outcome}
	if rule != "" {
		p["retentionRule"] = rule
	}
	return outbox.Emit(ctx, tx, outbox.Event{Type: "rights.task_done", SubjectRef: subject, Payload: p})
}

// OnErasureRequested is step 1.
func (m *Module) OnErasureRequested(ctx context.Context, e outbox.Event) error {
	req, subject, err := erasureRefs(e)
	if err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "identity.erasure_requested", e, func(tx pgx.Tx) error {
		now := m.now().UTC()
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_account WHERE id=$1)`, subject).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return reportTask(ctx, tx, subject, req, "none", "")
		}
		for _, q := range []string{
			`UPDATE session SET revoked_at=$2, revoke_reason='account_closed' WHERE user_id=$1 AND revoked_at IS NULL`,
			`UPDATE device SET status='revoked', revoked_at=$2, push_token_ct=NULL, push_token_updated_at=NULL WHERE user_id=$1 AND status <> 'revoked'`,
			`UPDATE user_account SET status='closed', closed_at=$2 WHERE id=$1 AND status <> 'closed'`,
		} {
			if _, err := tx.Exec(ctx, q, subject, now); err != nil {
				return err
			}
		}
		return reportTask(ctx, tx, subject, req, "restricted", "RET-18")
	})
}

// OnErasureDue is step 2: delete the account's rows once the hold has ended.
func (m *Module) OnErasureDue(ctx context.Context, e outbox.Event) error {
	req, subject, err := erasureRefs(e)
	if err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "identity.erasure_due", e, func(tx pgx.Tx) error {
		// Children first (foreign keys), then the account itself.
		for _, q := range []string{
			`DELETE FROM session WHERE user_id=$1`,
			`DELETE FROM device WHERE user_id=$1`,
			`DELETE FROM mfa_factor WHERE user_id=$1`,
			`DELETE FROM role_assignment WHERE user_id=$1`,
			`DELETE FROM profile WHERE user_id=$1`,
		} {
			if _, err := tx.Exec(ctx, q, subject); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `DELETE FROM user_account WHERE id=$1`, subject)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return reportTask(ctx, tx, subject, req, "none", "")
		}
		return reportTask(ctx, tx, subject, req, "erased", "")
	})
}

// OnErasureCancelled reopens the account closed by step 1, unless another active account
// now holds the same phone number (then it stays closed and the person signs up again).
// Nothing is reported: records ignores task reports for an undone request.
func (m *Module) OnErasureCancelled(ctx context.Context, e outbox.Event) error {
	req, subject, err := erasureRefs(e)
	if err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "identity.erasure_cancelled", e, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE user_account u SET status='active', closed_at=NULL
			WHERE u.id=$1 AND u.status='closed'
			  AND NOT EXISTS (SELECT 1 FROM user_account o WHERE o.phone_bidx=u.phone_bidx AND o.status='active')`, subject)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			m.log.Warn("identity.erasure_reopen_skipped", "resource_ref", req)
		}
		return nil
	})
}
