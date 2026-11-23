package booking

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Clinic closure for a day (S-145, E-BK-06). A clinic admin closes the whole clinic, or one
// branch, for one local date and chooses what happens to the appointments of that day:
//
//   - ClosureCancel: each confirmed appointment is cancelled by the clinic through the same
//     path as a staff cancellation (appointment.cancelled with byWhom "clinic" and rule RF04;
//     payments applies the stored refund policy, K-PAY-05, and notifications tells the
//     patient). Unpaid holds of that day are released.
//   - ClosureReschedule: each confirmed appointment is flagged needs_reschedule and
//     appointment.reschedule_needed is emitted; the patient picks a new time with a free
//     reschedule (the existing flow).
//
// Open slots of the day are removed in both modes. Appointments already in progress,
// completed, no-show, cancelled or in the past are never touched. The closure row is the unit
// of progress: it is created in one transaction with the slot changes, each appointment is
// then handled in its own transaction, and a closure still 'processing' is simply run again
// (ProcessClosure, ResumeClosures) until it is 'done'. The same closure twice is one closure.
// The booking module sends no notification and creates no refund itself.

// Closure modes and states.
const (
	ClosureCancel     = "cancel"
	ClosureReschedule = "reschedule"

	ClosureProcessing = "processing"
	ClosureDone       = "done"
)

var (
	ProblemBadClosure          = problem.New(http.StatusUnprocessableEntity, "booking.invalid_closure", "The closure is not valid")
	ProblemClosureModeConflict = problem.New(http.StatusConflict, "booking.closure_mode_conflict", "The clinic is already closed on that date with the other choice")
)

// ClosureInput is a request to close a clinic or one branch for a local date.
type ClosureInput struct {
	BranchRef string // empty = every branch of the clinic
	Date      string // "2006-01-02", read in each practice's own zone
	Mode      string // ClosureCancel or ClosureReschedule
}

// Closure is the stored record.
type Closure struct {
	ID             string
	ClinicRef      string
	BranchRef      string
	Date           string
	Mode           string
	State          string
	CancelledCount int
	FlaggedCount   int
	CreatedAt      time.Time
	CompletedAt    *time.Time
}

const closureCols = `id, clinic_ref, COALESCE(branch_ref::text,''), to_char(closure_date,'YYYY-MM-DD'), mode, state, cancelled_count, flagged_count, created_at, completed_at`

func scanClosure(row pgx.Row) (Closure, error) {
	var c Closure
	err := row.Scan(&c.ID, &c.ClinicRef, &c.BranchRef, &c.Date, &c.Mode, &c.State, &c.CancelledCount, &c.FlaggedCount, &c.CreatedAt, &c.CompletedAt)
	c.CreatedAt = c.CreatedAt.UTC()
	if c.CompletedAt != nil {
		t := c.CompletedAt.UTC()
		c.CompletedAt = &t
	}
	return c, err
}

// closureAdmin allows the clinic admin of exactly this clinic. Front desk of the clinic is
// read-only (a closure cancels paid appointments and starts refunds, a decision for the
// clinic's administrator); everyone else gets not found (K-SEC-04).
func closureAdmin(a Actor, clinicRef string) error {
	if !ids.Valid(clinicRef) || a.UserID == "" || a.ClinicRef == "" || a.ClinicRef != clinicRef {
		return problem.NotFound
	}
	switch a.Role {
	case ip.RoleClinicAdmin:
		return nil
	case ip.RoleFrontDesk:
		return problem.Forbidden
	}
	return problem.NotFound
}

// checkBranch confirms the branch belongs to the clinic. Another clinic's branch and an
// unknown one answer the same (K-SEC-04).
func (m *Module) checkBranch(ctx context.Context, clinicRef, branchRef string) error {
	if !ids.Valid(branchRef) {
		return problem.NotFound
	}
	var br dp.BranchReader
	if m.d.Branches != nil {
		br = m.d.Branches
	} else if r, ok := m.d.Directory.(dp.BranchReader); ok {
		br = r
	}
	if br != nil {
		b, err := br.GetBranch(ctx, branchRef)
		if errors.Is(err, dp.ErrNotFound) {
			return problem.NotFound
		}
		if err != nil {
			return err
		}
		if b.ClinicRef != clinicRef {
			return problem.NotFound
		}
		return nil
	}
	// No directory reader: fall back to the branches booking has seen on availability rows.
	var ok bool
	if err := m.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM availability WHERE clinic_ref=$1 AND branch_ref=$2)`, clinicRef, branchRef).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return problem.NotFound
	}
	return nil
}

func nullUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CloseClinic records the closure and applies it. It is idempotent: asking again for the same
// clinic (or branch) and date returns the same closure and changes nothing that is already
// done; asking again with the other mode is refused. In cancel mode the appointments are
// cancelled one by one; if that stops half way the closure stays 'processing' and
// ResumeClosures (or the same request again) finishes it.
func (m *Module) CloseClinic(ctx context.Context, a Actor, clinicRef string, in ClosureInput) (Closure, error) {
	if err := closureAdmin(a, clinicRef); err != nil {
		return Closure{}, err
	}
	if in.Mode != ClosureCancel && in.Mode != ClosureReschedule {
		return Closure{}, ProblemBadClosure
	}
	day, err := time.Parse("2006-01-02", in.Date)
	now := m.now()
	today := now.UTC().Truncate(24 * time.Hour)
	// The date is local to the practice, which can be a day ahead of or behind UTC: accept
	// from two days back (everything on it is past, so it changes nothing) to a year ahead.
	if err != nil || day.Before(today.AddDate(0, 0, -2)) || day.After(today.AddDate(1, 0, 0)) {
		return Closure{}, ProblemBadClosure
	}
	if in.BranchRef != "" {
		if err := m.checkBranch(ctx, clinicRef, in.BranchRef); err != nil {
			return Closure{}, err
		}
	}
	var out Closure
	err = m.inTx(ctx, func(tx pgx.Tx) error {
		id := ids.New()
		tag, err := tx.Exec(ctx, `INSERT INTO clinic_closure (id, clinic_ref, branch_ref, closure_date, mode, created_by, created_at)
			VALUES ($1,$2,$3,$4::date,$5,$6,$7) ON CONFLICT DO NOTHING`, id, clinicRef, nullUUID(in.BranchRef), in.Date, in.Mode, a.UserID, now)
		if err != nil {
			return err
		}
		created := tag.RowsAffected() == 1
		out, err = scanClosure(tx.QueryRow(ctx, `SELECT `+closureCols+` FROM clinic_closure
			WHERE clinic_ref=$1 AND COALESCE(branch_ref,'00000000-0000-0000-0000-000000000000'::uuid)=COALESCE($2::uuid,'00000000-0000-0000-0000-000000000000'::uuid)
			AND closure_date=$3::date FOR UPDATE`, clinicRef, nullUUID(in.BranchRef), in.Date))
		if err != nil {
			return err
		}
		if out.Mode != in.Mode {
			return ProblemClosureModeConflict
		}
		if created {
			if err := ap.Record(ctx, tx, ap.Event{ActorRef: a.UserID, ActorRole: a.Role, Action: "booking.clinic_closure.create", SubjectRef: "",
				ResourceType: "clinic_closure", ResourceRef: out.ID, Result: ap.Allowed, RequestID: a.RequestID}); err != nil {
				return err
			}
		}
		if out.State == ClosureDone && !created {
			return nil // already applied: nothing to do
		}
		// Remove the day's open slots and flag the appointments that no longer fit. In
		// reschedule mode this emits reschedule_needed for confirmed ones; in cancel mode it is
		// quiet, because those appointments are cancelled next.
		rows, err := tx.Query(ctx, `SELECT practice_id FROM availability WHERE clinic_ref=$1 AND ($2::uuid IS NULL OR branch_ref=$2::uuid) ORDER BY practice_id`, clinicRef, nullUUID(in.BranchRef))
		if err != nil {
			return err
		}
		var practices []string
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return err
			}
			practices = append(practices, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, p := range practices {
			if err := m.reconcileOpt(ctx, tx, p, now, in.Mode == ClosureCancel); err != nil {
				return err
			}
		}
		if in.Mode == ClosureReschedule {
			var flagged int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM appointment a JOIN availability av ON av.practice_id=a.practice_id
				JOIN clinic_closure c ON c.id=$1 AND av.clinic_ref=c.clinic_ref AND (c.branch_ref IS NULL OR av.branch_ref=c.branch_ref)
				WHERE a.clinic_ref=c.clinic_ref AND a.state='confirmed' AND a.needs_reschedule AND `+closureWindow, out.ID).Scan(&flagged); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE clinic_closure SET state='done', completed_at=$2, flagged_count=$3 WHERE id=$1`, out.ID, now, flagged); err != nil {
				return err
			}
			out.State, out.FlaggedCount = ClosureDone, flagged
			t := now.UTC()
			out.CompletedAt = &t
		}
		return nil
	})
	if err != nil {
		return Closure{}, err
	}
	if out.State == ClosureProcessing {
		if err := m.ProcessClosure(ctx, out.ID); err != nil {
			m.log.Error("booking.closure_incomplete", logging.Err(err))
			// Not an error to the caller: the closure is recorded and ResumeClosures finishes it.
		}
		return m.getClosure(ctx, out.ID)
	}
	return out, nil
}

func (m *Module) getClosure(ctx context.Context, id string) (Closure, error) {
	return scanClosure(m.pool.QueryRow(ctx, `SELECT `+closureCols+` FROM clinic_closure WHERE id=$1`, id))
}

// closureWindow restricts an appointment query (aliases a, av, c) to the closure's local day
// in the practice's zone, from now on.
const closureWindow = `a.start_at >= (c.closure_date::timestamp AT TIME ZONE av.time_zone) AND a.start_at < ((c.closure_date + 1)::timestamp AT TIME ZONE av.time_zone)`

// closureCandidates lists appointments the closure still has to handle: confirmed ones and
// unpaid holds of that clinic and day that have not started. skip excludes ones that failed
// this run.
func (m *Module) closureCandidates(ctx context.Context, q querier, closureID string, now time.Time, skip []string, only string, limit int) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT a.id FROM appointment a JOIN availability av ON av.practice_id=a.practice_id
		JOIN clinic_closure c ON c.id=$1 AND av.clinic_ref=c.clinic_ref AND (c.branch_ref IS NULL OR av.branch_ref=c.branch_ref)
		WHERE a.clinic_ref=c.clinic_ref AND a.state IN ('confirmed','held','pending_payment') AND a.start_at > $2
		AND NOT (a.id = ANY($3::uuid[])) AND ($5::text = '' OR a.id::text = $5) AND `+closureWindow+` ORDER BY a.start_at, a.id LIMIT $4`, closureID, now, skip, limit, only)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ProcessClosure finishes a closure in cancel mode: every remaining confirmed appointment of
// that clinic and day is cancelled by the clinic, one transaction each, then the closure is
// marked done. It is safe to run again and from several workers at once: each appointment is
// locked and re-checked, so it is cancelled exactly once. One appointment that cannot be
// cancelled is logged and skipped; the closure then stays 'processing' and an error is
// returned so the sweep retries it.
func (m *Module) ProcessClosure(ctx context.Context, closureID string) error {
	if !ids.Valid(closureID) {
		return problem.NotFound
	}
	c, err := m.getClosure(ctx, closureID)
	if errors.Is(err, pgx.ErrNoRows) {
		return problem.NotFound
	}
	if err != nil {
		return err
	}
	if c.State == ClosureDone {
		return nil
	}
	var createdBy string
	if err := m.pool.QueryRow(ctx, `SELECT created_by FROM clinic_closure WHERE id=$1`, closureID).Scan(&createdBy); err != nil {
		return err
	}
	failed := []string{}
	for {
		batch, err := m.closureCandidates(ctx, m.pool, closureID, m.now(), failed, "", 100)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, id := range batch {
			if err := m.cancelForClosure(ctx, c, createdBy, id); err != nil {
				m.log.Error("booking.closure_cancel_failed", logging.Err(err))
				failed = append(failed, id)
			}
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("booking: closure %s: %d appointments not handled", closureID, len(failed))
	}
	_, err = m.pool.Exec(ctx, `UPDATE clinic_closure SET state='done', completed_at=$2 WHERE id=$1 AND state='processing'`, closureID, m.now())
	return err
}

func (m *Module) cancelForClosure(ctx context.Context, c Closure, createdBy, apptID string) error {
	return m.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadAppt(ctx, tx, apptID, true)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		now := m.now()
		// Re-check under the lock: still this clinic's, still on the closed day, not started,
		// still live. Anything else was handled in the meantime and is left alone.
		if cur.ClinicRef != c.ClinicRef || !cur.StartsAt.After(now) {
			return nil
		}
		still, err := m.closureCandidates(ctx, tx, c.ID, now, []string{}, cur.ID, 1)
		if err != nil {
			return err
		}
		if len(still) == 0 {
			return nil
		}
		switch cur.State {
		case StateConfirmed:
			if err := m.cancelConfirmedTx(ctx, tx, cur, "clinic", RuleClinicOrDoctorCancelled, createdBy, ip.RoleClinicAdmin, "", now); err != nil {
				return err
			}
		case StateHeld, StatePendingPayment:
			if err := m.cancelUnpaidTx(ctx, tx, cur, "clinic", createdBy, ip.RoleClinicAdmin, ""); err != nil {
				return err
			}
		default:
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE clinic_closure SET cancelled_count = cancelled_count + 1 WHERE id=$1`, c.ID)
		return err
	})
}

// ResumeClosures finishes every closure still 'processing', for example after a crash. Run it
// every minute or so from the worker. It returns how many closures it completed.
func (m *Module) ResumeClosures(ctx context.Context) (int, error) {
	rows, err := m.pool.Query(ctx, `SELECT id FROM clinic_closure WHERE state='processing' ORDER BY created_at, id LIMIT 100`)
	if err != nil {
		return 0, err
	}
	var open []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		open = append(open, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, id := range open {
		if err := m.ProcessClosure(ctx, id); err != nil {
			m.log.Error("booking.closure_resume_failed", logging.Err(err))
			continue
		}
		n++
	}
	return n, nil
}

// ListClosures lists the clinic's closures, newest date first. Clinic admin and front desk of
// that clinic may read it; it holds counts only, no patient data.
func (m *Module) ListClosures(ctx context.Context, a Actor, clinicRef string, limit int) ([]Closure, error) {
	if !ids.Valid(clinicRef) || a.UserID == "" || a.ClinicRef != clinicRef || (a.Role != ip.RoleClinicAdmin && a.Role != ip.RoleFrontDesk) {
		return nil, problem.NotFound
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := m.pool.Query(ctx, `SELECT `+closureCols+` FROM clinic_closure WHERE clinic_ref=$1 ORDER BY closure_date DESC, id DESC LIMIT $2`, clinicRef, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Closure{}
	for rows.Next() {
		c, err := scanClosure(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
