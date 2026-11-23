package payments

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// Erasure consumers (K-DP-07, D-32). Every payments record (orders, refunds, ledger,
// invoices, webhooks) is a financial record kept 8 years (RET-04, DEL-RESTRICT, data
// inventory section 4.5), so nothing is deleted here. Step 1 reports "restricted" with
// RET-04 (patient access is already closed by records' account restriction); step 2
// reports "none" because RET-04 runs its own course. Cancel has nothing to undo.

var reErasureRef = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func erasureRefs(e outbox.Event) (req, subject string, err error) {
	req, subject = e.Payload["requestRef"], e.Payload["subjectRef"]
	if !reErasureRef.MatchString(req) || !reErasureRef.MatchString(subject) {
		return "", "", fmt.Errorf("payments: erasure event needs requestRef and subjectRef")
	}
	return req, subject, nil
}

func reportTask(ctx context.Context, tx pgx.Tx, subject, req, outcome, rule string) error {
	p := map[string]string{"requestRef": req, "module": "payments", "outcome": outcome}
	if rule != "" {
		p["retentionRule"] = rule
	}
	return outbox.Emit(ctx, tx, outbox.Event{Type: "rights.task_done", SubjectRef: subject, Payload: p})
}

// OnErasureRequested is step 1: financial records are restricted under RET-04.
func (m *Module) OnErasureRequested(ctx context.Context, e outbox.Event) error {
	req, subject, err := erasureRefs(e)
	if err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "payments.erasure_requested", e, func(tx pgx.Tx) error {
		return reportTask(ctx, tx, subject, req, "restricted", "RET-04")
	})
}

// OnErasureDue is step 2: RET-04 records are not deleted here; reports "none".
func (m *Module) OnErasureDue(ctx context.Context, e outbox.Event) error {
	req, subject, err := erasureRefs(e)
	if err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "payments.erasure_due", e, func(tx pgx.Tx) error {
		return reportTask(ctx, tx, subject, req, "none", "")
	})
}

// OnErasureCancelled: nothing was changed in step 1, so there is nothing to undo.
func (m *Module) OnErasureCancelled(ctx context.Context, e outbox.Event) error {
	if _, _, err := erasureRefs(e); err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "payments.erasure_cancelled", e, func(pgx.Tx) error { return nil })
}
