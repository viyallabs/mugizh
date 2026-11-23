package notifications

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/email"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
)

var errText = errors.New("notifications: text rejected")

// text renders catalog text for key, replacing {name} placeholders. Text containing a
// denylisted specialty or diagnosis term, or an unfilled placeholder, is rejected (K-DP-09).
func (m *Module) text(locale, key string, vars map[string]string) (string, error) {
	s, ok := m.deps.Catalog.Text(locale, key)
	if !ok {
		s, ok = m.deps.Catalog.Text(m.cfg.DefaultLocale, key)
	}
	if !ok {
		return "", errText
	}
	for k, v := range vars {
		if m.unsafeVar(v) { // TM-27, TM-41: a variable value is never a link or a placeholder
			return "", errText
		}
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	if strings.ContainsAny(s, "{}") || m.denied(s) {
		return "", errText
	}
	return s, nil
}

// varValues maps variable names to values, with time variables formatted in the recipient's zone (an unreadable
// time is left out, so rendering fails closed rather than showing a wrong time).
func (m *Module) varValues(vs []storedVar, loc *time.Location) map[string]string {
	out := make(map[string]string, len(vs))
	for _, v := range vs {
		if v.Type == string(public.Time) {
			if t, err := time.Parse(time.RFC3339, v.Value); err == nil {
				out[v.Name] = t.In(loc).Format(m.cfg.DateTimeLayout)
			}
			continue
		}
		out[v.Name] = v.Value
	}
	return out
}

func (m *Module) timeOfDay(locale string, t time.Time, loc *time.Location) (string, error) {
	h := t.In(loc).Hour()
	k := "timeofday.night"
	switch {
	case h >= 5 && h < 12:
		k = "timeofday.morning"
	case h >= 12 && h < 17:
		k = "timeofday.afternoon"
	case h >= 17 && h < 21:
		k = "timeofday.evening"
	}
	return m.text(locale, k, nil)
}

// content is what a unit looks like to the patient.
type content struct {
	title   string // translation key
	body    string
	data    map[string]string
	replace string // display-replacement key (reminders only, S-165)
}

// unitCtx carries what rendering needs for one dispatch unit.
type unitCtx struct {
	unit    []row
	tpl     Template
	prefs   Preferences
	loc     *time.Location
	allow   bool // medicine names allowed now (P-MEDNAMES at send time and not discreet)
	neutral string
	named   string
	resolve bool // resolver ran successfully
}

func (u *unitCtx) grouped() bool { return len(u.unit) > 1 }

// prepare resolves reminder wording at send time (algorithms §3 step 3).
func (m *Module) prepare(ctx context.Context, u *unitCtx) {
	lead := u.unit[0]
	if lead.kind != "reminder" || u.grouped() {
		return
	}
	u.allow = m.namesAllowed(ctx, lead.recipient, u.prefs)
	if m.deps.Resolver == nil {
		return
	}
	n, nm, ok := m.resolveWords(ctx, lead.occ, lead.act, lead.typeKey, lead.recipient, u.allow)
	if !ok {
		m.log.WarnContext(ctx, "notifications.resolver_failed", "reason", "resolver_error", "resource_ref", lead.id)
		return
	}
	u.neutral, u.named, u.resolve = n, nm, true
}

// namesAllowed reports whether a notification may name a medicine now: P-MEDNAMES is on and discreet mode is
// off. A failed consent check never allows a name (K-DP-09, fail closed).
func (m *Module) namesAllowed(ctx context.Context, user string, p Preferences) bool {
	if p.Discreet {
		return false
	}
	ok, err := m.deps.Gate.HasConsent(ctx, user, PurposeMedNames)
	return err == nil && ok
}

// resolveWords asks the resolver for a reminder's wording. Text that is too long or denylisted is dropped, and
// the named wording only ever comes back when allow is true. ok is false when there is no resolver or it failed.
func (m *Module) resolveWords(ctx context.Context, occ, act, typeKey, user string, allow bool) (neutral, named string, ok bool) {
	if m.deps.Resolver == nil {
		return "", "", false
	}
	n, nm, err := m.deps.Resolver.Resolve(ctx, occ, act, typeKey, user, allow)
	if err != nil {
		return "", "", false
	}
	if n != "" && len(n) <= 200 && !m.unsafeVar(n) {
		neutral = n
	}
	if allow && nm != "" && len(nm) <= 200 && !m.unsafeVar(nm) {
		named = nm
	}
	return neutral, named, true
}

// reminderBody is the body of a single reminder: the named wording when allowed, else the neutral one, else the
// template's generic neutral text (a reminder is never blocked for want of wording). The push and the
// on-device backup schedule (S-165) both use it, so they always say the same thing.
func (m *Module) reminderBody(locale string, t Template, neutral, named string) (string, error) {
	if named != "" {
		return named, nil
	}
	if neutral != "" {
		return neutral, nil
	}
	return m.text(locale, t.BodyKey, nil)
}

// replaceKey is the display-replacement key of the reminders the server sends for one notify minute and class
// (critical or not, as they are grouped): the on-device backup entry for the same minute carries the same key,
// so a phone that is online shows one notification, not two (S-165, D-67). It holds no reference to the patient.
func replaceKey(notifyAt time.Time, critical bool) string {
	class := "n"
	if critical {
		class = "c"
	}
	return "rem-" + strconv.FormatInt(notifyAt.UTC().Unix()/60, 10) + "-" + class
}

func (m *Module) pushContent(u *unitCtx) (content, error) {
	lead := u.unit[0]
	loc := u.prefs.Locale
	replace := ""
	if lead.kind == "reminder" && lead.dueAt != nil {
		replace = replaceKey(*lead.dueAt, unitCritical(u.unit, u.tpl))
	}
	switch {
	case u.grouped():
		g := m.tmpl[m.cfg.GroupedTemplate]
		b, err := m.text(loc, g.BodyKey, map[string]string{"count": strconv.Itoa(len(u.unit))})
		if err != nil {
			return content{}, err
		}
		return content{title: g.TitleKey, body: b, data: map[string]string{"screen": g.LinkScreen}, replace: replace}, nil
	case lead.kind == "reminder":
		body, err := m.reminderBody(loc, u.tpl, u.neutral, u.named)
		if err != nil {
			return content{}, err
		}
		return content{title: u.tpl.TitleKey, body: body, data: map[string]string{"screen": u.tpl.LinkScreen, "ref": lead.occ}, replace: replace}, nil
	}
	key := u.tpl.BodyKey
	if u.prefs.Discreet && u.tpl.DiscreetBodyKey != "" {
		key = u.tpl.DiscreetBodyKey
	}
	vm := m.varValues(lead.vars, u.loc)
	b, err := m.text(loc, key, vm)
	if err != nil {
		return content{}, err
	}
	data := map[string]string{"screen": u.tpl.LinkScreen}
	if u.tpl.LinkRefVar != "" {
		data["ref"] = vm[u.tpl.LinkRefVar]
	}
	return content{title: u.tpl.TitleKey, body: b, data: data}, nil
}

func (m *Module) smsVars(u *unitCtx, b *SMSBinding) ([]sms.Var, error) {
	lead := u.unit[0]
	vm := m.varValues(lead.vars, u.loc)
	out := make([]sms.Var, 0, len(b.Vars))
	for _, sv := range b.Vars {
		var val string
		var err error
		switch {
		case strings.HasPrefix(sv.Source, "var:"):
			val = vm[strings.TrimPrefix(sv.Source, "var:")]
		case sv.Source == "time_of_day" && lead.dueAt != nil:
			val, err = m.timeOfDay(u.prefs.Locale, *lead.dueAt, u.loc)
		case sv.Source == "due_at" && lead.dueAt != nil:
			val = lead.dueAt.In(u.loc).Format(m.cfg.DateTimeLayout)
		case sv.Source == "named":
			val = u.named
		}
		if err != nil || val == "" || m.unsafeVar(val) {
			return nil, errText
		}
		out = append(out, sms.Var{Name: sv.Name, Value: val})
	}
	return out, nil
}

// smsMessage builds the registered-template message. The named variant is tried only when the
// medicine name is allowed now; if it does not validate, the neutral template is used.
func (m *Module) smsMessage(u *unitCtx, to string) (sms.Message, error) {
	lead := u.unit[0]
	try := func(b *SMSBinding) (sms.Message, error) {
		vars, err := m.smsVars(u, b)
		if err != nil {
			return sms.Message{}, err
		}
		msg := sms.Message{To: to, Template: b.TemplateID, Vars: vars, IdempotencyKey: lead.id + ":sms"}
		return msg, m.cfg.SMSRegistry.Validate(msg)
	}
	if u.allow && u.named != "" && u.tpl.SMSNamed != nil && !u.grouped() {
		if msg, err := try(u.tpl.SMSNamed); err == nil {
			return msg, nil
		}
	}
	return try(u.tpl.SMS)
}

func (m *Module) emailMessage(u *unitCtx, to string) (email.Message, error) {
	lead := u.unit[0]
	vm := m.varValues(lead.vars, u.loc)
	msg := email.Message{To: to, TemplateKey: u.tpl.Email.TemplateKey, Locale: u.prefs.Locale, IdempotencyKey: lead.id + ":email"}
	for _, n := range u.tpl.Email.Vars {
		v, ok := vm[n]
		if !ok || m.unsafeVar(v) {
			return email.Message{}, errText
		}
		msg.Vars = append(msg.Vars, email.Var{Name: n, Value: v})
	}
	return msg, email.Validate(msg)
}
