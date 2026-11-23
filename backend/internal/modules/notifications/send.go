package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/scrub"
)

var (
	reDedupe  = regexp.MustCompile(`^[A-Za-z0-9_:.\-]{1,100}$`)
	reReason  = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,60}$`)
	reNumeric = regexp.MustCompile(`^[0-9]{1,10}$`)
	reAlnum   = regexp.MustCompile(`^[\p{L}\p{N} .,:/'()\-]{1,30}$`)
	reRef     = regexp.MustCompile(`^[A-Za-z0-9_\-.]{1,64}$`)
	reDigits  = regexp.MustCompile(`[0-9]{7,}`)
)

var _ public.Sender = (*Module)(nil)

// storedVar is the JSON form of a variable while a dispatch is in flight.
type storedVar struct {
	Name, Type, Value string
}

// denied reports whether s contains a denylisted specialty or diagnosis term (defence in
// depth for K-DP-09).
func (m *Module) denied(s string) bool {
	for _, re := range m.deny {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func checkVarType(v public.Var) bool {
	switch v.Type {
	case public.Numeric:
		return reNumeric.MatchString(v.Value)
	case public.Alphanumeric:
		return reAlnum.MatchString(v.Value) && utf8.RuneCountInString(v.Value) <= 30 && !reDigits.MatchString(v.Value) && !scrub.HasLinkOrMarkup(v.Value)
	case public.Ref:
		return reRef.MatchString(v.Value)
	case public.Time:
		t, err := time.Parse(time.RFC3339, v.Value)
		return err == nil && t.Year() >= 2000 && t.Year() <= 2200
	}
	return false
}

// Send validates and enqueues one message. It is idempotent by DedupeKey. Consent and
// quiet hours are decided when the message is processed, not here, except that a promotional
// message without P-NEWS is blocked and logged at once (TC-52, E-MSG-02).
func (m *Module) Send(ctx context.Context, r public.Request) (string, error) {
	t, ok := m.tmpl[r.TemplateKey]
	if !ok {
		return "", public.ErrUnknownTemplate
	}
	if t.Reminder || !ids.Valid(r.RecipientRef) || !reDedupe.MatchString(r.DedupeKey) || !reReason.MatchString(r.Reason) {
		return "", public.ErrInvalid
	}
	if r.Category == "" {
		r.Category = public.Service
	}
	if r.Category != t.Category {
		if r.Category == public.Promotional || t.Category == public.Promotional {
			return "", public.ErrPromotionalNotAllowed
		}
		return "", public.ErrInvalid
	}
	if r.Priority == "" {
		r.Priority = t.Priority
	}
	if r.Priority != public.Critical && r.Priority != public.Normal {
		return "", public.ErrInvalid
	}
	// Review M2: a caller may lower a priority but never raise it above the template's (critical
	// skips quiet hours and forces SMS); promotional messages are always normal.
	if r.Priority == public.Critical && t.Priority != public.Critical {
		return "", public.ErrInvalid
	}
	if t.Category == public.Promotional {
		r.Priority = public.Normal
	}
	if len(r.Vars) != len(t.Vars) {
		return "", public.ErrInvalid
	}
	sv := make([]storedVar, 0, len(r.Vars))
	for i, v := range r.Vars {
		if v.Name != t.Vars[i].Name || v.Type != t.Vars[i].Type || !checkVarType(v) || m.denied(v.Value) {
			return "", public.ErrInvalid
		}
		sv = append(sv, storedVar{v.Name, string(v.Type), v.Value})
	}
	raw, _ := json.Marshal(sv)
	now := m.now().UTC()
	id := ids.New()
	state := stQueued
	if t.Category == public.Promotional {
		okc, err := m.deps.Gate.HasConsent(ctx, r.RecipientRef, PurposeNews)
		if err != nil || !okc {
			state = stBlocked // fail closed
		}
	}
	var got string
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO dispatch (id, dedupe_key, kind, template_key, recipient_ref, priority, category, reason, vars, state,
			fail_reason, next_attempt_at, created_at, updated_at)
			VALUES ($1,$2,'message',$3,$4,$5,$6,$7,$8,$9,$10,$11,$11,$11)
			ON CONFLICT (dedupe_key) DO NOTHING RETURNING id`,
			id, r.DedupeKey, t.Key, r.RecipientRef, string(r.Priority), string(r.Category), r.Reason, nullVars(raw, state), state,
			failReason(state), now).Scan(&got)
		if errors.Is(err, pgx.ErrNoRows) {
			return tx.QueryRow(ctx, `SELECT id FROM dispatch WHERE dedupe_key=$1`, r.DedupeKey).Scan(&got)
		}
		if err != nil {
			return err
		}
		if state == stBlocked {
			return insertLog(ctx, tx, logEntry{dispatch: got, recipient: r.RecipientRef, template: t.Key, outcome: "blocked", reason: "consent_missing", at: now})
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if state == stBlocked && got == id {
		m.log.WarnContext(ctx, "notifications.blocked", "reason", "consent_missing", "resource_ref", got)
		return got, public.ErrConsentRequired
	}
	return got, nil
}

func nullVars(raw []byte, state string) any {
	if state == stBlocked {
		return nil
	}
	return raw
}

func failReason(state string) string {
	if state == stBlocked {
		return "consent_missing"
	}
	return ""
}

type logEntry struct {
	dispatch, recipient, template, channel, outcome, reason, provider string
	attempt                                                           int
	at                                                                time.Time
}

func insertLog(ctx context.Context, tx pgx.Tx, e logEntry) error {
	_, err := tx.Exec(ctx, `INSERT INTO delivery_log (id, dispatch_id, recipient_ref, template_key, channel, outcome, reason, provider_message_id, attempt, at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, ids.New(), e.dispatch, e.recipient, e.template, e.channel, e.outcome, e.reason, e.provider, e.attempt, e.at)
	return err
}

// unsafeVar reports whether s may not be placed in an SMS, push or e-mail variable: a denylisted
// term (K-DP-09) or a link, domain or markup (TM-27, TM-41).
func (m *Module) unsafeVar(s string) bool { return m.denied(s) || scrub.HasLinkOrMarkup(s) }
