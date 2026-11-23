package reviews

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// Erasure consumers (K-DP-07, D-32). Events come from records: rights.erasure_requested
// (step 1), rights.erasure_due (step 2), rights.erasure_cancelled. Each handler is idempotent
// (outbox.Once) and reports rights.task_done in the same transaction. Reviews, their sealed text and
// the visit read model are kept "until withdrawn or erased" (data inventory 4.13, DEL-ERASE), with no
// legal hold, so step 1 deletes them at once and fixes the doctors' aggregates; step 2 sweeps what
// arrived since (for example a late appointment.completed).

func erasureRefs(e outbox.Event) (req, subject string, err error) {
	req, subject = e.Payload["requestRef"], e.Payload["subjectRef"]
	if !ids.Valid(req) || !ids.Valid(subject) {
		return "", "", fmt.Errorf("reviews: erasure event needs requestRef and subjectRef")
	}
	return req, subject, nil
}

func reportTask(ctx context.Context, tx pgx.Tx, subject, req, outcome string) error {
	return outbox.Emit(ctx, tx, outbox.Event{Type: "rights.task_done", SubjectRef: subject,
		Payload: map[string]string{"requestRef": req, "module": "reviews", "outcome": outcome}})
}

// eraseSubject deletes the subject's reviews (taking each out of its doctor's aggregate) and then
// their visit rows. It returns how many rows it removed.
func eraseSubject(ctx context.Context, tx pgx.Tx, subject string) (int, error) {
	type adj struct {
		doctor   string
		n, total int
	}
	rows, err := tx.Query(ctx, `WITH d AS (DELETE FROM review WHERE patient_ref=$1 RETURNING doctor_ref, rating)
		SELECT doctor_ref::text, count(*)::int, sum(rating)::int FROM d GROUP BY doctor_ref`, subject)
	if err != nil {
		return 0, err
	}
	var adjs []adj
	for rows.Next() {
		var a adj
		if err := rows.Scan(&a.doctor, &a.n, &a.total); err != nil {
			rows.Close()
			return 0, err
		}
		adjs = append(adjs, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	removed := 0
	for _, a := range adjs {
		if _, err := tx.Exec(ctx, `UPDATE doctor_rating SET n = n - $2, sum = sum - $3 WHERE doctor_ref=$1`, a.doctor, a.n, a.total); err != nil {
			return 0, err
		}
		removed += a.n
	}
	tag, err := tx.Exec(ctx, `DELETE FROM visit WHERE patient_ref=$1`, subject)
	if err != nil {
		return 0, err
	}
	return removed + int(tag.RowsAffected()), nil
}

// OnErasureRequested is step 1: delete the subject's reviews, text and visit rows. Reports "deleted".
func (m *Module) OnErasureRequested(ctx context.Context, e outbox.Event) error {
	req, subject, err := erasureRefs(e)
	if err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "reviews.erasure_requested", e, func(tx pgx.Tx) error {
		if _, err := eraseSubject(ctx, tx, subject); err != nil {
			return err
		}
		return reportTask(ctx, tx, subject, req, "deleted")
	})
}

// OnErasureDue is step 2: sweep anything written for the subject since step 1. Reports "erased"
// when it removed something, otherwise "none".
func (m *Module) OnErasureDue(ctx context.Context, e outbox.Event) error {
	req, subject, err := erasureRefs(e)
	if err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "reviews.erasure_due", e, func(tx pgx.Tx) error {
		n, err := eraseSubject(ctx, tx, subject)
		if err != nil {
			return err
		}
		out := "none"
		if n > 0 {
			out = "erased"
		}
		return reportTask(ctx, tx, subject, req, out)
	})
}

// OnErasureCancelled: deleted reviews cannot be restored; there is nothing to undo and nothing to report.
func (m *Module) OnErasureCancelled(ctx context.Context, e outbox.Event) error {
	if _, _, err := erasureRefs(e); err != nil {
		return err
	}
	return outbox.Once(ctx, m.pool, "reviews.erasure_cancelled", e, func(pgx.Tx) error { return nil })
}
