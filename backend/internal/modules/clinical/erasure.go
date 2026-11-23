package clinical

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// Erasure consumers (K-DP-07, D-32). Notes, prescriptions and their versions are kept
// restricted under RET-02 (DEL-RESTRICT, data inventory section 4.7; hidden from the app
// by records' account restriction, reachable by the treating doctor through T2). Reminder
// links and the scheduler-core activities behind them are DEL-ERASE.
//
// Step 1 cancels every active reminder activity of the subject, asks scheduler-core to
// erase the subject when the client supports it, and reports "restricted" with RET-02.
// Step 2 asks scheduler-core to erase the subject (sweep) and reports "none"; RET-02 runs
// its own course. Step 2 fails closed (returns an error, so the delivery is retried and
// the erasure stays open) when the scheduler client cannot erase a subject: scheduler-core
// must never be left holding an erased person's schedule.

// SubjectEraser is the scheduler-core erase call (POST /v1/subjects/{subjectRef}/erase).
// The schedulercore adapter client should implement it; it is found by type assertion on
// Deps.Scheduler so this module needs no adapter change.
type SubjectEraser interface {
	EraseSubject(ctx context.Context, subjectRef string) error
}

// ErrSchedulerCannotErase means the scheduler client has no subject-erase call.
var ErrSchedulerCannotErase = errors.New("clinical: scheduler client cannot erase a subject")

var reErasureRef = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func erasureRefs(e outbox.Event) (req, subject string, err error) {
	req, subject = e.Payload["requestRef"], e.Payload["subjectRef"]
	if !reErasureRef.MatchString(req) || !reErasureRef.MatchString(subject) {
		return "", "", fmt.Errorf("clinical: erasure event needs requestRef and subjectRef")
	}
	return req, subject, nil
}

func reportTask(ctx context.Context, tx pgx.Tx, subject, req, outcome, rule string) error {
	p := map[string]string{"requestRef": req, "module": "clinical", "outcome": outcome}
	if rule != "" {
		p["retentionRule"] = rule
	}
	return outbox.Emit(ctx, tx, outbox.Event{Type: "rights.task_done", SubjectRef: subject, Payload: p})
}

// cancelSubjectReminders cancels each active activity (idempotent: an unknown activity is
// fine) and returns the external refs to mark.
func (r *Rx) cancelSubjectReminders(ctx context.Context, subject string) error {
	rows, err := r.m.pool.Query(ctx, `SELECT activity_id::text FROM reminder_link WHERE patient_ref=$1 AND state='active'`, subject)
	if err != nil {
		return err
	}
	var acts []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			rows.Close()
			return err
		}
		acts = append(acts, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, a := range acts {
		if err := r.d.Scheduler.CancelActivity(ctx, a); err != nil && adapters.KindOf(err) != adapters.NotFound {
			return err
		}
	}
	return nil
}

// OnErasureRequested is step 1. It makes calls to scheduler-core before the transaction, all
// of them safe to repeat; the outbox.Once transaction then marks the links and reports.
func (r *Rx) OnErasureRequested(ctx context.Context, e outbox.Event) error {
	req, subject, err := erasureRefs(e)
	if err != nil {
		return err
	}
	if err := r.cancelSubjectReminders(ctx, subject); err != nil {
		return err
	}
	if er, ok := r.d.Scheduler.(SubjectEraser); ok {
		if err := er.EraseSubject(ctx, subject); err != nil {
			return err
		}
	}
	return outbox.Once(ctx, r.m.pool, "clinical.erasure_requested", e, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE reminder_link SET state='cancelled', cancelled_at=$2 WHERE patient_ref=$1 AND state='active'`, subject, r.now()); err != nil {
			return err
		}
		return reportTask(ctx, tx, subject, req, "restricted", "RET-02")
	})
}

// OnErasureDue is step 2: erase the subject in scheduler-core (sweep); clinical records
// stay under RET-02, so the outcome is "none".
func (r *Rx) OnErasureDue(ctx context.Context, e outbox.Event) error {
	req, subject, err := erasureRefs(e)
	if err != nil {
		return err
	}
	er, ok := r.d.Scheduler.(SubjectEraser)
	if !ok {
		return ErrSchedulerCannotErase
	}
	if err := er.EraseSubject(ctx, subject); err != nil {
		return err
	}
	return outbox.Once(ctx, r.m.pool, "clinical.erasure_due", e, func(tx pgx.Tx) error {
		return reportTask(ctx, tx, subject, req, "none", "")
	})
}

// OnErasureCancelled: cancelled reminders are not recreated (that would mean rebuilding
// schedules from prescription content; the patient switches reminders on again, P-REMIND).
// Notes and prescriptions were never changed. Nothing to report.
func (r *Rx) OnErasureCancelled(ctx context.Context, e outbox.Event) error {
	if _, _, err := erasureRefs(e); err != nil {
		return err
	}
	return outbox.Once(ctx, r.m.pool, "clinical.erasure_cancelled", e, func(pgx.Tx) error { return nil })
}
