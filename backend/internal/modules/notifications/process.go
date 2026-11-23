package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/push"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

type row struct {
	id, kind, tmpl, recipient, priority, category, reason string
	vars                                                  []storedVar
	occ, act, typeKey                                     string
	dueAt                                                 *time.Time
	state                                                 string
	tried                                                 []string
	channel                                               string
	attempt                                               int
	chStart                                               *time.Time
	created                                               time.Time
}

const rowCols = `id::text, kind, template_key, recipient_ref::text, priority, category, reason, vars, occurrence_ref, activity_ref, type_key,
	due_at, state, tried, channel, attempt, channel_started_at, created_at`

func scanRow(s pgx.Row) (row, error) {
	var r row
	var raw []byte
	if err := s.Scan(&r.id, &r.kind, &r.tmpl, &r.recipient, &r.priority, &r.category, &r.reason, &raw, &r.occ, &r.act, &r.typeKey,
		&r.dueAt, &r.state, &r.tried, &r.channel, &r.attempt, &r.chStart, &r.created); err != nil {
		return r, err
	}
	if raw != nil {
		_ = json.Unmarshal(raw, &r.vars)
	}
	return r, nil
}

const dueWhere = `state IN ('queued','fallback') AND next_attempt_at <= $1 AND (lease_until IS NULL OR lease_until <= $1)`

// ProcessDue sends due dispatches, critical first (River-friendly: call it from a periodic
// job). It returns how many dispatch rows it handled.
func (m *Module) ProcessDue(ctx context.Context, now time.Time, limit int) (int, error) {
	now = now.UTC()
	if err := m.checkReceipts(ctx, now); err != nil {
		return 0, err
	}
	rows, err := m.claim(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	for _, u := range m.group(rows) {
		if err := m.processUnit(ctx, now, u); err != nil {
			m.log.ErrorContext(ctx, "notifications.unit_failed", "error_code", "process", "resource_ref", u[0].id) // lease expires, then retried with the same keys
		}
	}
	// Refresh hints for the on-device schedule (S-165). A failure here never holds up the messages.
	if err := m.processSync(ctx, now); err != nil {
		m.log.ErrorContext(ctx, "notifications.sync_failed", "error_code", "process")
	}
	return len(rows), nil
}

func (m *Module) claim(ctx context.Context, now time.Time, limit int) ([]row, error) {
	var out []row
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		out = out[:0]
		lease := now.Add(m.cfg.Lease)
		rs, err := tx.Query(ctx, `UPDATE dispatch SET lease_until=$2 WHERE id IN (SELECT id FROM dispatch WHERE `+dueWhere+`
			ORDER BY (priority='critical') DESC, next_attempt_at, id LIMIT $3 FOR UPDATE SKIP LOCKED) RETURNING `+rowCols, now, lease, limit)
		if err != nil {
			return err
		}
		for rs.Next() {
			r, err := scanRow(rs)
			if err != nil {
				rs.Close()
				return err
			}
			out = append(out, r)
		}
		rs.Close()
		if rs.Err() != nil {
			return rs.Err()
		}
		// Pull in the same-minute siblings of every claimed reminder so they merge (S-166).
		seen := map[string]bool{}
		for _, r := range out {
			seen[r.id] = true
		}
		type gk struct {
			who    string
			minute time.Time
		}
		done := map[gk]bool{}
		for _, r := range slices.Clone(out) {
			if r.kind != "reminder" || r.dueAt == nil {
				continue
			}
			k := gk{r.recipient, r.dueAt.UTC().Truncate(time.Minute)}
			if done[k] {
				continue
			}
			done[k] = true
			rs, err := tx.Query(ctx, `UPDATE dispatch SET lease_until=$2 WHERE id IN (SELECT id FROM dispatch WHERE kind='reminder' AND recipient_ref=$3
				AND `+dueWhere+` AND date_trunc('minute', due_at) = $4 FOR UPDATE SKIP LOCKED) RETURNING `+rowCols, now, lease, k.who, k.minute)
			if err != nil {
				return err
			}
			for rs.Next() {
				x, err := scanRow(rs)
				if err != nil {
					rs.Close()
					return err
				}
				if !seen[x.id] {
					seen[x.id] = true
					out = append(out, x)
				}
			}
			rs.Close()
			if rs.Err() != nil {
				return rs.Err()
			}
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].priority == "critical") != (out[j].priority == "critical") {
			return out[i].priority == "critical"
		}
		if !out[i].created.Equal(out[j].created) {
			return out[i].created.Before(out[j].created)
		}
		return out[i].id < out[j].id
	})
	return out, err
}

// group merges reminders for the same user due in the same minute (E-REM-03).
func (m *Module) group(rows []row) [][]row {
	var units [][]row
	idx := map[string]int{}
	for _, r := range rows {
		if r.kind != "reminder" || r.dueAt == nil {
			units = append(units, []row{r})
			continue
		}
		t := m.tmpl[r.tmpl]
		k := fmt.Sprintf("%s|%d|%d|%s|%s|%v", r.recipient, r.dueAt.UTC().Truncate(time.Minute).Unix(), r.attempt, strings.Join(r.tried, ","), r.state,
			r.priority == "critical" || t.QuietExempt)
		if i, ok := idx[k]; ok {
			units[i] = append(units[i], r)
			continue
		}
		idx[k] = len(units)
		units = append(units, []row{r})
	}
	return units
}

type change struct {
	state          string // "" keeps
	channel        string
	tried          []string
	attempt        int
	chStart        *time.Time
	sentAt         *time.Time
	next           *time.Time
	reason         string
	clearVars      bool
	receiptChecked bool
	emit           bool
}

func (m *Module) apply(ctx context.Context, unit []row, c change, logs []logEntry, now time.Time) error {
	tried := c.tried
	if tried == nil {
		tried = []string{}
	}
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		for _, r := range unit {
			if _, err := tx.Exec(ctx, `UPDATE dispatch SET state=COALESCE(NULLIF($2,''),state), channel=$3, tried=$4, attempt=$5, channel_started_at=$6,
				sent_at=COALESCE($7,sent_at), next_attempt_at=COALESCE($8,next_attempt_at), fail_reason=$9,
				vars=CASE WHEN $10 THEN NULL ELSE vars END, receipt_checked=receipt_checked OR $11, lease_until=NULL, updated_at=$12 WHERE id=$1 AND state <> 'acted'`,
				r.id, c.state, c.channel, tried, c.attempt, c.chStart, c.sentAt, c.next, c.reason, c.clearVars, c.receiptChecked, now); err != nil {
				return err
			}
			for _, l := range logs {
				l.dispatch, l.recipient, l.template, l.at = r.id, r.recipient, r.tmpl, now
				if err := insertLog(ctx, tx, l); err != nil {
					return err
				}
			}
			if c.emit {
				if err := outbox.Emit(ctx, tx, outbox.Event{Type: "notification.failed", SubjectRef: r.recipient,
					Payload: map[string]string{"dispatchId": r.id, "reason": c.reason}}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil && c.emit && m.Alert != nil {
		for _, r := range unit {
			m.Alert(ctx, r.id, c.reason)
		}
	}
	return err
}

func (m *Module) deadLetter(ctx context.Context, unit []row, reason string, tried []string, logs []logEntry, now time.Time) error {
	logs = append(logs, logEntry{outcome: "dead_letter", reason: reason})
	m.log.ErrorContext(ctx, "notifications.dead_letter", "reason", reason, "resource_ref", unit[0].id)
	return m.apply(ctx, unit, change{state: stDeadLetter, tried: tried, reason: reason, clearVars: true, receiptChecked: true, emit: true}, logs, now)
}

func (m *Module) backoff(attempt int) time.Duration {
	d := m.cfg.BackoffBase
	for i := 0; i < attempt && d < m.cfg.BackoffCap; i++ {
		d *= 2
	}
	if d > m.cfg.BackoffCap {
		d = m.cfg.BackoffCap
	}
	return time.Duration(float64(d) * m.rnd()) // full jitter
}

func unitCritical(unit []row, t Template) bool {
	if t.Category == public.Promotional {
		return false // review M2: promotional is never critical
	}
	if t.QuietExempt {
		return true
	}
	for _, r := range unit {
		if r.priority == "critical" {
			return true
		}
	}
	return false
}

func (m *Module) processUnit(ctx context.Context, now time.Time, unit []row) error {
	if unit[0].kind == "reminder" {
		// A patient action (taken, skipped, snoozed) may have finished some of these since they were claimed (S-154).
		live, err := m.liveRows(ctx, unit)
		if err != nil {
			return err // the lease expires and the unit is retried
		}
		if len(live) == 0 {
			return nil
		}
		unit = live
	}
	lead := unit[0]
	t, ok := m.tmpl[lead.tmpl]
	if !ok {
		return m.deadLetter(ctx, unit, "template_missing", lead.tried, nil, now)
	}
	// Review M2: an item with a due time is never sent more than MaxLateness after it (rule D-28),
	// whether it waited for quiet hours to end, for a retry or for a lease.
	if lead.dueAt != nil && now.Sub(*lead.dueAt) > m.cfg.MaxLateness {
		m.log.WarnContext(ctx, "notifications.expired", "reason", "stale", "resource_ref", lead.id)
		return m.apply(ctx, unit, change{state: stExpired, reason: "stale", tried: lead.tried, clearVars: true, receiptChecked: true},
			[]logEntry{{outcome: "expired", reason: "stale"}}, now)
	}
	prefs, err := m.Preferences(ctx, lead.recipient)
	if err != nil {
		return m.retryLater(ctx, unit, now, "preferences", lead.tried)
	}
	loc, lerr := time.LoadLocation(prefs.TimeZone)
	if lerr != nil {
		loc, _ = time.LoadLocation(m.cfg.DefaultTimeZone)
	}
	// Consent at send time (K-DP-09, TC-52): fail closed.
	var purpose string
	switch {
	case t.Category == public.Promotional:
		purpose = PurposeNews
	case lead.kind == "reminder":
		purpose = PurposeRemind
	}
	if purpose != "" {
		okc, err := m.deps.Gate.HasConsent(ctx, lead.recipient, purpose)
		if err != nil {
			return m.retryLater(ctx, unit, now, "consent_check", lead.tried)
		}
		if !okc {
			m.log.WarnContext(ctx, "notifications.blocked", "reason", "consent_missing", "resource_ref", lead.id)
			return m.apply(ctx, unit, change{state: stBlocked, reason: "consent_missing", tried: lead.tried, clearVars: true, receiptChecked: true},
				[]logEntry{{outcome: "blocked", reason: "consent_missing"}}, now)
		}
	}
	critical := unitCritical(unit, t)
	// Quiet hours (K-MSG-03): hold, release at the end of the window. Promotional messages
	// always respect them, whatever the patient's switch says (review M2).
	if !critical && (prefs.QuietHoursEnabled || t.Category == public.Promotional) {
		if in, end := m.inQuiet(now, loc); in {
			return m.apply(ctx, unit, change{channel: lead.channel, tried: lead.tried, attempt: lead.attempt, chStart: lead.chStart, next: &end},
				[]logEntry{{outcome: "held", reason: "quiet_hours"}}, now)
		}
	}
	u := &unitCtx{unit: unit, tpl: t, prefs: prefs, loc: loc}
	m.prepare(ctx, u)

	var remaining []Channel
	for _, c := range m.plan(t, prefs, critical) {
		if !slices.Contains(lead.tried, string(c)) {
			remaining = append(remaining, c)
		}
	}
	tried := slices.Clone(lead.tried)
	attempt, chStart, cur := lead.attempt, lead.chStart, lead.channel
	var logs []logEntry
	for len(remaining) > 0 {
		ch := remaining[0]
		if string(ch) != cur || chStart == nil {
			cur, attempt, chStart = string(ch), 0, &now
		}
		pid, res, reason := m.attempt(ctx, u, ch)
		switch res {
		case resSent:
			tried = append(tried, string(ch))
			noAck := ch != Push
			done := noAck || len(remaining) == 1
			logs = append(logs, logEntry{channel: string(ch), outcome: "sent", provider: pid, attempt: attempt + 1})
			return m.apply(ctx, unit, change{state: stSent, channel: string(ch), tried: tried, attempt: attempt + 1, chStart: chStart, sentAt: &now,
				clearVars: done, receiptChecked: done}, logs, now)
		case resTransient:
			attempt++
			delay := m.backoff(attempt)
			logs = append(logs, logEntry{channel: string(ch), outcome: "retry", reason: reason, attempt: attempt})
			if now.Add(delay).Before(chStart.Add(m.cfg.RetryBudget)) {
				next := now.Add(delay)
				return m.apply(ctx, unit, change{channel: cur, tried: tried, attempt: attempt, chStart: chStart, next: &next}, logs, now)
			}
			logs = append(logs, logEntry{channel: string(ch), outcome: "failed", reason: "retry_budget", attempt: attempt})
		default: // skipped or permanent: next channel
			outcome := "failed"
			if res == resSkip {
				outcome = "skipped"
			}
			logs = append(logs, logEntry{channel: string(ch), outcome: outcome, reason: reason, attempt: attempt + 1})
		}
		tried = append(tried, string(ch))
		remaining = remaining[1:]
		if len(remaining) > 0 {
			logs = append(logs, logEntry{channel: string(remaining[0]), outcome: "fallback", reason: reason})
		}
	}
	return m.deadLetter(ctx, unit, "all_channels_failed", tried, logs, now)
}

// retryLater reschedules without spending a channel attempt (for a failed consent or
// preference lookup).
func (m *Module) retryLater(ctx context.Context, unit []row, now time.Time, reason string, tried []string) error {
	lead := unit[0]
	attempt := lead.attempt + 1
	start := lead.chStart
	if start == nil {
		start = &now
	}
	next := now.Add(m.backoff(attempt))
	if !next.Before(start.Add(m.cfg.RetryBudget)) {
		return m.deadLetter(ctx, unit, reason, tried, nil, now)
	}
	return m.apply(ctx, unit, change{channel: lead.channel, tried: tried, attempt: attempt, chStart: start, next: &next},
		[]logEntry{{outcome: "retry", reason: reason, attempt: attempt}}, now)
}

type result int

const (
	resSent result = iota
	resTransient
	resSkip
	resPermanent
)

func classify(err error) (result, string) {
	k := adapters.KindOf(err)
	switch k {
	case adapters.Timeout, adapters.Unavailable, adapters.RateLimited, "":
		if k == "" {
			return resTransient, "error"
		}
		return resTransient, string(k)
	}
	return resPermanent, string(k)
}

// attempt sends on one channel. The idempotency key is derived from the dispatch ID and
// channel, so a retry after a Timeout never duplicates the message.
func (m *Module) attempt(ctx context.Context, u *unitCtx, ch Channel) (providerID string, res result, reason string) {
	lead := u.unit[0]
	switch ch {
	case Push:
		if m.deps.PushSend == nil {
			return "", resSkip, "no_adapter"
		}
		targets, err := m.deps.Push.PushTargets(ctx, lead.recipient, u.tpl.pushApp())
		if err != nil {
			return "", resTransient, "targets"
		}
		if len(targets) == 0 {
			return "", resSkip, "no_target"
		}
		c, err := m.pushContent(u)
		if err != nil {
			return "", resPermanent, "text_rejected"
		}
		worst, wreason := resSkip, "token_invalid"
		for _, tg := range targets {
			id, err := m.deps.PushSend.Send(ctx, push.Message{Target: push.Target{Token: tg.Token, Platform: push.Platform(tg.Platform)},
				TitleKey: c.title, Body: c.body, Data: c.data, ReplaceKey: c.replace, IdempotencyKey: lead.id + ":push:" + tg.DeviceID})
			if err == nil {
				if providerID == "" {
					providerID = id
				}
				continue
			}
			if e, ok := err.(*adapters.Error); ok && e.Kind == adapters.NotFound && e.Reason == push.ReasonTokenInvalid {
				_ = m.deps.Push.InvalidatePushToken(ctx, tg.DeviceID)
				continue
			}
			r, rs := classify(err)
			if r == resTransient || (worst != resTransient && r == resPermanent) {
				worst, wreason = r, rs
			}
		}
		if providerID != "" {
			return providerID, resSent, ""
		}
		return "", worst, wreason
	case SMS:
		if m.deps.SMS == nil {
			return "", resSkip, "no_adapter"
		}
		phone, err := m.deps.Contacts.PhoneFor(ctx, lead.recipient)
		if err != nil {
			return "", resTransient, "contact"
		}
		if phone == "" || u.tpl.SMS == nil {
			return "", resSkip, "no_contact"
		}
		msg, err := m.smsMessage(u, phone)
		if err != nil {
			return "", resPermanent, "sms_invalid"
		}
		id, err := m.deps.SMS.Send(ctx, msg)
		if err != nil {
			r, rs := classify(err)
			return "", r, rs
		}
		return id, resSent, ""
	case Email:
		if m.deps.Email == nil || m.deps.Emails == nil {
			return "", resSkip, "no_adapter"
		}
		addr, err := m.deps.Emails.EmailFor(ctx, lead.recipient)
		if err != nil {
			return "", resTransient, "contact"
		}
		if addr == "" || u.tpl.Email == nil {
			return "", resSkip, "no_contact"
		}
		msg, err := m.emailMessage(u, addr)
		if err != nil {
			return "", resPermanent, "email_invalid"
		}
		id, err := m.deps.Email.Send(ctx, msg)
		if err != nil {
			r, rs := classify(err)
			return "", r, rs
		}
		return id, resSent, ""
	}
	return "", resSkip, "channel_disabled" // WhatsApp and anything unknown: never sent
}

// checkReceipts moves push dispatches with no app receipt in ReceiptWait to the next channel
// (TC-40: critical items reach SMS even when the phone never shows the push).
func (m *Module) checkReceipts(ctx context.Context, now time.Time) error {
	rs, err := m.pool.Query(ctx, `SELECT `+rowCols+` FROM dispatch WHERE state='sent' AND channel='push' AND receipt_checked=false AND sent_at <= $1 LIMIT 200`,
		now.Add(-m.cfg.ReceiptWait))
	if err != nil {
		return err
	}
	var rows []row
	for rs.Next() {
		r, err := scanRow(rs)
		if err != nil {
			rs.Close()
			return err
		}
		rows = append(rows, r)
	}
	rs.Close()
	if rs.Err() != nil {
		return rs.Err()
	}
	for _, r := range rows {
		t, ok := m.tmpl[r.tmpl]
		prefs, err := m.Preferences(ctx, r.recipient)
		if err != nil {
			continue
		}
		more := false
		if ok {
			for _, c := range m.plan(t, prefs, unitCritical([]row{r}, t)) {
				if !slices.Contains(r.tried, string(c)) {
					more = true
				}
			}
		}
		if more {
			err = m.apply(ctx, []row{r}, change{state: stFallback, tried: r.tried, next: &now, receiptChecked: true},
				[]logEntry{{channel: "push", outcome: "no_receipt", reason: "no_app_receipt"}}, now)
		} else {
			err = m.apply(ctx, []row{r}, change{channel: r.channel, tried: r.tried, clearVars: true, receiptChecked: true}, nil, now)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// MarkDelivered records the app's receipt (or a provider delivery report) for a dispatch.
func (m *Module) MarkDelivered(ctx context.Context, dispatchID string) error {
	now := m.now().UTC()
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var who, tk, ch string
		err := tx.QueryRow(ctx, `UPDATE dispatch SET state='delivered', vars=NULL, receipt_checked=true, lease_until=NULL, updated_at=$2
			WHERE id=$1 AND state IN ('sent','fallback','queued') RETURNING recipient_ref::text, template_key, channel`, dispatchID, now).Scan(&who, &tk, &ch)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		return insertLog(ctx, tx, logEntry{dispatch: dispatchID, recipient: who, template: tk, channel: ch, outcome: "delivered", at: now})
	})
}

// PurgeDeliveryLogs deletes delivery logs and finished dispatches older than the retention
// period (RET-10, 90 days) and clears any variables still held on finished dispatches.
func (m *Module) PurgeDeliveryLogs(ctx context.Context, now time.Time) (int64, error) {
	cut := now.UTC().Add(-m.cfg.LogRetention)
	var n int64
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		t, err := tx.Exec(ctx, `DELETE FROM delivery_log WHERE at < $1`, cut)
		if err != nil {
			return err
		}
		n = t.RowsAffected()
		if _, err := tx.Exec(ctx, `DELETE FROM reminder_action WHERE created_at < $1`, cut); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM dispatch WHERE state NOT IN ('queued','fallback') AND updated_at < $1`, cut); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM device_sync WHERE NOT pending AND updated_at < $1`, now.UTC().Add(-syncKeep)); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE dispatch SET vars=NULL WHERE vars IS NOT NULL AND state NOT IN ('queued','fallback') AND receipt_checked`)
		return err
	})
	return n, err
}

// EraseUser removes a user's preferences, dispatches and delivery logs (DEL-ERASE).
func (m *Module) EraseUser(ctx context.Context, userID string) error {
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := eraseUserTx(ctx, tx, userID, false)
		return err
	})
}

// eraseUserTx deletes in tx and returns how many rows went.
// keepRightsNotices keeps dispatches with reason data_rights (the erasure notices
// themselves, references only) so step 1 of an erasure cannot delete the notice that
// tells the person about it.
func eraseUserTx(ctx context.Context, tx pgx.Tx, userID string, keepRightsNotices bool) (int64, error) {
	var total int64
	dispatch := `DELETE FROM dispatch WHERE recipient_ref=$1`
	if keepRightsNotices {
		dispatch += ` AND reason <> 'data_rights'`
	}
	for _, q := range []string{`DELETE FROM preference WHERE user_ref=$1`, `DELETE FROM delivery_log WHERE recipient_ref=$1`, dispatch, `DELETE FROM reminder_action WHERE user_ref=$1`, `DELETE FROM device_sync WHERE recipient_ref=$1`} {
		t, err := tx.Exec(ctx, q, userID)
		if err != nil {
			return 0, err
		}
		total += t.RowsAffected()
	}
	return total, nil
}
