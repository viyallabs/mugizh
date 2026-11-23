package booking

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// Erasure consumers (K-DP-07, D-32; data inventory section 4.3).
//
// Every appointment row carries the fee, currency, settlement mode and refund policy copies
// (RET-04, DEL-RESTRICT) and, once a consultation happened, the consult facts (RET-02), so
// appointment rows are kept restricted, never deleted. Step 1 cancels the subject's future
// appointments (byWhom "system" with rule RF04, so payments refunds in full) and reports
// "restricted" with RET-04 (or "none" when the subject has no appointment). Step 2 deletes
// what has no legal basis to stay (reconfirmation tasks, RET-01 DEL-ERASE; leftover holds)
// and reports "erased" or "none".

var reErasureRef = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func erasureRefs(e outbox.Event) (req, subject string, err error) {
	req, subject = e.Payload["requestRef"], e.Payload["subjectRef"]
	if !reErasureRef.MatchString(req) || !reErasureRef.MatchString(subject) {
		return "", "", fmt.Errorf("booking: erasure event needs requestRef and subjectRef")
	}
	return req, subject, nil
}

func reportTask(ctx context.Context, tx pgx.Tx, subject, req, outcome, rule string) error {
	p := map[string]string{"requestRef": req, "module": "booking", "outcome": outcome}
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
	return outbox.Once(ctx, m.pool, "booking.erasure_requested", e, func(tx pgx.Tx) error {
		now := m.now()
		rows, err := tx.Query(ctx, `SELECT id::text FROM appointment WHERE patient_ref=$1 ORDER BY start_at, id`, subject)
		if err != nil {
			return err
		}
		var apptIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			apptIDs = append(apptIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range apptIDs {
			a, err := loadAppt(ctx, tx, id, true)
			if err != nil {
				return err
			}
			if !m.cancellable(a, now) {
				continue // past or finished: kept restricted
			}
			if a.State == StateConfirmed {
				err = m.cancelConfirmedTx(ctx, tx, a, "system", RuleClinicOrDoctorCancelled, SystemActor, "system", "", now)
			} else {
				err = m.cancelUnpaidTx(ctx, tx, a, "system", SystemActor, "system", "")
			}
			if err != nil {
				return err
			}
		}
		if len(apptIDs) == 0 {
			return reportTask(ctx, tx, subject, req, "none", "")
		}
		return reportTask(ctx, tx, subject, req, "restricted", "RET-04")
	})
}

// OnErasureDue is step 2.
func (m *Module) OnErasureDue(ctx context.Context, e outbox.Event) error {
	req, subject, err := erasureRefs(e)
	if err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "booking.erasure_due", e, func(tx pgx.Tx) error {
		var n int64
		for _, q := range []string{
			`DELETE FROM reconfirmation_task WHERE appointment_id IN (SELECT id FROM appointment WHERE patient_ref=$1)`,
			`DELETE FROM hold WHERE patient_ref=$1`,
		} {
			t, err := tx.Exec(ctx, q, subject)
			if err != nil {
				return err
			}
			n += t.RowsAffected()
		}
		out := "none"
		if n > 0 {
			out = "erased"
		}
		return reportTask(ctx, tx, subject, req, out, "")
	})
}

// OnErasureCancelled: cancelled appointments are not restored (the slot may be gone and a
// refund may be paid); the patient books again. Nothing to report.
func (m *Module) OnErasureCancelled(ctx context.Context, e outbox.Event) error {
	if _, _, err := erasureRefs(e); err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "booking.erasure_cancelled", e, func(pgx.Tx) error { return nil })
}
