package notifications

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// Erasure consumers (K-DP-07, D-32). Events come from records:
// rights.erasure_requested (step 1), rights.erasure_due (step 2), rights.erasure_cancelled.
// Each handler is idempotent (outbox.Once) and reports rights.task_done in the same
// transaction. Retention: preferences RET-01 (DEL-ERASE), dispatches and delivery logs
// RET-10 (DEL-ROLL) with no legal hold, so step 1 deletes them at once.

var reErasureRef = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func erasureRefs(e outbox.Event) (req, subject string, err error) {
	req, subject = e.Payload["requestRef"], e.Payload["subjectRef"]
	if !reErasureRef.MatchString(req) || !reErasureRef.MatchString(subject) {
		return "", "", fmt.Errorf("notifications: erasure event needs requestRef and subjectRef")
	}
	return req, subject, nil
}

func (m *Module) reportTask(ctx context.Context, tx pgx.Tx, subject, req, outcome string) error {
	return outbox.Emit(ctx, tx, outbox.Event{Type: "rights.task_done", SubjectRef: subject,
		Payload: map[string]string{"requestRef": req, "module": "notifications", "outcome": outcome}})
}

// OnErasureRequested is step 1: delete preferences, pending dispatches and delivery logs.
// Reports "deleted".
func (m *Module) OnErasureRequested(ctx context.Context, e outbox.Event) error {
	req, subject, err := erasureRefs(e)
	if err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "notifications.erasure_requested", e, func(tx pgx.Tx) error {
		// Keep the queued "erasure requested" notice (reason data_rights) so the person is
		// told; step 2 sweeps it (TM-33).
		if _, err := eraseUserTx(ctx, tx, subject, true); err != nil {
			return err
		}
		return m.reportTask(ctx, tx, subject, req, "deleted")
	})
}

// OnErasureDue is step 2: nothing is held here, but anything written for the subject since
// step 1 (for example a booking notice) is swept. Reports "erased" when it removed
// something, otherwise "none".
func (m *Module) OnErasureDue(ctx context.Context, e outbox.Event) error {
	req, subject, err := erasureRefs(e)
	if err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "notifications.erasure_due", e, func(tx pgx.Tx) error {
		n, err := eraseUserTx(ctx, tx, subject, false)
		if err != nil {
			return err
		}
		out := "none"
		if n > 0 {
			out = "erased"
		}
		return m.reportTask(ctx, tx, subject, req, out)
	})
}

// OnErasureCancelled: deleted preferences and logs cannot be restored; the user gets the
// defaults again. Nothing to undo and nothing to report (records ignores late reports).
func (m *Module) OnErasureCancelled(ctx context.Context, e outbox.Event) error {
	if _, _, err := erasureRefs(e); err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "notifications.erasure_cancelled", e, func(pgx.Tx) error { return nil })
}
