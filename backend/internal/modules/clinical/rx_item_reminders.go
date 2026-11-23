package clinical

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore"
	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// Per-item reminder switch (S-155). Reminders are on by default for every item only when the
// patient has granted P-REMIND (scheduleReminders); each item can then be turned off, and on
// again, by the patient. The switch is kept per prescription item, so an amended prescription
// keeps an item that was switched off, off.

// ItemReminder is one prescription item's reminder state.
type ItemReminder struct {
	ItemRef string
	Kind    string // med | test
	Enabled bool   // the patient's switch (default on)
	Active  bool   // reminders are scheduled now
}

// ErrReminderConsent: turning reminders on needs P-REMIND (the app offers it).
var ErrReminderConsent = errors.New("clinical: reminders need P-REMIND")

// ErrReminderConflict: the switch changed at the same time (another tap); re-read and retry.
var ErrReminderConflict = errors.New("clinical: reminder switch changed concurrently")

var reItemRef = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

type itemPref struct {
	enabled    bool
	generation int
}

func (r *Rx) itemPrefs(ctx context.Context, rxRef string) (map[string]itemPref, error) {
	rows, err := r.m.pool.Query(ctx, `SELECT item_ref, enabled, generation FROM reminder_item_pref WHERE prescription_id=$1`, rxRef)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]itemPref{}
	for rows.Next() {
		var ref string
		var p itemPref
		if err := rows.Scan(&ref, &p.enabled, &p.generation); err != nil {
			return nil, err
		}
		out[ref] = p
	}
	return out, rows.Err()
}

// createActivity creates one planned reminder in scheduler-core and links it. A re-enabled item
// (generation > 0) gets new external refs, so the scheduler never returns a cancelled activity.
func (r *Rx) createActivity(ctx context.Context, s signedRow, p planned, zone string, generation int) error {
	key := s.versionID + "|" + p.itemRef + "|" + strconv.Itoa(p.step) + "|" + p.kind
	if generation > 0 {
		key += "|g" + strconv.Itoa(generation)
	}
	ext := uuid.NewSHA1(reminderNS, []byte(key)).String()
	idem := uuid.NewSHA1(reminderNS, []byte("idem|"+ext)).String()
	act, err := r.d.Scheduler.CreateActivity(ctx, schedulercore.CreateRequest{IdempotencyKey: idem, SubjectRef: s.patient, AppRef: r.cfg.AppRef,
		ExternalRef: ext, TypeKey: p.typeKey, RRule: p.rrule, TimeZone: zone, StartsAt: p.starts, EndsAt: p.ends})
	if err != nil {
		return err
	}
	_, err = r.m.pool.Exec(ctx, `INSERT INTO reminder_link (external_ref, prescription_version_id, patient_ref, item_ref, step, kind, activity_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (external_ref) DO NOTHING`, ext, s.versionID, s.patient, p.itemRef, p.step, p.kind, act.ID)
	return err
}

// latestSignedOf returns the caller's latest signed version of a prescription, or ErrNotFound
// (someone else's prescription looks missing, K-SEC-04).
func (r *Rx) latestSignedOf(ctx context.Context, c Caller, rxRef string) (signedRow, error) {
	if err := r.selfGate(ctx, c, "prescription", ""); err != nil {
		return signedRow{}, err
	}
	if !ids.Valid(rxRef) {
		return signedRow{}, ErrNotFound
	}
	var version int
	err := r.m.pool.QueryRow(ctx, `SELECT coalesce(max(v.version), 0) FROM prescription p JOIN prescription_version v ON v.prescription_id = p.id
		WHERE p.id = $1 AND p.patient_ref = $2 AND v.state = 'signed'`, rxRef, c.UserID).Scan(&version)
	if err != nil {
		return signedRow{}, err
	}
	if version == 0 {
		return signedRow{}, ErrNotFound
	}
	s, err := r.loadSigned(ctx, rxRef, version)
	if errors.Is(err, pgx.ErrNoRows) {
		return signedRow{}, ErrNotFound
	}
	return s, err
}

// ItemReminders lists the reminder state of every item of the caller's latest signed version.
func (r *Rx) ItemReminders(ctx context.Context, c Caller, rxRef string) ([]ItemReminder, error) {
	s, err := r.latestSignedOf(ctx, c, rxRef)
	if err != nil {
		return nil, err
	}
	prefs, err := r.itemPrefs(ctx, rxRef)
	if err != nil {
		return nil, err
	}
	active := map[string]bool{}
	rows, err := r.m.pool.Query(ctx, `SELECT DISTINCT item_ref FROM reminder_link WHERE prescription_version_id=$1 AND state='active'`, s.versionID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			rows.Close()
			return nil, err
		}
		active[ref] = true
	}
	rows.Close()
	out := []ItemReminder{}
	add := func(ref, kind string) {
		p, ok := prefs[ref]
		out = append(out, ItemReminder{ItemRef: ref, Kind: kind, Enabled: !ok || p.enabled, Active: active[ref]})
	}
	for _, it := range s.content.Items {
		add(it.Ref, "med")
	}
	for _, t := range s.content.Tests {
		add(t.Ref, "test")
	}
	return out, nil
}

// SetItemReminders switches one item's reminders off (its scheduler activities are cancelled) or
// on again (scheduled from the latest signed version; needs P-REMIND now). Idempotent; audited.
func (r *Rx) SetItemReminders(ctx context.Context, c Caller, rxRef, itemRef string, enabled bool) error {
	s, err := r.latestSignedOf(ctx, c, rxRef)
	if err != nil {
		return err
	}
	if !reItemRef.MatchString(itemRef) || !s.hasItem(itemRef) {
		return ErrNotFound
	}
	prefs, err := r.itemPrefs(ctx, rxRef)
	if err != nil {
		return err
	}
	cur, ok := prefs[itemRef]
	if (!ok && enabled) || (ok && cur.enabled == enabled) {
		return nil // already in that state
	}
	now := r.now()
	gen := cur.generation
	if enabled {
		has, err := r.d.Gate.HasConsent(ctx, s.patient, r.cfg.ReminderPurpose)
		if err != nil {
			return fmt.Errorf("clinical: consent check: %w", err) // fail closed
		}
		if !has {
			return ErrReminderConsent
		}
		gen++
		// Review F2: schedule first, save the switch last. A failure leaves the switch off, and a
		// retry uses the same generation, so the scheduler's idempotency keys return the same
		// activities instead of duplicating them.
		f, err := r.d.Consults.Consult(ctx, s.appt)
		if err != nil {
			return fmt.Errorf("clinical: consult facts: %w", err)
		}
		if loc, lerr := time.LoadLocation(f.PatientTimeZone); lerr == nil && f.PatientTimeZone != "" && f.PatientTimeZone != "Local" {
			for _, p := range r.plan(s, loc) {
				if p.itemRef != itemRef {
					continue
				}
				if err := r.createActivity(ctx, s, p, f.PatientTimeZone, gen); err != nil {
					return err
				}
			}
		} // no zone to anchor local times: nothing scheduled, as for a new prescription
	} else {
		// Cancel first: a failure leaves the switch unchanged, so the patient can retry.
		rows, err := r.m.pool.Query(ctx, `SELECT activity_id::text FROM reminder_link WHERE prescription_version_id=$1 AND item_ref=$2 AND state='active'`, s.versionID, itemRef)
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
		for _, a := range acts {
			if err := r.d.Scheduler.CancelActivity(ctx, a); err != nil {
				return fmt.Errorf("clinical: cancel reminder: %w", err)
			}
		}
	}
	return db.InTx(ctx, r.m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Optimistic check: the row must still be what was read, so two taps at once cannot
		// interleave; the loser gets a conflict and the app re-reads the state.
		tag, err := tx.Exec(ctx, `INSERT INTO reminder_item_pref (prescription_id, item_ref, enabled, generation, updated_at) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (prescription_id, item_ref) DO UPDATE SET enabled=EXCLUDED.enabled, generation=EXCLUDED.generation, updated_at=EXCLUDED.updated_at
			WHERE reminder_item_pref.enabled = $6 AND reminder_item_pref.generation = $7`,
			rxRef, itemRef, enabled, gen, now, !enabled, cur.generation)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrReminderConflict
		}
		if !enabled {
			if _, err := tx.Exec(ctx, `UPDATE reminder_link SET state='cancelled', cancelled_at=$3 WHERE prescription_version_id=$1 AND item_ref=$2 AND state='active'`,
				s.versionID, itemRef, now); err != nil {
				return err
			}
		}
		reason := "off"
		if enabled {
			reason = "on"
		}
		if err := ap.Record(ctx, tx, r.auditEvent(c, "clinical.reminder_item_set", s.patient, "prescription", rxRef, ap.Allowed, reason)); err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, outbox.Event{Type: EventRemindersChanged, SubjectRef: s.patient})
	})
}

func (s signedRow) hasItem(ref string) bool {
	for _, it := range s.content.Items {
		if it.Ref == ref {
			return true
		}
	}
	for _, t := range s.content.Tests {
		if t.Ref == ref {
			return true
		}
	}
	return false
}
