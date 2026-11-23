package admin

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// Erasure consumers (K-DP-07, D-32). Events come from records: rights.erasure_requested
// (step 1), rights.erasure_due (step 2), rights.erasure_cancelled. Each handler is idempotent
// (outbox.Once) and reports rights.task_done in the same transaction.
//
// Feedback (S-189, data inventory 4.11, DEL-ERASE) has no legal hold, so step 1 deletes it at
// once and step 2 sweeps anything written since. Grievances the person filed, the CH-12 cases,
// the evidence trail and the staff-access records are kept under RET-09 and RET-06 (restricted,
// not deleted); records reports those two rules for the account, and this module reports
// "restricted" with RET-09 itself only when that is all it holds for the person.

func erasureRefs(e outbox.Event) (req, subject string, err error) {
	req, subject = e.Payload["requestRef"], e.Payload["subjectRef"]
	if !ids.Valid(req) || !ids.Valid(subject) {
		return "", "", fmt.Errorf("admin: erasure event needs requestRef and subjectRef")
	}
	return req, subject, nil
}

func reportTask(ctx context.Context, tx pgx.Tx, subject, req, outcome, rule string) error {
	p := map[string]string{"requestRef": req, "module": "admin", "outcome": outcome}
	if rule != "" {
		p["retentionRule"] = rule
	}
	return outbox.Emit(ctx, tx, outbox.Event{Type: "rights.task_done", SubjectRef: subject, Payload: p})
}

func eraseFeedback(ctx context.Context, tx pgx.Tx, subject string) (int, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM feedback WHERE user_ref=$1`, subject)
	return int(tag.RowsAffected()), err
}

// OnErasureRequested is step 1: delete the subject's feedback. Reports "deleted"; when there
// was no feedback but the person has grievances, reports "restricted" under RET-09.
func (m *Module) OnErasureRequested(ctx context.Context, e outbox.Event) error {
	req, subject, err := erasureRefs(e)
	if err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "admin.erasure_requested", e, func(tx pgx.Tx) error {
		n, err := eraseFeedback(ctx, tx, subject)
		if err != nil {
			return err
		}
		// One rule (review of S-189): grievances kept under RET-09 decide the report, whether or
		// not feedback was deleted too; otherwise "deleted" when feedback went, else "none".
		var kept bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM grievance WHERE submitter_ref=$1)`, subject).Scan(&kept); err != nil {
			return err
		}
		switch {
		case kept:
			return reportTask(ctx, tx, subject, req, "restricted", "RET-09")
		case n > 0:
			return reportTask(ctx, tx, subject, req, "deleted", "")
		}
		return reportTask(ctx, tx, subject, req, "none", "")
	})
}

// OnErasureDue is step 2: sweep feedback written since step 1. Reports "erased" when it
// removed something, otherwise "none".
func (m *Module) OnErasureDue(ctx context.Context, e outbox.Event) error {
	req, subject, err := erasureRefs(e)
	if err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "admin.erasure_due", e, func(tx pgx.Tx) error {
		n, err := eraseFeedback(ctx, tx, subject)
		if err != nil {
			return err
		}
		out := "none"
		if n > 0 {
			out = "erased"
		}
		return reportTask(ctx, tx, subject, req, out, "")
	})
}

// OnErasureCancelled: deleted feedback cannot be restored; nothing to undo or report.
func (m *Module) OnErasureCancelled(ctx context.Context, e outbox.Event) error {
	if _, _, err := erasureRefs(e); err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "admin.erasure_cancelled", e, func(pgx.Tx) error { return nil })
}
