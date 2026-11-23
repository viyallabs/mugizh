package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore"
	auditpublic "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// The patient's reminder calendar (S-153) and actions on a reminder (S-154). The occurrences and their state
// live in scheduler-core, which holds no health data (K-ARC-02); this module maps the caller to the opaque
// subject (the subject of an activity is the patient's user ID, as clinical creates them), adds the wording at
// request time through the same resolver the notifications use, and audits every action with references only.

// Limits of the calendar API.
const (
	MaxReminderRange = 31 * 24 * time.Hour
	MaxReminderItems = 1500
)

// reminderKinds maps scheduler type keys to the kinds shown in the calendar. Appointments are not reminders
// here: the app merges them from booking's list.
var reminderKinds = map[string]string{"med.dose": "medicine", "injection.dose": "injection", "test.due": "test"}

// Problems of the reminder API.
var (
	ErrReminderRange   = problem.New(http.StatusBadRequest, "reminders.invalid_range", "The range is not valid (from before to, at most 31 days)")
	ErrReminderTooMany = problem.New(http.StatusBadRequest, "reminders.range_too_large", "Too many reminders in the range; ask for a shorter range")
	errActionInvalid   = problem.New(http.StatusBadRequest, "reminders.invalid_action", "The action is not valid")
	errActedAtRange    = problem.New(http.StatusBadRequest, "reminders.acted_at_out_of_range", "The time of the action is outside the accepted range")
	errActionConflict  = problem.New(http.StatusConflict, "reminders.action_conflict", "A different final action was already recorded")
	errSnoozeActive    = problem.New(http.StatusConflict, "reminders.snooze_active", "The reminder is already snoozed")
	errSnoozeLimit     = problem.New(http.StatusConflict, "reminders.snooze_limit", "The reminder cannot be snoozed again")
	errNotActionable   = problem.New(http.StatusConflict, "reminders.not_actionable", "The reminder is not in a state that allows this")
	errKeyReused       = problem.New(http.StatusConflict, "idempotency.key_reused", "This Idempotency-Key was used with a different request")
)

// ReminderOccurrence is one calendar entry.
type ReminderOccurrence struct {
	ID               string
	Kind             string // medicine, injection or test
	DueAt            time.Time
	TimeZone         string
	State            string // due, taken, skipped, snoozed or missed
	ActedAt          *time.Time
	SnoozedUntil     *time.Time
	SnoozeCount      int
	Label            string // for the signed-in app; may name the medicine
	NotificationText string // what a notification would say now; names a medicine only with P-MEDNAMES
}

// Reminders is the calendar answer. ETag changes whenever anything in Items changes.
type Reminders struct {
	From, To time.Time
	Items    []ReminderOccurrence
	ETag     string
}

func reminderState(o schedulercore.Occurrence, now time.Time) (state string, snoozedUntil *time.Time) {
	switch o.State {
	case "acknowledged":
		if o.Action == schedulercore.ActionSkipped {
			return "skipped", nil
		}
		return "taken", nil
	case "missed":
		return "missed", nil
	case "pending":
		if o.NotifyAt != nil && o.NotifyAt.After(now) {
			u := o.NotifyAt.UTC()
			return "snoozed", &u
		}
	}
	return "due", nil
}

// Reminders returns the caller's own reminder occurrences with from <= dueAt < to.
func (m *Module) Reminders(ctx context.Context, userID string, from, to time.Time) (Reminders, error) {
	if !ids.Valid(userID) {
		return Reminders{}, problem.Forbidden
	}
	from, to = from.UTC(), to.UTC()
	if from.IsZero() || to.IsZero() || !to.After(from) || to.Sub(from) > MaxReminderRange {
		return Reminders{}, ErrReminderRange
	}
	if m.deps.Occurrences == nil {
		return Reminders{}, problem.Unavailable
	}
	occs, err := m.deps.Occurrences.ListOccurrences(ctx, schedulercore.ListOccurrencesRequest{SubjectRef: userID, From: from, To: to})
	if err != nil {
		m.log.WarnContext(ctx, "notifications.reminders_unavailable", "error_code", string(adapters.KindOf(err)))
		if adapters.KindOf(err) == adapters.Invalid {
			return Reminders{}, ErrReminderTooMany
		}
		return Reminders{}, problem.Unavailable
	}
	prefs, err := m.Preferences(ctx, userID)
	if err != nil {
		return Reminders{}, err
	}
	allow := false // fail closed: a failed consent check never allows a medicine name in notificationText
	if !prefs.Discreet {
		ok, err := m.deps.Gate.HasConsent(ctx, userID, PurposeMedNames)
		allow = err == nil && ok
	}
	now := m.now().UTC()
	type words struct{ label, notif string }
	memo := map[string]words{} // wording belongs to the activity, not to one occurrence
	wording := func(o schedulercore.Occurrence) words {
		key := o.ExternalRef + "|" + string(o.TypeKey)
		if w, ok := memo[key]; ok {
			return w
		}
		generic := ""
		if t, ok := m.tmpl[m.cfg.ReminderTemplates[string(o.TypeKey)]]; ok {
			generic, _ = m.text(prefs.Locale, t.BodyKey, nil)
		}
		var neutral, named string
		if m.deps.Resolver != nil {
			// allowMedicineNames is true: the label is the patient's own data in the signed-in app. The
			// notification text below uses the named wording only when P-MEDNAMES allows it now (K-DP-09).
			n, nm, err := m.deps.Resolver.Resolve(ctx, o.ID, o.ExternalRef, string(o.TypeKey), userID, true)
			if err != nil {
				m.log.WarnContext(ctx, "notifications.resolver_failed", "reason", "resolver_error")
			} else {
				if n != "" && len(n) <= 200 && !m.unsafeVar(n) {
					neutral = n
				}
				if nm != "" && len(nm) <= 200 && !m.unsafeVar(nm) {
					named = nm
				}
			}
		}
		pick := func(opts ...string) string {
			for _, s := range opts {
				if s != "" {
					return s
				}
			}
			return ""
		}
		w := words{label: pick(named, neutral, generic), notif: pick(neutral, generic)}
		if allow {
			w.notif = pick(named, neutral, generic)
		}
		memo[key] = w
		return w
	}
	out := Reminders{From: from, To: to, Items: []ReminderOccurrence{}}
	for _, o := range occs {
		kind, ok := reminderKinds[string(o.TypeKey)]
		if !ok || o.State == "cancelled" {
			continue
		}
		if len(out.Items) >= MaxReminderItems {
			return Reminders{}, ErrReminderTooMany
		}
		st, until := reminderState(o, now)
		w := wording(o)
		it := ReminderOccurrence{ID: o.ID, Kind: kind, DueAt: o.DueAt.UTC(), TimeZone: o.TimeZone, State: st, SnoozedUntil: until, SnoozeCount: o.SnoozeCount,
			Label: w.label, NotificationText: w.notif}
		if st == "taken" || st == "skipped" {
			it.ActedAt = o.ActedAt
		}
		out.Items = append(out.Items, it)
	}
	raw, _ := json.Marshal(struct {
		F, T  time.Time
		Items []ReminderOccurrence
	}{from, to, out.Items})
	sum := sha256.Sum256(raw)
	out.ETag = `"` + hex.EncodeToString(sum[:16]) + `"`
	return out, nil
}

// ReminderAction is the patient's action. ClientActionID (a UUID) is the idempotency key.
type ReminderAction struct {
	ClientActionID string
	Action         string // taken, skipped or snoozed
	ActedAt        time.Time
	SnoozeMinutes  int
}

// ReminderActionResult is the occurrence after the action.
type ReminderActionResult struct {
	ID           string
	State        string
	ActedAt      *time.Time
	SnoozedUntil *time.Time
	SnoozeCount  int
}

func actionProblem(err error) error {
	var ae *adapters.Error
	if !errors.As(err, &ae) {
		return problem.Unavailable
	}
	switch ae.Kind {
	case adapters.NotFound:
		return problem.NotFound
	case adapters.Rejected:
		switch ae.Reason {
		case schedulercore.ReasonActionConflict:
			return errActionConflict
		case schedulercore.ReasonSnoozeActive:
			return errSnoozeActive
		case schedulercore.ReasonSnoozeLimit:
			return errSnoozeLimit
		case schedulercore.ReasonKeyReused:
			return errKeyReused
		}
		return errNotActionable
	case adapters.Invalid:
		if ae.Reason == schedulercore.ReasonActedAtRange {
			return errActedAtRange.WithFields(problem.FieldError{Field: "actedAt", Code: "out_of_range"})
		}
		return errActionInvalid
	}
	return problem.Unavailable
}

// RecordReminderAction records the caller's action on one of their own occurrences. The action is applied in
// scheduler-core (idempotent by the client action ID); a taken or skipped dose also stops any notification
// still waiting for that occurrence, so no SMS fallback goes out for a dose already taken (TC-40). Every
// action is audited once, with references only.
func (m *Module) RecordReminderAction(ctx context.Context, userID, occurrenceID string, a ReminderAction) (ReminderActionResult, error) {
	if !ids.Valid(userID) {
		return ReminderActionResult{}, problem.Forbidden
	}
	if !ids.Valid(occurrenceID) {
		return ReminderActionResult{}, problem.NotFound
	}
	if !ids.Valid(a.ClientActionID) || a.ActedAt.IsZero() {
		return ReminderActionResult{}, errActionInvalid
	}
	switch a.Action {
	case schedulercore.ActionTaken, schedulercore.ActionSkipped, schedulercore.ActionSnoozed:
	default:
		return ReminderActionResult{}, errActionInvalid.WithFields(problem.FieldError{Field: "action", Code: "invalid"})
	}
	if m.deps.Occurrences == nil {
		return ReminderActionResult{}, problem.Unavailable
	}
	res, err := m.deps.Occurrences.RecordAction(ctx, schedulercore.ActionRequest{OccurrenceID: occurrenceID, SubjectRef: userID,
		ClientActionID: a.ClientActionID, Action: a.Action, ActedAt: a.ActedAt, SnoozeMinutes: a.SnoozeMinutes})
	if err != nil {
		p := actionProblem(err)
		if pe, ok := p.(*problem.Problem); ok && pe.Status != http.StatusServiceUnavailable {
			reason := "action_refused"
			if pe.Status == http.StatusNotFound {
				reason = "occurrence_not_found" // someone else's or unknown: the patient sees the same 404 (K-SEC-04)
			}
			if aerr := m.auditAction(ctx, userID, occurrenceID, a.Action, auditpublic.Denied, reason); aerr != nil {
				return ReminderActionResult{}, aerr
			}
		}
		return ReminderActionResult{}, p
	}
	now := m.now().UTC()
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO reminder_action (client_action_id, user_ref, occurrence_ref, action, acted_at, created_at)
			VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, a.ClientActionID, userID, occurrenceID, a.Action, a.ActedAt.UTC(), now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // a retry: already audited and applied
		}
		if err := auditpublic.Record(ctx, tx, auditEvent(userID, occurrenceID, a.Action, auditpublic.Allowed, "")); err != nil {
			return err
		}
		// The patient's other devices drop (or move, for a snooze) their backup for this dose (S-165).
		if err := requestSync(ctx, tx, userID, now); err != nil {
			return err
		}
		// The patient has seen the notification and answered it: nothing still waiting for this occurrence
		// (a retry, the SMS fallback after a push without receipt) may go out. A snoozed occurrence gets a new
		// dispatch from scheduler-core's next callback.
		rs, err := tx.Query(ctx, `UPDATE dispatch SET state='acted', vars=NULL, receipt_checked=true, lease_until=NULL, updated_at=$3
			WHERE recipient_ref=$1 AND occurrence_ref=$2 AND kind='reminder' AND state IN ('queued','fallback','sent')
			RETURNING id::text, template_key, channel`, userID, occurrenceID, now)
		if err != nil {
			return err
		}
		var logs []logEntry
		for rs.Next() {
			var l logEntry
			if err := rs.Scan(&l.dispatch, &l.template, &l.channel); err != nil {
				rs.Close()
				return err
			}
			l.recipient, l.outcome, l.reason, l.at = userID, "acted", a.Action, now
			logs = append(logs, l)
		}
		rs.Close()
		if rs.Err() != nil {
			return rs.Err()
		}
		for _, l := range logs {
			if err := insertLog(ctx, tx, l); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ReminderActionResult{}, err
	}
	st, until := reminderState(res.Occurrence, now)
	out := ReminderActionResult{ID: res.Occurrence.ID, State: st, SnoozedUntil: until, SnoozeCount: res.Occurrence.SnoozeCount}
	if st == "taken" || st == "skipped" {
		out.ActedAt = res.Occurrence.ActedAt
	}
	return out, nil
}

func auditEvent(userID, occurrenceID, action string, result auditpublic.Result, reason string) auditpublic.Event {
	return auditpublic.Event{ActorRef: userID, ActorRole: "patient", Action: "reminders.action", SubjectRef: userID, ResourceType: "occurrence",
		ResourceRef: occurrenceID, Purpose: action, ReasonCode: reason, Result: result}
}

func (m *Module) auditAction(ctx context.Context, userID, occurrenceID, action string, result auditpublic.Result, reason string) error {
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return auditpublic.Record(ctx, tx, auditEvent(userID, occurrenceID, action, result, reason))
	})
}

// liveRows keeps the rows of a claimed unit that are still waiting to be sent: a patient action may have
// finished some of them since they were claimed.
func (m *Module) liveRows(ctx context.Context, unit []row) ([]row, error) {
	idsList := make([]string, len(unit))
	for i, r := range unit {
		idsList[i] = r.id
	}
	rs, err := m.pool.Query(ctx, `SELECT id::text FROM dispatch WHERE id::text = ANY($1) AND state IN ('queued','fallback')`, idsList)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	alive := map[string]bool{}
	for rs.Next() {
		var id string
		if err := rs.Scan(&id); err != nil {
			return nil, err
		}
		alive[id] = true
	}
	if rs.Err() != nil {
		return nil, rs.Err()
	}
	var out []row
	for _, r := range unit {
		if alive[r.id] {
			out = append(out, r)
		}
	}
	return out, nil
}
