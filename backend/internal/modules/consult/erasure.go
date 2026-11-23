package consult

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// Erasure consumers (K-DP-07, D-32). Consult sessions, pre-join records, participant events,
// identity confirmations and mode changes are part of the teleconsultation record kept
// under RET-02 (data inventory section 4.7, DEL-RESTRICT), so nothing is deleted here.
// Step 1 reports "restricted" with RET-02 (the patient's access is already closed by the
// records account restriction); step 2 reports "none" because RET-02 runs its own course.
// Cancel has nothing to undo. Participant references (RET-12, DEL-TTL) are opaque and
// expire on their own schedule.

func erasureRefs(e outbox.Event) (req, subject string, err error) {
	req, subject = e.Payload["requestRef"], e.Payload["subjectRef"]
	if !ids.Valid(req) || !ids.Valid(subject) {
		return "", "", fmt.Errorf("consult: erasure event needs requestRef and subjectRef")
	}
	return req, subject, nil
}

func reportTask(ctx context.Context, tx pgx.Tx, subject, req, outcome, rule string) error {
	p := map[string]string{"requestRef": req, "module": "consult", "outcome": outcome}
	if rule != "" {
		p["retentionRule"] = rule
	}
	return outbox.Emit(ctx, tx, outbox.Event{Type: "rights.task_done", SubjectRef: subject, Payload: p})
}

// OnErasureRequested is step 1: consult records are restricted under RET-02.
func (m *Module) OnErasureRequested(ctx context.Context, e outbox.Event) error {
	req, subject, err := erasureRefs(e)
	if err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "consult.erasure_requested", e, func(tx pgx.Tx) error {
		return reportTask(ctx, tx, subject, req, "restricted", "RET-02")
	})
}

// OnErasureDue is step 2: RET-02 records are not deleted here; reports "none".
func (m *Module) OnErasureDue(ctx context.Context, e outbox.Event) error {
	req, subject, err := erasureRefs(e)
	if err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "consult.erasure_due", e, func(tx pgx.Tx) error {
		return reportTask(ctx, tx, subject, req, "none", "")
	})
}

// OnErasureCancelled: nothing was changed in step 1, so there is nothing to undo.
func (m *Module) OnErasureCancelled(ctx context.Context, e outbox.Event) error {
	if _, _, err := erasureRefs(e); err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "consult.erasure_cancelled", e, func(pgx.Tx) error { return nil })
}
