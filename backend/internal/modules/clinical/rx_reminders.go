package clinical

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore"
	np "gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// reminderNS derives deterministic references, so a retried handler creates nothing twice.
var reminderNS = uuid.MustParse("6f1d2a58-3c4e-5b7a-9d10-2b6c8e4f7a31")

// HandleSigned is the subscriber for prescription.signed (Opus subscribes it). It is
// idempotent without outbox.Once because it makes HTTP calls: every step is safe to repeat
// (T2 grant is idempotent, scheduler creates replay by key, links are unique, the
// notification has a dedupe key).
func (r *Rx) HandleSigned(ctx context.Context, e outbox.Event) error { return r.handle(ctx, e, false) }

// HandleAmended is the subscriber for prescription.amended.
func (r *Rx) HandleAmended(ctx context.Context, e outbox.Event) error { return r.handle(ctx, e, true) }

type signedRow struct {
	rxRef, versionID, appt, patient, doctor, clinic string
	version                                         int
	content                                         DraftContent
	signedAt                                        time.Time
}

func (r *Rx) loadSigned(ctx context.Context, rxRef string, version int) (signedRow, error) {
	var s signedRow
	var ct careKey
	err := r.m.pool.QueryRow(ctx, `SELECT v.id::text, p.appointment_ref::text, p.patient_ref::text, p.doctor_ref::text, coalesce(p.clinic_ref::text,''), v.version, v.content, v.signed_at
		FROM prescription p JOIN prescription_version v ON v.prescription_id = p.id WHERE p.id = $1 AND v.version = $2 AND v.state = 'signed'`, rxRef, version).
		Scan(&s.versionID, &s.appt, &s.patient, &s.doctor, &s.clinic, &s.version, &ct, &s.signedAt)
	s.rxRef = rxRef
	if err != nil {
		return s, err
	}
	raw, err := r.open(ctx, "prescription_version", "content", s.versionID, ct)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(raw, &s.content)
}

func (r *Rx) handle(ctx context.Context, e outbox.Event, amended bool) error {
	rxRef := e.Payload[PayloadPrescrip]
	version, err := strconv.Atoi(e.Payload[PayloadVersion])
	if err != nil || rxRef == "" {
		return nil // malformed event: nothing to do, never retry forever
	}
	s, err := r.loadSigned(ctx, rxRef, version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := r.d.Granter.GrantT2(ctx, s.patient, s.doctor, s.clinic); err != nil {
		return fmt.Errorf("clinical: t2 grant: %w", err)
	}
	if amended || s.version > 1 {
		if err := r.cancelOlder(ctx, rxRef, s.version); err != nil {
			return err
		}
	}
	if err := r.scheduleReminders(ctx, s); err != nil {
		return err
	}
	if err := r.remindersChanged(ctx, s.patient); err != nil {
		return err
	}
	return r.notify(ctx, s, amended || s.version > 1)
}

func (r *Rx) cancelOlder(ctx context.Context, rxRef string, version int) error {
	rows, err := r.m.pool.Query(ctx, `SELECT l.external_ref::text, l.activity_id::text FROM reminder_link l
		JOIN prescription_version v ON v.id = l.prescription_version_id WHERE v.prescription_id = $1 AND v.version < $2 AND l.state = 'active'`, rxRef, version)
	if err != nil {
		return err
	}
	type link struct{ ext, act string }
	var ls []link
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.ext, &l.act); err != nil {
			rows.Close()
			return err
		}
		ls = append(ls, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, l := range ls {
		if err := r.d.Scheduler.CancelActivity(ctx, l.act); err != nil && adapters.KindOf(err) != adapters.NotFound {
			return err
		}
		if _, err := r.m.pool.Exec(ctx, `UPDATE reminder_link SET state='cancelled', cancelled_at=$2 WHERE external_ref=$1`, l.ext, r.now()); err != nil {
			return err
		}
	}
	return nil
}

func (r *Rx) notify(ctx context.Context, s signedRow, amended bool) error {
	key, reason := r.cfg.SignedTemplateKey, "prescription.signed"
	if amended {
		key, reason = r.cfg.AmendedTemplateKey, "prescription.amended"
	}
	if r.d.Notifier == nil || key == "" {
		return nil
	}
	_, err := r.d.Notifier.Send(ctx, np.Request{TemplateKey: key, RecipientRef: s.patient, Priority: np.Normal, Category: np.Service,
		Reason: reason, DedupeKey: "rx-" + s.versionID})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, np.ErrInvalid), errors.Is(err, np.ErrUnknownTemplate):
		r.d.Log.Error("clinical.notify_rejected", logging.Err(err)) // a configuration problem: retrying cannot fix it
		return nil
	}
	return err
}

// ReminderOutcome is what the follow-up did about reminders for a signed version.
type ReminderOutcome string

// Outcomes; ConsentMissing means "offer once" (S-155).
const (
	RemindersScheduled      ReminderOutcome = "scheduled"
	RemindersConsentMissing ReminderOutcome = "consent_missing"
	RemindersNone           ReminderOutcome = "none"
)

// ReminderOutcomeFor returns the recorded outcome of a signed version ("" if not yet run).
func (r *Rx) ReminderOutcomeFor(ctx context.Context, versionID string) (ReminderOutcome, error) {
	var o string
	err := r.m.pool.QueryRow(ctx, `SELECT outcome FROM reminder_run WHERE prescription_version_id = $1`, versionID).Scan(&o)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return ReminderOutcome(o), err
}

type planned struct {
	itemRef string
	step    int
	kind    string
	typeKey schedulercore.TypeKey
	rrule   string
	starts  time.Time
	ends    *time.Time
}

func (r *Rx) scheduleReminders(ctx context.Context, s signedRow) error {
	if o, err := r.ReminderOutcomeFor(ctx, s.versionID); err != nil || o != "" {
		return err
	}
	ok, err := r.d.Gate.HasConsent(ctx, s.patient, r.cfg.ReminderPurpose)
	if err != nil {
		return err // fail closed: retry later, create nothing
	}
	if !ok {
		return r.recordRun(ctx, s.versionID, RemindersConsentMissing)
	}
	f, err := r.d.Consults.Consult(ctx, s.appt)
	if err != nil {
		return fmt.Errorf("clinical: consult facts: %w", err)
	}
	loc, err := time.LoadLocation(f.PatientTimeZone)
	if err != nil || f.PatientTimeZone == "" || f.PatientTimeZone == "Local" {
		return r.recordRun(ctx, s.versionID, RemindersNone) // cannot anchor local times: no reminders
	}
	prefs, err := r.itemPrefs(ctx, s.rxRef)
	if err != nil {
		return err
	}
	plan := r.plan(s, loc)
	for _, p := range plan {
		pref, ok := prefs[p.itemRef]
		if ok && !pref.enabled {
			continue // S-155: the patient switched this item off
		}
		if err := r.createActivity(ctx, s, p, f.PatientTimeZone, pref.generation); err != nil {
			return err
		}
	}
	out := RemindersScheduled
	if len(plan) == 0 {
		out = RemindersNone
	}
	return r.recordRun(ctx, s.versionID, out)
}

func (r *Rx) recordRun(ctx context.Context, versionID string, o ReminderOutcome) error {
	_, err := r.m.pool.Exec(ctx, `INSERT INTO reminder_run (prescription_version_id, outcome) VALUES ($1,$2) ON CONFLICT DO NOTHING`, versionID, string(o))
	return err
}

// hours spreads n reminders evenly across the configured waking hours.
func (r *Rx) hours(n int) []int {
	lo, hi := r.cfg.ReminderStartHour, r.cfg.ReminderEndHour
	if n < 1 || n > hi-lo+1 {
		return nil
	}
	if n == 1 {
		return []int{lo}
	}
	out := make([]int, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, lo+(i*(hi-lo)+(n-1)/2)/(n-1))
	}
	return out
}

func byHour(hs []int) string {
	s := make([]string, len(hs))
	for i, h := range hs {
		s[i] = strconv.Itoa(h)
	}
	return strings.Join(s, ",")
}

func addDuration(d time.Time, v int, unit string) (time.Time, bool) {
	switch unit {
	case "day":
		return d.AddDate(0, 0, v), true
	case "week":
		return d.AddDate(0, 0, 7*v), true
	case "month":
		return d.AddDate(0, v, 0), true
	}
	return d, false
}

// plan turns the signed items into scheduler activities. As-needed items get none
// (E-RX-04). A tapering schedule becomes one activity per step, one after the other
// (E-RX-05). Only the dose times are derived (evenly spread, from configuration); the drug
// and dose never leave this module.
func (r *Rx) plan(s signedRow, loc *time.Location) []planned {
	var out []planned
	signed := s.signedAt.In(loc)
	midnight := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc) }
	for _, it := range s.content.Items {
		if it.AsNeeded {
			continue
		}
		type step struct {
			f FrequencyInput
			d DurationInput
		}
		steps := []step{{it.Frequency, it.Duration}}
		for _, t := range it.Taper {
			steps = append(steps, step{t.Frequency, t.Duration})
		}
		start := signed
		for i, st := range steps {
			hs := r.hours(st.f.Times)
			end, okDur := addDuration(midnight(start), st.d.Value, st.d.Unit)
			if st.f.Period != "day" || hs == nil || !okDur || st.d.Value < 1 {
				break // cannot schedule this step or what follows it
			}
			e := end.UTC()
			out = append(out, planned{itemRef: it.Ref, step: i, kind: "med", typeKey: schedulercore.MedDose,
				rrule: "FREQ=DAILY;BYHOUR=" + byHour(hs) + ";BYMINUTE=0;BYSECOND=0", starts: start.UTC(), ends: &e})
			start = end
		}
	}
	for _, t := range s.content.Tests {
		h := strconv.Itoa(r.cfg.ReminderStartHour)
		suffix := ";BYHOUR=" + h + ";BYMINUTE=0;BYSECOND=0"
		switch {
		case t.DueOn != "":
			d, err := time.ParseInLocation("2006-01-02", t.DueOn, loc)
			if err != nil {
				continue
			}
			at := d.Add(time.Duration(r.cfg.ReminderStartHour) * time.Hour)
			if !at.After(signed) {
				continue
			}
			out = append(out, planned{itemRef: t.Ref, kind: "test", typeKey: schedulercore.TestDue, rrule: "FREQ=DAILY;COUNT=1" + suffix, starts: at.UTC()})
		case t.IntervalDays > 0:
			at := midnight(signed).AddDate(0, 0, t.IntervalDays).Add(time.Duration(r.cfg.ReminderStartHour) * time.Hour)
			out = append(out, planned{itemRef: t.Ref, kind: "test", typeKey: schedulercore.TestDue,
				rrule: "FREQ=DAILY;INTERVAL=" + strconv.Itoa(t.IntervalDays) + ";COUNT=" + strconv.Itoa(t.Repeats) + suffix, starts: at.UTC()})
		}
	}
	return out
}

// Resolve implements notifications' ReminderContentResolver. The neutral text names no
// medicine, condition or specialty (K-DP-09). The named text is built only when the caller
// says P-MEDNAMES is on now, and only from the drug master's generic name and strength,
// never from the doctor's free text. Any doubt returns an error, and notifications then
// uses its generic wording.
func (r *Rx) Resolve(ctx context.Context, _ /*occurrenceRef*/, activityExternalRef, _ /*typeKey*/, recipientRef string, allowMedicineNames bool) (string, string, error) {
	if !isUUID(activityExternalRef) || !isUUID(recipientRef) {
		return "", "", ErrNotFound
	}
	var versionID, patient, itemRef, kind, clsVersion string
	var ct careKey
	err := r.m.pool.QueryRow(ctx, `SELECT v.id::text, l.patient_ref::text, l.item_ref, l.kind, v.classification_version, v.content
		FROM reminder_link l JOIN prescription_version v ON v.id = l.prescription_version_id WHERE l.external_ref = $1 AND l.state = 'active'`, activityExternalRef).
		Scan(&versionID, &patient, &itemRef, &kind, &clsVersion, &ct)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	if patient != recipientRef {
		return "", "", ErrNotFound // never resolve someone else's reminder
	}
	neutral := r.cfg.NeutralReminderText
	if kind == "test" {
		// Review F1 (K-DP-09): a test name is the doctor's free text and can disclose a
		// diagnosis ("HIV RNA PCR"); P-MEDNAMES covers medicine names only. Test reminders are
		// always neutral; the test name stays in the prescription inside the app.
		return r.cfg.NeutralTestText, "", nil
	}
	if !allowMedicineNames {
		return neutral, "", nil
	}
	raw, err := r.open(ctx, "prescription_version", "content", versionID, ct)
	if err != nil {
		return "", "", err
	}
	var d DraftContent
	if err := json.Unmarshal(raw, &d); err != nil {
		return "", "", err
	}
	for _, it := range d.Items {
		if it.Ref != itemRef {
			continue
		}
		dd, err := r.drugDetails(ctx, clsVersion, []string{it.DrugRef})
		if err != nil {
			return "", "", err
		}
		x, ok := dd[it.DrugRef]
		if !ok {
			return "", "", ErrNotFound
		}
		return neutral, fmt.Sprintf(r.cfg.NamedReminderFormat, strings.TrimSpace(x[0]+" "+x[1])), nil
	}
	return "", "", ErrNotFound
}

func isUUID(s string) bool { _, err := uuid.Parse(s); return err == nil }

// remindersChanged emits EventRemindersChanged for the patient (S-165). A retry of the follow-up emits it again,
// which only asks the devices to refresh once more.
func (r *Rx) remindersChanged(ctx context.Context, patient string) error {
	return db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return outbox.Emit(ctx, tx, outbox.Event{Type: EventRemindersChanged, SubjectRef: patient})
	})
}
