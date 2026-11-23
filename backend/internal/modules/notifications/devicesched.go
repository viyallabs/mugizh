package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/push"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// The on-device backup schedule (S-165, E-REM-02, TC-40). The app keeps the next 72 hours of reminders as local
// notifications so they still fire with no network. The schedule says exactly what the server itself would send
// and when: the same grouping (E-REM-03), the same wording (a medicine is named only with P-MEDNAMES and
// discreet mode off, K-DP-09), the same quiet-hours rule for non-critical reminders (K-MSG-03), nothing without
// P-REMIND. Each entry carries the replace key of the server push for the same minute, so a phone that is
// online shows one notification, not two. When anything that shapes the schedule changes, the patient's devices
// get a silent, content-free push asking them to fetch it again (D-67, D-68).

// Limits of the device schedule (D-67).
const (
	// DeviceScheduleHorizon is how far ahead the device keeps reminders (the story: 72 hours).
	DeviceScheduleHorizon = 72 * time.Hour
	// MaxDeviceScheduleEntries keeps the schedule under the 64 pending local notifications iOS allows an app.
	MaxDeviceScheduleEntries = 60
	// DeviceScheduleRefresh is when the app should fetch again even without a refresh push.
	DeviceScheduleRefresh = 12 * time.Hour
	// maxEntryOccurrences bounds the occurrence IDs one grouped entry lists.
	maxEntryOccurrences = 100
	// snoozeLookback: a dose may be snoozed up to 24 hours after it was due (D-60), so an occurrence due that long
	// ago can still have a notification ahead of it.
	snoozeLookback = 25 * time.Hour
	// syncMinInterval spaces the refresh pushes to one patient (the OS throttles frequent background pushes).
	syncMinInterval = 30 * time.Second
	// syncMaxAttempts: a refresh push is a hint; after this many transient failures it is dropped.
	syncMaxAttempts = 5
	// syncKeep is how long a finished refresh request row is kept.
	syncKeep = 24 * time.Hour
)

// DeviceScheduleEntry is one local notification for the device to schedule.
type DeviceScheduleEntry struct {
	ReplaceKey    string    // same key as the server push for this minute and class
	FireAt        time.Time // UTC instant
	TitleKey      string    // translation key, as in the push
	Body          string    // rendered and privacy-safe, as in the push
	Screen        string    // deep link target
	OccurrenceIDs []string  // the occurrences this notification covers (one, or several when grouped)
}

// DeviceSchedule is the answer: the entries with GeneratedAt <= FireAt < ValidUntil, ordered by FireAt.
type DeviceSchedule struct {
	GeneratedAt  time.Time
	ValidUntil   time.Time
	RefreshAfter time.Time
	RemindersOn  bool // false: P-REMIND is off, the device must cancel every backup
	Truncated    bool // more entries than MaxDeviceScheduleEntries: fetch again by RefreshAfter
	Entries      []DeviceScheduleEntry
	ETag         string // changes whenever RemindersOn, Truncated or Entries change
}

// critical mirrors unitCritical for one reminder template.
func templateCritical(t Template) bool {
	return t.Category != public.Promotional && (t.QuietExempt || t.Priority == public.Critical)
}

// DeviceSchedule returns the caller's on-device backup schedule for the next DeviceScheduleHorizon.
func (m *Module) DeviceSchedule(ctx context.Context, userID string) (DeviceSchedule, error) {
	if !ids.Valid(userID) {
		return DeviceSchedule{}, problem.Forbidden
	}
	now := m.now().UTC()
	out := DeviceSchedule{GeneratedAt: now, ValidUntil: now.Add(DeviceScheduleHorizon), RefreshAfter: now.Add(DeviceScheduleRefresh),
		Entries: []DeviceScheduleEntry{}}
	on, err := m.deps.Gate.HasConsent(ctx, userID, PurposeRemind)
	if err != nil {
		// Never a schedule without a positive consent check; the app keeps what it has and asks again.
		return DeviceSchedule{}, problem.Unavailable
	}
	if !on {
		out.ETag = scheduleTag(out)
		return out, nil
	}
	out.RemindersOn = true
	if m.deps.Occurrences == nil {
		return DeviceSchedule{}, problem.Unavailable
	}
	occs, err := m.deps.Occurrences.ListOccurrences(ctx, schedulercore.ListOccurrencesRequest{SubjectRef: userID, From: now.Add(-snoozeLookback), To: out.ValidUntil})
	if err != nil {
		m.log.WarnContext(ctx, "notifications.device_schedule_unavailable", "error_code", string(adapters.KindOf(err)))
		return DeviceSchedule{}, problem.Unavailable
	}
	prefs, err := m.Preferences(ctx, userID)
	if err != nil {
		return DeviceSchedule{}, err
	}
	loc, lerr := time.LoadLocation(prefs.TimeZone)
	if lerr != nil {
		loc, _ = time.LoadLocation(m.cfg.DefaultTimeZone)
	}
	type group struct {
		key  string
		fire time.Time
		tpl  Template
		occs []schedulercore.Occurrence
	}
	groups := map[string]*group{}
	for _, o := range occs {
		if _, ok := reminderKinds[string(o.TypeKey)]; !ok {
			continue
		}
		if o.State != "pending" && o.State != "dispatched" {
			continue // taken, skipped, missed, cancelled or failed: nothing will be sent
		}
		t, ok := m.tmpl[m.cfg.ReminderTemplates[string(o.TypeKey)]]
		if !ok {
			continue
		}
		// The server notifies at the due time, or at the time a snooze asked for (receiver: lateness and grouping
		// follow that time).
		notify := o.DueAt.UTC()
		if o.NotifyAt != nil && !o.NotifyAt.Before(o.DueAt) {
			notify = o.NotifyAt.UTC()
		}
		crit := templateCritical(t)
		fire := notify
		if !crit && prefs.QuietHoursEnabled {
			// As processUnit: held to the end of quiet hours, and expired (never sent) if that is more than
			// MaxLateness after the notify time.
			if in, end := m.inQuiet(notify, loc); in {
				if end.Sub(notify) > m.cfg.MaxLateness {
					continue
				}
				fire = end
			}
		}
		if fire.Before(now) || !fire.Before(out.ValidUntil) {
			continue
		}
		k := replaceKey(notify, crit)
		g, ok := groups[k]
		if !ok {
			g = &group{key: k, fire: fire, tpl: t}
			groups[k] = g
		}
		g.occs = append(g.occs, o)
	}
	list := make([]*group, 0, len(groups))
	for _, g := range groups {
		sort.Slice(g.occs, func(i, j int) bool { return g.occs[i].ID < g.occs[j].ID })
		list = append(list, g)
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].fire.Equal(list[j].fire) {
			return list[i].fire.Before(list[j].fire)
		}
		return list[i].key < list[j].key
	})
	allow := m.namesAllowed(ctx, userID, prefs)
	type words struct{ neutral, named string }
	memo := map[string]words{} // wording belongs to the activity, not to one occurrence
	for _, g := range list {
		if len(out.Entries) == MaxDeviceScheduleEntries {
			out.Truncated = true
			if last := out.Entries[len(out.Entries)-1].FireAt; last.Before(out.RefreshAfter) {
				out.RefreshAfter = last
			}
			break
		}
		e := DeviceScheduleEntry{ReplaceKey: g.key, FireAt: g.fire}
		if len(g.occs) == 1 {
			o := g.occs[0]
			key := o.ExternalRef + "|" + string(o.TypeKey)
			w, ok := memo[key]
			if !ok {
				n, nm, rok := m.resolveWords(ctx, o.ID, o.ExternalRef, string(o.TypeKey), userID, allow)
				if !rok && m.deps.Resolver != nil {
					m.log.WarnContext(ctx, "notifications.resolver_failed", "reason", "resolver_error")
				}
				w = words{n, nm}
				memo[key] = w
			}
			body, err := m.reminderBody(prefs.Locale, g.tpl, w.neutral, w.named)
			if err != nil {
				m.log.WarnContext(ctx, "notifications.device_schedule_text", "reason", "text_rejected")
				continue // the server would refuse to send it too
			}
			e.TitleKey, e.Body, e.Screen = g.tpl.TitleKey, body, g.tpl.LinkScreen
		} else {
			gt := m.tmpl[m.cfg.GroupedTemplate]
			body, err := m.text(prefs.Locale, gt.BodyKey, map[string]string{"count": strconv.Itoa(len(g.occs))})
			if err != nil {
				m.log.WarnContext(ctx, "notifications.device_schedule_text", "reason", "text_rejected")
				continue
			}
			e.TitleKey, e.Body, e.Screen = gt.TitleKey, body, gt.LinkScreen
		}
		for i, o := range g.occs {
			if i == maxEntryOccurrences {
				break
			}
			e.OccurrenceIDs = append(e.OccurrenceIDs, o.ID)
		}
		out.Entries = append(out.Entries, e)
	}
	out.ETag = scheduleTag(out)
	return out, nil
}

func scheduleTag(s DeviceSchedule) string {
	raw, _ := json.Marshal(struct {
		On, Truncated bool
		Entries       []DeviceScheduleEntry
	}{s.RemindersOn, s.Truncated, s.Entries})
	sum := sha256.Sum256(raw)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// execer is a pool or a transaction.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// requestSync records that the patient's devices should fetch their schedule again. Requests merge into one row
// per patient, and pushes to one patient are at least syncMinInterval apart.
func requestSync(ctx context.Context, q execer, userID string, now time.Time) error {
	_, err := q.Exec(ctx, `INSERT INTO device_sync (recipient_ref, pending, requested_at, next_attempt_at, attempts, updated_at)
		VALUES ($1, true, $2, $2, 0, $2)
		ON CONFLICT (recipient_ref) DO UPDATE SET pending=true, requested_at=EXCLUDED.requested_at, attempts=0, updated_at=EXCLUDED.updated_at,
			next_attempt_at=GREATEST(EXCLUDED.next_attempt_at, COALESCE(device_sync.last_sent_at + make_interval(secs => $3), EXCLUDED.next_attempt_at))`,
		userID, now.UTC(), syncMinInterval.Seconds())
	return err
}

// OnConsentChanged asks the patient's devices to refresh when P-REMIND or P-MEDNAMES changes: a withdrawal must
// reach the backups already on the phone (K-DP-09; consent withdrawal). Other purposes are ignored.
func (m *Module) OnConsentChanged(ctx context.Context, e outbox.Event) error {
	if p := e.Payload["purposeId"]; p != PurposeRemind && p != PurposeMedNames {
		return nil
	}
	if !ids.Valid(e.SubjectRef) {
		return nil // malformed: nothing to do, never retried forever
	}
	return outbox.Once(ctx, m.pool, "notifications.reminder_sync_consent", e, func(tx pgx.Tx) error {
		return requestSync(ctx, tx, e.SubjectRef, m.now())
	})
}

// OnRemindersChanged asks the patient's devices to refresh after clinical created or cancelled reminder
// activities (a prescription signed or amended, an item switched on or off).
func (m *Module) OnRemindersChanged(ctx context.Context, e outbox.Event) error {
	if !ids.Valid(e.SubjectRef) {
		return nil
	}
	return outbox.Once(ctx, m.pool, "notifications.reminder_sync_changed", e, func(tx pgx.Tx) error {
		return requestSync(ctx, tx, e.SubjectRef, m.now())
	})
}

type syncJob struct {
	user      string
	requested time.Time
	attempts  int
}

// processSync sends the due refresh pushes: data only ({"sync":"reminders"}), no title, no body.
func (m *Module) processSync(ctx context.Context, now time.Time) error {
	var jobs []syncJob
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		jobs = jobs[:0]
		rs, err := tx.Query(ctx, `UPDATE device_sync SET lease_until=$2 WHERE recipient_ref IN (SELECT recipient_ref FROM device_sync
			WHERE pending AND next_attempt_at <= $1 AND (lease_until IS NULL OR lease_until <= $1) ORDER BY next_attempt_at LIMIT 100 FOR UPDATE SKIP LOCKED)
			RETURNING recipient_ref::text, requested_at, attempts`, now, now.Add(m.cfg.Lease))
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var j syncJob
			if err := rs.Scan(&j.user, &j.requested, &j.attempts); err != nil {
				return err
			}
			jobs = append(jobs, j)
		}
		return rs.Err()
	})
	if err != nil {
		return err
	}
	for _, j := range jobs {
		sent, transient := m.sendSync(ctx, j)
		if transient {
			att := j.attempts + 1
			if att >= syncMaxAttempts {
				m.log.WarnContext(ctx, "notifications.sync_dropped", "reason", "retry_budget")
				_, err = m.pool.Exec(ctx, `UPDATE device_sync SET pending = (requested_at <> $2), attempts=0, lease_until=NULL, updated_at=$3 WHERE recipient_ref=$1`,
					j.user, j.requested, now)
			} else {
				// A request that arrived meanwhile starts its own attempts.
				_, err = m.pool.Exec(ctx, `UPDATE device_sync SET lease_until=NULL, updated_at=$3,
					attempts = CASE WHEN requested_at = $2 THEN $4 ELSE 0 END,
					next_attempt_at = CASE WHEN requested_at = $2 THEN $5 ELSE next_attempt_at END
					WHERE recipient_ref=$1`, j.user, j.requested, now, att, now.Add(m.backoff(att)))
			}
		} else {
			// Done. A request that arrived meanwhile stays pending, at least syncMinInterval after this push.
			_, err = m.pool.Exec(ctx, `UPDATE device_sync SET pending = (requested_at <> $2), attempts=0, lease_until=NULL, updated_at=$3,
				last_sent_at = CASE WHEN $4 THEN $3 ELSE last_sent_at END,
				next_attempt_at = CASE WHEN requested_at <> $2 AND $4 THEN GREATEST(next_attempt_at, $3 + make_interval(secs => $5)) ELSE next_attempt_at END
				WHERE recipient_ref=$1`, j.user, j.requested, now, sent, syncMinInterval.Seconds())
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// sendSync pushes the refresh hint to each of the patient's devices. sent reports whether any device got it;
// transient whether it should be tried again.
func (m *Module) sendSync(ctx context.Context, j syncJob) (sent, transient bool) {
	if m.deps.PushSend == nil {
		return false, false
	}
	targets, err := m.deps.Push.PushTargets(ctx, j.user, AudiencePatient) // the reminder schedule lives in the patient app
	if err != nil {
		return false, true
	}
	sum := sha256.Sum256([]byte(j.user + "|" + j.requested.UTC().Format(time.RFC3339Nano)))
	key := "sync:" + hex.EncodeToString(sum[:12])
	for _, tg := range targets {
		_, err := m.deps.PushSend.Send(ctx, push.Message{Target: push.Target{Token: tg.Token, Platform: push.Platform(tg.Platform)}, Silent: true,
			Data: map[string]string{"sync": "reminders"}, IdempotencyKey: key + ":" + tg.DeviceID})
		if err == nil {
			sent = true
			continue
		}
		if e, ok := err.(*adapters.Error); ok && e.Kind == adapters.NotFound && e.Reason == push.ReasonTokenInvalid {
			_ = m.deps.Push.InvalidatePushToken(ctx, tg.DeviceID)
			continue
		}
		if r, _ := classify(err); r == resTransient {
			transient = true
		}
	}
	return sent, transient
}

// OnDeviceRevoked tells a device the patient signed out remotely to wipe itself: one silent push with data
// {"signout": <the revoked device ID>}, after which its push token is dropped. The app wipes only when the ID is its
// own: the same phone may have signed in again since (a new device ID with the same OS push token). It then clears
// its tokens, caches, local database and scheduled local notifications (mobile-data-protection §2), so a lost phone stops showing reminders.
// A transient failure is retried by the event system within the 24 hours the target stays available.
func (m *Module) OnDeviceRevoked(ctx context.Context, e outbox.Event) error {
	device := e.Payload["deviceRef"]
	if !ids.Valid(e.SubjectRef) || !ids.Valid(device) {
		return nil // malformed: nothing to do, never retried forever
	}
	return outbox.Once(ctx, m.pool, "notifications.device_signout", e, func(pgx.Tx) error {
		tg, ok, err := m.deps.Push.RevokedPushTarget(ctx, e.SubjectRef, device)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if m.deps.PushSend != nil {
			_, err = m.deps.PushSend.Send(ctx, push.Message{Target: push.Target{Token: tg.Token, Platform: push.Platform(tg.Platform)}, Silent: true,
				Data: map[string]string{"signout": device}, IdempotencyKey: "signout:" + e.ID})
			if err != nil {
				if ae, isAE := err.(*adapters.Error); !isAE || ae.Kind != adapters.NotFound || ae.Reason != push.ReasonTokenInvalid {
					if r, _ := classify(err); r == resTransient {
						return err // rolled back, so the event is handled again
					}
				}
			}
		}
		// The token has no further use once the device has been told (or cannot be reached).
		return m.deps.Push.InvalidatePushToken(ctx, device)
	})
}
