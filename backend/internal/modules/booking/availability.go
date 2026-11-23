package booking

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// RuleInput is one weekly window in the practice's local wall time. Weekday 0 is Sunday.
type RuleInput struct {
	Weekday     int
	StartLocal  string // "15:04"
	EndLocal    string // "15:04"
	SlotMinutes int
	Mode        string
}

// Rule is a stored RuleInput.
type Rule struct {
	ID string
	RuleInput
}

// Exception is a leave or closure window.
type Exception struct {
	ID       string
	Kind     string // leave | closure
	StartsAt time.Time
	EndsAt   time.Time
}

// Availability is a practice's template and exceptions.
type Availability struct {
	PracticeID string
	TimeZone   string
	Rules      []Rule
	Exceptions []Exception
}

// Conflict is a booked appointment that no longer fits the availability (TC-23). It is kept
// and listed for rescheduling; it is never deleted.
type Conflict struct {
	AppointmentID string
	PatientRef    string
	PracticeID    string
	StartsAt      time.Time
	Mode          string
	State         string
}

// SlotInfo is a slot offered to a patient.
type SlotInfo struct {
	ID         string
	PracticeID string
	StartsAt   time.Time
	EndsAt     time.Time
	Mode       string
}

type slotSpec struct {
	Start, End time.Time
	Mode       string
}

type ruleRow struct {
	weekday          int
	startMin, endMin int
	slot             int
	mode             string
}

func parseHM(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		t, err = time.Parse("15:04:05", s)
		if err != nil {
			return 0, err
		}
	}
	return t.Hour()*60 + t.Minute(), nil
}

// generate returns the slots a template yields in (from, to), start-ordered by local date.
// It works on local wall times in loc, so a DST change moves no slot to the wrong wall
// time; a wall time that does not exist on a day (a spring-forward gap) is skipped.
func generate(loc *time.Location, rules []ruleRow, exc []Exception, from, to time.Time) []slotSpec {
	var out []slotSpec
	seen := map[int64]bool{}
	day := time.Date(from.In(loc).Year(), from.In(loc).Month(), from.In(loc).Day(), 12, 0, 0, 0, loc)
	last := to.In(loc)
	for ; !day.After(last.AddDate(0, 0, 1)); day = day.AddDate(0, 0, 1) {
		y, mo, d := day.Date()
		for _, r := range rules {
			if int(day.Weekday()) != r.weekday {
				continue
			}
			for wall := r.startMin; wall+r.slot <= r.endMin; wall += r.slot {
				start := time.Date(y, mo, d, wall/60, wall%60, 0, 0, loc)
				if start.Hour()*60+start.Minute() != wall { // wall time skipped by a clock change
					continue
				}
				end := start.Add(time.Duration(r.slot) * time.Minute)
				if !start.After(from) || !start.Before(to) {
					continue
				}
				if seen[start.Unix()] {
					continue
				}
				blocked := false
				for _, e := range exc {
					if start.Before(e.EndsAt) && end.After(e.StartsAt) {
						blocked = true
						break
					}
				}
				if blocked {
					continue
				}
				seen[start.Unix()] = true
				out = append(out, slotSpec{Start: start.UTC(), End: end.UTC(), Mode: r.mode})
			}
		}
	}
	return out
}

type practiceRefs struct{ ClinicRef, ProfessionalRef, TimeZone string }

// staffPractice resolves a practice for a doctor or clinic staff member. The doctor must own
// the practice; clinic staff must belong to its clinic. Anything else is 404 (K-SEC-04).
// Staff views use booking's own copy of the refs so a suspended doctor's day is still visible.
func (m *Module) staffPractice(ctx context.Context, a Actor, practiceID string, write bool) (practiceRefs, error) {
	if !ids.Valid(practiceID) {
		return practiceRefs{}, problem.NotFound
	}
	switch a.Role {
	case ip.RoleDoctor, ip.RoleClinicAdmin:
	case ip.RoleFrontDesk:
		if write {
			return practiceRefs{}, problem.Forbidden
		}
	default:
		return practiceRefs{}, problem.Forbidden
	}
	var r practiceRefs
	err := m.pool.QueryRow(ctx, `SELECT clinic_ref, professional_ref, time_zone FROM availability WHERE practice_id=$1`, practiceID).Scan(&r.ClinicRef, &r.ProfessionalRef, &r.TimeZone)
	if errors.Is(err, pgx.ErrNoRows) {
		p, derr := m.d.Directory.GetPractice(ctx, practiceID)
		if errors.Is(derr, dp.ErrNotFound) {
			return practiceRefs{}, problem.NotFound
		}
		if derr != nil {
			return practiceRefs{}, derr
		}
		r = practiceRefs{ClinicRef: p.ClinicRef, ProfessionalRef: p.ProfessionalRef, TimeZone: p.TimeZone}
	} else if err != nil {
		return practiceRefs{}, err
	}
	if !ownsPractice(a, r) {
		return practiceRefs{}, problem.NotFound
	}
	return r, nil
}

func ownsPractice(a Actor, r practiceRefs) bool {
	switch a.Role {
	case ip.RoleDoctor:
		return a.UserID == r.ProfessionalRef
	case ip.RoleClinicAdmin, ip.RoleFrontDesk:
		return a.ClinicRef != "" && a.ClinicRef == r.ClinicRef
	}
	return false
}

// GetAvailability returns the template and exceptions (doctor, clinic admin, front desk).
func (m *Module) GetAvailability(ctx context.Context, a Actor, practiceID string) (Availability, error) {
	r, err := m.staffPractice(ctx, a, practiceID, false)
	if err != nil {
		return Availability{}, err
	}
	out := Availability{PracticeID: practiceID, TimeZone: r.TimeZone, Rules: []Rule{}, Exceptions: []Exception{}}
	rows, err := m.pool.Query(ctx, `SELECT id, weekday, start_local::text, end_local::text, slot_minutes, mode FROM availability_rule WHERE practice_id=$1 ORDER BY weekday, start_local`, practiceID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var ru Rule
		if err := rows.Scan(&ru.ID, &ru.Weekday, &ru.StartLocal, &ru.EndLocal, &ru.SlotMinutes, &ru.Mode); err != nil {
			return out, err
		}
		ru.StartLocal, ru.EndLocal = ru.StartLocal[:5], ru.EndLocal[:5]
		out.Rules = append(out.Rules, ru)
	}
	rows.Close()
	ex, err := m.exceptions(ctx, m.pool, practiceID)
	out.Exceptions = ex
	return out, err
}

// branchOf is the branch a practice belongs to. A directory that does not name one (an older
// reader) means the clinic's main branch, whose ID is the clinic's (S-130).
func branchOf(p dp.Practice) string {
	if p.BranchRef != "" {
		return p.BranchRef
	}
	return p.ClinicRef
}

// exceptions returns a practice's leave and closure windows: the ones it recorded itself and
// the clinic or branch closures that cover it (S-145). A closure date is read in the
// practice's own zone, so a branch in another zone closes its own local day.
func (m *Module) exceptions(ctx context.Context, q querier, practiceID string) ([]Exception, error) {
	rows, err := q.Query(ctx, `SELECT id, kind, starts_at, ends_at FROM (
		SELECT id, kind, starts_at, ends_at FROM availability_exception WHERE practice_id=$1
		UNION ALL
		SELECT c.id, 'closure', (c.closure_date::timestamp AT TIME ZONE av.time_zone), ((c.closure_date + 1)::timestamp AT TIME ZONE av.time_zone)
		FROM clinic_closure c JOIN availability av ON av.practice_id=$1 AND av.clinic_ref=c.clinic_ref AND (c.branch_ref IS NULL OR c.branch_ref=av.branch_ref)
	) e ORDER BY starts_at, id`, practiceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Exception{}
	for rows.Next() {
		var e Exception
		if err := rows.Scan(&e.ID, &e.Kind, &e.StartsAt, &e.EndsAt); err != nil {
			return nil, err
		}
		e.StartsAt, e.EndsAt = e.StartsAt.UTC(), e.EndsAt.UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

func validateRules(in []RuleInput, modes []string) ([]ruleRow, error) {
	if len(in) > 100 {
		return nil, fmt.Errorf("too many rules")
	}
	offered := map[string]bool{}
	for _, md := range modes {
		offered[md] = true
	}
	var rows []ruleRow
	for _, r := range in {
		s, err1 := parseHM(r.StartLocal)
		e, err2 := parseHM(r.EndLocal)
		if err1 != nil || err2 != nil || e <= s || r.Weekday < 0 || r.Weekday > 6 || r.SlotMinutes < 5 || r.SlotMinutes > 240 || !validModes[r.Mode] || !offered[r.Mode] {
			return nil, fmt.Errorf("invalid rule")
		}
		for _, o := range rows {
			if o.weekday == r.Weekday && s < o.endMin && e > o.startMin {
				return nil, fmt.Errorf("overlapping rules")
			}
		}
		rows = append(rows, ruleRow{weekday: r.Weekday, startMin: s, endMin: e, slot: r.SlotMinutes, mode: r.Mode})
	}
	return rows, nil
}

// SetWeeklyTemplate replaces the weekly template of a practice. Open slots that no longer
// match are removed and new ones materialised; booked slots are never deleted. Appointments
// that no longer fit are flagged and returned for rescheduling (TC-23, E-BK-02).
func (m *Module) SetWeeklyTemplate(ctx context.Context, a Actor, practiceID string, in []RuleInput) ([]Conflict, error) {
	if _, err := m.staffPractice(ctx, a, practiceID, true); err != nil {
		return nil, err
	}
	// Refresh the refs and the zone from the directory: only a listed practice has availability.
	p, err := m.d.Directory.GetPractice(ctx, practiceID)
	if errors.Is(err, dp.ErrNotFound) {
		return nil, problem.NotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := loadZone(p.TimeZone); err != nil {
		return nil, problem.NotFound
	}
	rows, err := validateRules(in, p.Modes)
	if err != nil {
		return nil, ProblemBadAvailability
	}
	now := m.now()
	err = m.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO availability (practice_id, clinic_ref, branch_ref, professional_ref, time_zone, updated_at) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (practice_id) DO UPDATE SET clinic_ref=EXCLUDED.clinic_ref, branch_ref=EXCLUDED.branch_ref, professional_ref=EXCLUDED.professional_ref, time_zone=EXCLUDED.time_zone, updated_at=EXCLUDED.updated_at`,
			practiceID, p.ClinicRef, branchOf(p), p.ProfessionalRef, p.TimeZone, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM availability_rule WHERE practice_id=$1`, practiceID); err != nil {
			return err
		}
		for i, r := range in {
			if _, err := tx.Exec(ctx, `INSERT INTO availability_rule (id, practice_id, weekday, start_local, end_local, slot_minutes, mode) VALUES ($1,$2,$3,$4::text::time,$5::text::time,$6,$7)`,
				ids.New(), practiceID, rows[i].weekday, r.StartLocal, r.EndLocal, r.SlotMinutes, r.Mode); err != nil {
				return err
			}
		}
		if err := m.reconcile(ctx, tx, practiceID, now); err != nil {
			return err
		}
		return m.audit(ctx, tx, a.UserID, a.Role, "booking.availability_change", "", practiceID, a.RequestID)
	})
	if err != nil {
		return nil, err
	}
	return m.listConflicts(ctx, m.pool, practiceID, now)
}

// AddException records a leave or closure from fromDate to toDate inclusive (local dates in
// the practice zone, "2006-01-02"). Booked slots in the window are kept and flagged.
func (m *Module) AddException(ctx context.Context, a Actor, practiceID, kind, fromDate, toDate string) (Exception, []Conflict, error) {
	r, err := m.staffPractice(ctx, a, practiceID, true)
	if err != nil {
		return Exception{}, nil, err
	}
	loc, err := loadZone(r.TimeZone)
	if err != nil {
		return Exception{}, nil, problem.NotFound
	}
	f, err1 := time.ParseInLocation("2006-01-02", fromDate, loc)
	t, err2 := time.ParseInLocation("2006-01-02", toDate, loc)
	if (kind != "leave" && kind != "closure") || err1 != nil || err2 != nil || t.Before(f) || t.Sub(f) > 366*24*time.Hour {
		return Exception{}, nil, ProblemBadAvailability
	}
	ex := Exception{ID: ids.New(), Kind: kind, StartsAt: f.UTC(), EndsAt: t.AddDate(0, 0, 1).UTC()}
	now := m.now()
	err = m.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO availability_exception (id, practice_id, kind, starts_at, ends_at) VALUES ($1,$2,$3,$4,$5)`, ex.ID, practiceID, kind, ex.StartsAt, ex.EndsAt); err != nil {
			return err
		}
		if err := m.reconcile(ctx, tx, practiceID, now); err != nil {
			return err
		}
		return m.audit(ctx, tx, a.UserID, a.Role, "booking.availability_change", "", practiceID, a.RequestID)
	})
	if err != nil {
		return Exception{}, nil, err
	}
	c, err := m.listConflicts(ctx, m.pool, practiceID, now)
	return ex, c, err
}

// RemoveException deletes an exception; slots return and conflicts that cleared are unflagged.
func (m *Module) RemoveException(ctx context.Context, a Actor, practiceID, exceptionID string) error {
	if _, err := m.staffPractice(ctx, a, practiceID, true); err != nil {
		return err
	}
	if !ids.Valid(exceptionID) {
		return problem.NotFound
	}
	now := m.now()
	return m.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM availability_exception WHERE id=$1 AND practice_id=$2`, exceptionID, practiceID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return problem.NotFound
		}
		if err := m.reconcile(ctx, tx, practiceID, now); err != nil {
			return err
		}
		return m.audit(ctx, tx, a.UserID, a.Role, "booking.availability_change", "", practiceID, a.RequestID)
	})
}

// loadSchedule reads a practice's zone, rules and exceptions inside tx.
func (m *Module) loadSchedule(ctx context.Context, tx querier, practiceID string) (*time.Location, []ruleRow, []Exception, error) {
	var zone string
	if err := tx.QueryRow(ctx, `SELECT time_zone FROM availability WHERE practice_id=$1`, practiceID).Scan(&zone); err != nil {
		return nil, nil, nil, err
	}
	loc, err := loadZone(zone)
	if err != nil {
		return nil, nil, nil, err
	}
	rows, err := tx.Query(ctx, `SELECT weekday, start_local::text, end_local::text, slot_minutes, mode FROM availability_rule WHERE practice_id=$1`, practiceID)
	if err != nil {
		return nil, nil, nil, err
	}
	var rules []ruleRow
	for rows.Next() {
		var r ruleRow
		var s, e string
		if err := rows.Scan(&r.weekday, &s, &e, &r.slot, &r.mode); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		r.startMin, _ = parseHM(s)
		r.endMin, _ = parseHM(e)
		rules = append(rules, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	exc, err := m.exceptions(ctx, tx, practiceID)
	return loc, rules, exc, err
}

// reconcile brings a practice's slots in line with its template inside tx: open slots that
// no longer match are removed, missing ones are added, and booked slots stay. Appointments
// that no longer fit are flagged needs_reschedule (and the flag is cleared when they fit again).
func (m *Module) reconcile(ctx context.Context, tx pgx.Tx, practiceID string, now time.Time) error {
	return m.reconcileOpt(ctx, tx, practiceID, now, false)
}

// reconcileOpt is reconcile; quiet flags appointments that no longer fit but emits no
// appointment.reschedule_needed. A clinic closure in cancel mode uses it: those appointments
// are cancelled next, and the patient must get the cancellation message, not both.
func (m *Module) reconcileOpt(ctx context.Context, tx pgx.Tx, practiceID string, now time.Time, quiet bool) error {
	loc, rules, exc, err := m.loadSchedule(ctx, tx, practiceID)
	if err != nil {
		return err
	}
	horizon := now.Add(time.Duration(m.cfg.HorizonDays) * 24 * time.Hour)
	want := generate(loc, rules, exc, now, horizon)
	wantBy := make(map[int64]slotSpec, len(want))
	for _, s := range want {
		wantBy[s.Start.Unix()] = s
	}
	// Remove open future slots that are no longer wanted (never a taken slot).
	rows, err := tx.Query(ctx, `SELECT id, start_at, mode FROM slot WHERE practice_id=$1 AND state='open' AND start_at > $2`, practiceID, now)
	if err != nil {
		return err
	}
	var del []string
	for rows.Next() {
		var id, md string
		var st time.Time
		if err := rows.Scan(&id, &st, &md); err != nil {
			rows.Close()
			return err
		}
		if w, ok := wantBy[st.Unix()]; !ok || w.Mode != md {
			del = append(del, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(del) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM slot WHERE id = ANY($1::uuid[]) AND state='open'`, del); err != nil {
			return err
		}
	}
	if err := insertSlots(ctx, tx, practiceID, want); err != nil {
		return err
	}
	return m.flagConflicts(ctx, tx, practiceID, now, horizon, wantBy, quiet)
}

func insertSlots(ctx context.Context, tx querier, practiceID string, want []slotSpec) error {
	if len(want) == 0 {
		return nil
	}
	idsA := make([]string, len(want))
	starts := make([]time.Time, len(want))
	ends := make([]time.Time, len(want))
	modes := make([]string, len(want))
	for i, s := range want {
		idsA[i], starts[i], ends[i], modes[i] = ids.New(), s.Start, s.End, s.Mode
	}
	row := tx.QueryRow(ctx, `WITH ins AS (INSERT INTO slot (id, practice_id, start_at, end_at, mode)
		SELECT u.id, $1::uuid, u.s, u.e, u.m FROM unnest($2::uuid[], $3::timestamptz[], $4::timestamptz[], $5::text[]) AS u(id, s, e, m)
		ON CONFLICT (practice_id, start_at) DO NOTHING RETURNING 1) SELECT count(*) FROM ins`, practiceID, idsA, starts, ends, modes)
	var n int
	return row.Scan(&n)
}

// flagConflicts sets needs_reschedule on live appointments whose slot no longer fits.
func (m *Module) flagConflicts(ctx context.Context, tx pgx.Tx, practiceID string, now, horizon time.Time, wantBy map[int64]slotSpec, quiet bool) error {
	rows, err := tx.Query(ctx, `SELECT id, patient_ref, clinic_ref, state, start_at, mode, needs_reschedule FROM appointment
		WHERE practice_id=$1 AND state IN ('held','pending_payment','confirmed') AND start_at > $2 AND start_at < $3`, practiceID, now, horizon)
	if err != nil {
		return err
	}
	type chg struct {
		id, patient, clinic, state string
		flag                       bool
	}
	var changes []chg
	for rows.Next() {
		var id, patient, clinic, state, mode string
		var st time.Time
		var cur bool
		if err := rows.Scan(&id, &patient, &clinic, &state, &st, &mode, &cur); err != nil {
			rows.Close()
			return err
		}
		w, ok := wantBy[st.Unix()]
		fits := ok && w.Mode == mode
		if fits == cur { // fits and flagged, or does not fit and not flagged: change needed
			changes = append(changes, chg{id, patient, clinic, state, !fits})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range changes {
		if _, err := tx.Exec(ctx, `UPDATE appointment SET needs_reschedule=$2, updated_at=$3 WHERE id=$1`, c.id, c.flag, now); err != nil {
			return err
		}
		if c.flag && c.state == StateConfirmed && !quiet {
			if err := m.emit(ctx, tx, bp.EventRescheduleNeeded, c.id, map[string]string{"appointmentRef": c.id, "patientRef": c.patient, "clinicRef": c.clinic, "practiceRef": practiceID}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Module) listConflicts(ctx context.Context, q querier, practiceID string, now time.Time) ([]Conflict, error) {
	rows, err := q.Query(ctx, `SELECT id, patient_ref, practice_id, start_at, mode, state FROM appointment
		WHERE practice_id=$1 AND needs_reschedule AND state IN ('held','pending_payment','confirmed') AND start_at > $2 ORDER BY start_at, id`, practiceID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Conflict{}
	for rows.Next() {
		var c Conflict
		if err := rows.Scan(&c.AppointmentID, &c.PatientRef, &c.PracticeID, &c.StartsAt, &c.Mode, &c.State); err != nil {
			return nil, err
		}
		c.StartsAt = c.StartsAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListConflicts lists appointments that need rescheduling because availability changed
// (TC-23). Each row is a read of a patient's booking fields, so it goes through the records
// gate (K-DP-02); a denied row is left out.
func (m *Module) ListConflicts(ctx context.Context, a Actor, practiceID string) ([]Conflict, error) {
	if _, err := m.staffPractice(ctx, a, practiceID, false); err != nil {
		return nil, err
	}
	all, err := m.listConflicts(ctx, m.pool, practiceID, m.now())
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, c := range all {
		ok, err := m.authorizeBooking(ctx, a, c.PatientRef, c.AppointmentID)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// authorizeBooking asks the records gate whether this person may read the booking fields
// of the appointment (K-DP-02). Any error is a deny (fail closed) and is returned.
func (m *Module) authorizeBooking(ctx context.Context, a Actor, patientRef, apptID string) (bool, error) {
	d, err := m.d.Gate.Authorize(ctx, rp.Request{Actor: rp.Actor{Ref: a.UserID, Role: a.Role, ClinicRef: a.ClinicRef, Restricted: a.Restricted},
		PatientRef: patientRef, Kind: rp.KindBooking, BookingRef: apptID, ResourceType: "appointment", ResourceRef: apptID, RequestID: a.RequestID})
	if err != nil {
		return false, err
	}
	return d.Allowed, nil
}

// MaterialiseSlots adds slots 30 days ahead for every practice with availability. It is
// idempotent (unique (practice, start)) and safe to run from several workers at once.
func (m *Module) MaterialiseSlots(ctx context.Context, now time.Time) (int, error) {
	rows, err := m.pool.Query(ctx, `SELECT practice_id FROM availability ORDER BY practice_id`)
	if err != nil {
		return 0, err
	}
	var practices []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return 0, err
		}
		practices = append(practices, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	total := 0
	horizon := now.Add(time.Duration(m.cfg.HorizonDays) * 24 * time.Hour)
	for _, p := range practices {
		var added int
		err := m.inTx(ctx, func(tx pgx.Tx) error {
			loc, rules, exc, err := m.loadSchedule(ctx, tx, p)
			if err != nil {
				return err
			}
			want := generate(loc, rules, exc, now, horizon)
			var before int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM slot WHERE practice_id=$1`, p).Scan(&before); err != nil {
				return err
			}
			if err := insertSlots(ctx, tx, p, want); err != nil {
				return err
			}
			var after int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM slot WHERE practice_id=$1`, p).Scan(&after); err != nil {
				return err
			}
			added = after - before
			return nil
		})
		if err != nil {
			m.log.Error("booking.materialise_failed", logging.Err(err))
			continue
		}
		total += added
	}
	return total, nil
}

// RefreshPractice refreshes the copied refs and zone when the directory changes a practice
// (subscribe to practice.updated). An unlisted practice is left as it is.
func (m *Module) RefreshPractice(ctx context.Context, practiceID string) error {
	p, err := m.d.Directory.GetPractice(ctx, practiceID)
	if errors.Is(err, dp.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := loadZone(p.TimeZone); err != nil {
		return nil
	}
	now := m.now()
	return m.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE availability SET clinic_ref=$2, branch_ref=$6, professional_ref=$3, time_zone=$4, updated_at=$5 WHERE practice_id=$1`,
			practiceID, p.ClinicRef, p.ProfessionalRef, p.TimeZone, now, branchOf(p))
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return m.reconcile(ctx, tx, practiceID, now)
	})
}

// ListOpenSlots lists open future slots of a listed practice, earliest first, with a cursor.
func (m *Module) ListOpenSlots(ctx context.Context, practiceID string, from, to time.Time, limit int, cursor string) ([]SlotInfo, string, error) {
	if !ids.Valid(practiceID) {
		return nil, "", problem.NotFound
	}
	if _, err := m.d.Directory.GetPractice(ctx, practiceID); err != nil {
		if errors.Is(err, dp.ErrNotFound) {
			return nil, "", problem.NotFound
		}
		return nil, "", err
	}
	now := m.now()
	if from.Before(now) {
		from = now
	}
	if to.IsZero() || to.After(now.Add(time.Duration(m.cfg.HorizonDays)*24*time.Hour)) {
		to = now.Add(time.Duration(m.cfg.HorizonDays) * 24 * time.Hour)
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if cursor != "" {
		sec, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || sec < 0 {
			return nil, "", problem.BadRequest
		}
		if c := time.Unix(sec, 0).UTC(); c.After(from) {
			from = c
		}
	}
	rows, err := m.pool.Query(ctx, `SELECT id, practice_id, start_at, end_at, mode FROM slot
		WHERE practice_id=$1 AND state='open' AND start_at > $2 AND start_at < $3 ORDER BY start_at LIMIT $4`, practiceID, from, to, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []SlotInfo{}
	for rows.Next() {
		var s SlotInfo
		if err := rows.Scan(&s.ID, &s.PracticeID, &s.StartsAt, &s.EndsAt, &s.Mode); err != nil {
			return nil, "", err
		}
		s.StartsAt, s.EndsAt = s.StartsAt.UTC(), s.EndsAt.UTC()
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = strconv.FormatInt(out[len(out)-1].StartsAt.Unix(), 10)
	}
	return out, next, nil
}

// EarliestSlots implements directory.AvailabilitySource: for each practice with an open
// slot, the start of its earliest open slot at or after from. The directory has already
// filtered the practices to listed ones.
func (m *Module) EarliestSlots(ctx context.Context, practiceIDs []string, from time.Time) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	if len(practiceIDs) == 0 {
		return out, nil
	}
	valid := make([]string, 0, len(practiceIDs))
	for _, p := range practiceIDs {
		if ids.Valid(p) {
			valid = append(valid, p)
		}
	}
	if now := m.now(); from.Before(now) {
		from = now
	}
	rows, err := m.pool.Query(ctx, `SELECT practice_id, min(start_at) FROM slot WHERE practice_id = ANY($1::uuid[]) AND state='open' AND start_at >= $2 GROUP BY practice_id`, valid, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var t time.Time
		if err := rows.Scan(&p, &t); err != nil {
			return nil, err
		}
		out[p] = t.UTC()
	}
	return out, rows.Err()
}

func (m *Module) nextOpen(ctx context.Context, practiceID string, now time.Time) []SlotInfo {
	rows, err := m.pool.Query(ctx, `SELECT id, practice_id, start_at, end_at, mode FROM slot WHERE practice_id=$1 AND state='open' AND start_at > $2 ORDER BY start_at LIMIT $3`, practiceID, now, m.cfg.OfferNextSlots)
	if err != nil {
		m.log.Warn("booking.next_slots_failed", logging.Err(err))
		return nil
	}
	defer rows.Close()
	out := []SlotInfo{}
	for rows.Next() {
		var s SlotInfo
		if err := rows.Scan(&s.ID, &s.PracticeID, &s.StartsAt, &s.EndsAt, &s.Mode); err == nil {
			s.StartsAt, s.EndsAt = s.StartsAt.UTC(), s.EndsAt.UTC()
			out = append(out, s)
		}
	}
	return out
}
