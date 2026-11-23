package notifications_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters"
	emailfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/email/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/push"
	pushfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/push/fake"
	schedfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms"
	smsfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/testkit"
)

func TestMain(m *testing.M) { testkit.Main(m) }

const registryYAML = `
templates:
  - { id: SMS-02, category: service_implicit, variables: [{ name: doctor_name, type: alphanumeric }, { name: date_time, type: alphanumeric }] }
  - { id: SMS-10, category: service_implicit, variables: [{ name: time_of_day, type: alphanumeric }] }
  - { id: SMS-10-N, category: service_implicit, variables: [{ name: medicine_name, type: alphanumeric }] }
  - { id: SMS-11, category: service_implicit, variables: [{ name: date_time, type: alphanumeric }] }
  - { id: SMS-14, category: service_implicit, variables: [{ name: date_time, type: alphanumeric }] }
  - { id: SMS-P1, category: promotional, variables: [] }
`

var catalog = map[string]string{
	"notif.title":                  "Reminder",
	"notif.booking.confirmed.body": "Your appointment with {doctor_name} is confirmed for {date_time}",
	"notif.booking.discreet.body":  "Open the app for an update",
	"notif.reminder.body":          "Time for your medicine",
	"notif.followup.body":          "You have a health task due",
	"notif.reminders.grouped":      "You have {count} reminders due now",
	"notif.security.body":          "New sign-in to your account",
	"notif.promo.body":             "News about new features",
	"timeofday.morning":            "morning", "timeofday.afternoon": "afternoon", "timeofday.evening": "evening", "timeofday.night": "night",
}

type cat struct{}

func (cat) Text(_, key string) (string, bool) { s, ok := catalog[key]; return s, ok }

type gate struct {
	mu  sync.Mutex
	has map[string]bool
	err error // when set, every consent check fails with it
}

func (g *gate) set(user, purpose string, v bool) {
	g.mu.Lock()
	g.has[user+"|"+purpose] = v
	g.mu.Unlock()
}
func (g *gate) HasConsent(_ context.Context, u, p string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return false, g.err
	}
	return g.has[u+"|"+p], nil
}
func (g *gate) Authorize(context.Context, rp.Request) (rp.Decision, error) { return rp.Decision{}, nil }

type resolver struct {
	mu      sync.Mutex
	allowed []bool
	neutral string
	named   string
}

func (r *resolver) Resolve(_ context.Context, _, _, _, _ string, allow bool) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.allowed = append(r.allowed, allow)
	if allow {
		return r.neutral, r.named, nil
	}
	return r.neutral, "", nil
}

type contacts struct{ phones map[string]string }

func (c contacts) PhoneFor(_ context.Context, u string) (string, error) { return c.phones[u], nil }

type targets struct {
	mu          sync.Mutex
	by          map[string][]public.PushTarget
	invalidated []string
	revoked     map[string]public.PushTarget // "user|device" -> target of a device signed out remotely
	appOf       map[string]string            // device -> app (unset: every app, for tests that do not care)
	apps        []string                     // the app asked for, per lookup
}

func (t *targets) PushTargets(_ context.Context, u, app string) ([]public.PushTarget, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.apps = append(t.apps, app)
	var out []public.PushTarget
	for _, tg := range t.by[u] {
		if app == "" || t.appOf[tg.DeviceID] == "" || t.appOf[tg.DeviceID] == app {
			out = append(out, tg)
		}
	}
	return out, nil
}
func (t *targets) InvalidatePushToken(_ context.Context, d string) error {
	t.mu.Lock()
	t.invalidated = append(t.invalidated, d)
	for k, v := range t.revoked {
		if v.DeviceID == d {
			delete(t.revoked, k)
		}
	}
	t.mu.Unlock()
	return nil
}

type fx struct {
	env    *testkit.Env
	m      *notifications.Module
	gate   *gate
	res    *resolver
	push   *pushfake.Sender
	sms    *smsfake.Sender
	email  *emailfake.Sender
	tg     *targets
	phones contacts
	now    time.Time
	alerts []string
	smsOut sms.Sender // what the module is wired to (may wrap sms)
	sched  *schedfake.Client
}

var secret = []byte("test-secret-not-real")

func templates() []notifications.Template {
	return []notifications.Template{
		{Key: "booking.confirmed", MessageType: "booking", Category: public.Service, Priority: public.Normal, Channels: []notifications.Channel{notifications.Push, notifications.SMS},
			Vars:     []notifications.VarSpec{{Name: "doctor_name", Type: public.Alphanumeric}, {Name: "date_time", Type: public.Alphanumeric}, {Name: "booking_id", Type: public.Ref}},
			TitleKey: "notif.title", BodyKey: "notif.booking.confirmed.body", DiscreetBodyKey: "notif.booking.discreet.body", LinkScreen: "booking", LinkRefVar: "booking_id",
			SMS: &notifications.SMSBinding{TemplateID: "SMS-02", Vars: []notifications.SMSVar{{Name: "doctor_name", Source: "var:doctor_name"}, {Name: "date_time", Source: "var:date_time"}}}},
		{Key: "booking.urgent", MessageType: "booking", Category: public.Service, Priority: public.Critical, Channels: []notifications.Channel{notifications.Push, notifications.SMS},
			Vars:     []notifications.VarSpec{{Name: "doctor_name", Type: public.Alphanumeric}, {Name: "date_time", Type: public.Alphanumeric}, {Name: "booking_id", Type: public.Ref}},
			TitleKey: "notif.title", BodyKey: "notif.booking.confirmed.body", DiscreetBodyKey: "notif.booking.discreet.body", LinkScreen: "booking", LinkRefVar: "booking_id",
			SMS: &notifications.SMSBinding{TemplateID: "SMS-02", Vars: []notifications.SMSVar{{Name: "doctor_name", Source: "var:doctor_name"}, {Name: "date_time", Source: "var:date_time"}}}},
		{Key: "booking.timed", MessageType: "booking", Category: public.Service, Priority: public.Critical, QuietExempt: true, Channels: []notifications.Channel{notifications.Push, notifications.SMS},
			Vars:     []notifications.VarSpec{{Name: "doctor_name", Type: public.Alphanumeric}, {Name: "date_time", Type: public.Time}},
			TitleKey: "notif.title", BodyKey: "notif.booking.confirmed.body", LinkScreen: "booking",
			SMS: &notifications.SMSBinding{TemplateID: "SMS-02", Vars: []notifications.SMSVar{{Name: "doctor_name", Source: "var:doctor_name"}, {Name: "date_time", Source: "var:date_time"}}}},
		{Key: "reminder.medicine", MessageType: "reminder", Category: public.Service, Priority: public.Critical, Reminder: true, QuietExempt: true,
			Channels: []notifications.Channel{notifications.Push, notifications.SMS}, TitleKey: "notif.title", BodyKey: "notif.reminder.body", LinkScreen: "reminder",
			SMS:      &notifications.SMSBinding{TemplateID: "SMS-10", Vars: []notifications.SMSVar{{Name: "time_of_day", Source: "time_of_day"}}},
			SMSNamed: &notifications.SMSBinding{TemplateID: "SMS-10-N", Vars: []notifications.SMSVar{{Name: "medicine_name", Source: "named"}}}},
		{Key: "reminder.followup", MessageType: "reminder_followup", Category: public.Service, Priority: public.Normal, Reminder: true,
			Channels: []notifications.Channel{notifications.Push, notifications.SMS}, TitleKey: "notif.title", BodyKey: "notif.followup.body", LinkScreen: "reminder",
			SMS: &notifications.SMSBinding{TemplateID: "SMS-11", Vars: []notifications.SMSVar{{Name: "date_time", Source: "due_at"}}}},
		{Key: "reminder.grouped", MessageType: "reminder", Category: public.Service, Priority: public.Critical, Reminder: true,
			Channels: []notifications.Channel{notifications.Push}, TitleKey: "notif.title", BodyKey: "notif.reminders.grouped", LinkScreen: "reminders"},
		{Key: "security.alert", MessageType: "security", Category: public.Service, Priority: public.Critical, QuietExempt: true,
			Channels: []notifications.Channel{notifications.SMS, notifications.Push}, Vars: []notifications.VarSpec{{Name: "date_time", Type: public.Alphanumeric}},
			TitleKey: "notif.title", BodyKey: "notif.security.body", LinkScreen: "security",
			SMS: &notifications.SMSBinding{TemplateID: "SMS-14", Vars: []notifications.SMSVar{{Name: "date_time", Source: "var:date_time"}}}},
		{Key: "promo.news", MessageType: "news", Category: public.Promotional, Priority: public.Normal,
			Channels: []notifications.Channel{notifications.Push, notifications.SMS}, TitleKey: "notif.title", BodyKey: "notif.promo.body", LinkScreen: "news",
			SMS: &notifications.SMSBinding{TemplateID: "SMS-P1"}},
		{Key: "receipt.paid", MessageType: "receipt", Category: public.Service, Priority: public.Normal, Channels: []notifications.Channel{notifications.Email},
			Vars: []notifications.VarSpec{{Name: "receipt_id", Type: public.Ref}}, TitleKey: "notif.title", BodyKey: "notif.title", LinkScreen: "receipt",
			Email: &notifications.EmailBinding{TemplateKey: "receipt.paid", Vars: []string{"receipt_id"}}},
	}
}

func config(reg *sms.Registry) notifications.Config {
	return notifications.Config{
		Templates: templates(), GroupedTemplate: "reminder.grouped",
		ReminderTemplates: map[string]string{"medicine": "reminder.medicine", "followup": "reminder.followup", "med.dose": "reminder.medicine", "injection.dose": "reminder.medicine", "test.due": "reminder.followup"},
		ChannelsEnabled:   map[string]bool{"push": true, "sms": true, "email": true, "whatsapp": false},
		QuietStart:        "21:00", QuietEnd: "08:00", DefaultTimeZone: "Asia/Kolkata", DefaultLocale: "en", SupportedLocales: []string{"en", "hi"},
		DateTimeLayout: "Mon 2 Jan, 3:04 PM", Denylist: []string{"cardiology", "diabetes", "oncologist"}, SMSRegistry: reg,
		LogRetention: 90 * 24 * time.Hour, RetryBudget: 5 * time.Minute, ReceiptWait: 5 * time.Minute, GroupHold: 0,
		BackoffBase: 10 * time.Second, BackoffCap: time.Minute, Lease: 30 * time.Second,
	}
}

func setup(t *testing.T) *fx {
	t.Helper()
	env := testkit.New(t, testkit.Spec{Sources: []migrate.Source{dbschema.Source(), audit.Source(), notifications.Source()}})
	reg, err := sms.ParseRegistry([]byte(registryYAML))
	if err != nil {
		t.Fatal(err)
	}
	f := &fx{env: env, gate: &gate{has: map[string]bool{}}, res: &resolver{neutral: "Time for your medicine", named: "Metformin 500"},
		push: &pushfake.Sender{}, sms: &smsfake.Sender{Registry: reg}, email: &emailfake.Sender{},
		tg: &targets{by: map[string][]public.PushTarget{}}, phones: contacts{phones: map[string]string{}},
		now: time.Date(2026, 11, 17, 6, 30, 0, 0, time.UTC)} // 12:00 in the default zone
	f.smsOut = f.sms
	f.sched = &schedfake.Client{Now: func() time.Time { return f.now }}
	f.build(t, config(reg))
	return f
}

func (f *fx) build(t *testing.T, cfg notifications.Config) {
	t.Helper()
	m, err := notifications.New(f.env.Pools.For("notifications"), cfg, notifications.Deps{Gate: f.gate, Resolver: f.res, Contacts: f.phones, Push: f.tg,
		SMS: f.smsOut, PushSend: f.push, Email: f.email, Catalog: cat{}, Occurrences: f.sched}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	m.SetClock(func() time.Time { return f.now })
	m.SetJitter(func() float64 { return 0.999 })
	m.Alert = func(_ context.Context, id, reason string) { f.alerts = append(f.alerts, id+":"+reason) }
	f.m = m
}

// user creates a user with a phone and one push device.
func (f *fx) user(withPush bool) string {
	u := ids.New()
	f.phones.phones[u] = "+000" + strconv.Itoa(len(f.phones.phones)+1000000)
	if withPush {
		f.tg.by[u] = []public.PushTarget{{DeviceID: ids.New(), Platform: "android", Token: "tok-" + u}}
	}
	return u
}

func (f *fx) run(t *testing.T, at time.Time) {
	t.Helper()
	f.now = at
	if _, err := f.m.ProcessDue(context.Background(), at, 100); err != nil {
		t.Fatal(err)
	}
}

func (f *fx) state(t *testing.T, id string) string {
	t.Helper()
	var s string
	if err := f.env.Owner.QueryRow(context.Background(), `SELECT state FROM notifications.dispatch WHERE id=$1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fx) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := f.env.Owner.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func booking(u, key string) public.Request {
	return public.Request{TemplateKey: "booking.confirmed", RecipientRef: u, Reason: "booking.confirmed", DedupeKey: key,
		Vars: []public.Var{{Name: "doctor_name", Type: public.Alphanumeric, Value: "Dr Rao"}, {Name: "date_time", Type: public.Alphanumeric, Value: "Tue 17 Nov, 10:30 AM"},
			{Name: "booking_id", Type: public.Ref, Value: ids.New()}}}
}

// event posts a signed occurrence.due callback.
func (f *fx) event(t *testing.T, user, typeKey string, due time.Time) (*httptest.ResponseRecorder, string) {
	t.Helper()
	eid := ids.New()
	body := fmt.Sprintf(`{"occurrenceId":%q,"activityExternalRef":"act-1","typeKey":%q,"dueAt":%q,"subjectRef":%q,"eventId":%q}`,
		ids.New(), typeKey, due.UTC().Format(time.RFC3339), user, eid)
	return f.post(t, []byte(body), strconv.FormatInt(f.now.Unix(), 10), eid, ""), eid
}

func (f *fx) post(t *testing.T, body []byte, ts, eid, sigOverride string) *httptest.ResponseRecorder {
	t.Helper()
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	sig := "v1=" + hex.EncodeToString(mac.Sum(nil)) // scheduler-core's algorithm, written out independently
	if sigOverride != "" {
		sig = sigOverride
	}
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/scheduler/occurrence-due", bytes.NewReader(body))
	req.Header.Set(notifications.HeaderTimestamp, ts)
	req.Header.Set(notifications.HeaderSignature, sig)
	req.Header.Set(notifications.HeaderEventID, eid)
	rec := httptest.NewRecorder()
	f.m.OccurrenceDueHandler(secret, 5*time.Minute).ServeHTTP(rec, req)
	return rec
}

func TestTC09_NoSpecialtyOrDiagnosisInAnyNotification(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	// A variable that names a specialty or condition is refused at enqueue.
	for _, bad := range []string{"Cardiology clinic", "Diabetes care"} {
		r := booking(u, "k-"+bad)
		r.Vars[0].Value = bad
		if _, err := f.m.Send(context.Background(), r); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
	// The resolver (clinical) returns wording with a condition: the generic text is sent instead.
	f.gate.set(u, "P-REMIND", true)
	f.res.neutral = "Time for your diabetes tablet"
	if rec, _ := f.event(t, u, "medicine", f.now); rec.Code != http.StatusAccepted {
		t.Fatalf("receiver: %d", rec.Code)
	}
	f.run(t, f.now.Add(time.Second))
	sent := f.visible()
	if len(sent) != 1 || sent[0].Body != "Time for your medicine" {
		t.Fatalf("want generic text, got %+v", sent)
	}
	for _, m := range sent {
		if strings.Contains(strings.ToLower(m.Body), "diabetes") {
			t.Fatal("condition shown")
		}
	}
	// A normal booking message carries no specialty either.
	id, err := f.m.Send(context.Background(), booking(u, "ok-1"))
	if err != nil {
		t.Fatal(err)
	}
	f.run(t, f.now.Add(time.Minute))
	if f.state(t, id) != "sent" {
		t.Fatal(f.state(t, id))
	}
}

func TestTC09_MedicineNameOnlyWithConsent_AndWithdrawalSwitchesToNeutral(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	f.gate.set(u, "P-REMIND", true)
	// No P-MEDNAMES: neutral, and the resolver was told names are not allowed.
	f.event(t, u, "medicine", f.now)
	f.run(t, f.now.Add(time.Second))
	if b := f.visible()[0].Body; b != "Time for your medicine" || f.res.allowed[0] {
		t.Fatalf("neutral expected: %q %v", b, f.res.allowed)
	}
	// With P-MEDNAMES: named wording.
	f.gate.set(u, "P-MEDNAMES", true)
	f.event(t, u, "medicine", f.now.Add(10*time.Minute))
	f.run(t, f.now.Add(10*time.Minute+time.Second))
	if b := f.visible()[1].Body; b != "Metformin 500" {
		t.Fatalf("named expected: %q", b)
	}
	// Consent given when scheduled, withdrawn before sending: neutral (checked at send time).
	later := f.now.Add(time.Hour)
	f.now = later
	f.event(t, u, "medicine", later)
	f.gate.set(u, "P-MEDNAMES", false)
	f.run(t, later.Add(time.Second))
	if b := f.visible()[2].Body; b != "Time for your medicine" {
		t.Fatalf("withdrawal must switch to neutral: %q", b)
	}
	// Discreet mode wins over consent.
	f.gate.set(u, "P-MEDNAMES", true)
	if err := f.m.SetPreferences(context.Background(), u, notifications.Preferences{Channels: map[string][]notifications.Channel{}, Discreet: true,
		QuietHoursEnabled: true, TimeZone: "Asia/Kolkata", Locale: "en"}); err != nil {
		t.Fatal(err)
	}
	f.now = later.Add(time.Hour)
	f.event(t, u, "medicine", f.now)
	f.run(t, f.now.Add(time.Second))
	if b := f.visible()[3].Body; b != "Time for your medicine" {
		t.Fatalf("discreet: %q", b)
	}
}

func TestTC09_SMSNamedTemplateOnlyWithConsent(t *testing.T) {
	f := setup(t)
	u := f.user(false) // no device: the critical reminder goes to SMS
	f.gate.set(u, "P-REMIND", true)
	f.event(t, u, "medicine", f.now)
	f.run(t, f.now.Add(time.Second))
	m := f.sms.Sent(f.phones.phones[u])
	if len(m) != 1 || m[0].Template != "SMS-10" || m[0].Vars[0].Value != "afternoon" {
		t.Fatalf("%+v", m)
	}
	f.gate.set(u, "P-MEDNAMES", true)
	f.event(t, u, "medicine", f.now.Add(time.Hour))
	f.run(t, f.now.Add(time.Hour+time.Second))
	m = f.sms.Sent(f.phones.phones[u])
	if len(m) != 2 || m[1].Template != "SMS-10-N" || m[1].Vars[0].Value != "Metformin 500" {
		t.Fatalf("%+v", m)
	}
}

func TestTC52_PromotionalWithoutConsentIsBlockedAndLogged(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	req := public.Request{TemplateKey: "promo.news", RecipientRef: u, Category: public.Promotional, Reason: "news.feature", DedupeKey: "n1"}
	id, err := f.m.Send(context.Background(), req)
	if err != public.ErrConsentRequired || id == "" {
		t.Fatalf("want consent required with an ID, got %q %v", id, err)
	}
	f.run(t, f.now.Add(time.Minute))
	if len(f.visible()) != 0 || f.sms.Count() != 0 {
		t.Fatal("nothing may be sent")
	}
	if f.state(t, id) != "blocked" || f.count(t, `SELECT count(*) FROM notifications.delivery_log WHERE dispatch_id=$1 AND outcome='blocked'`, id) != 1 {
		t.Fatal("blocked attempt must be logged")
	}
	// A promotional category on a service template, and the reverse, are refused.
	r := booking(u, "x")
	r.Category = public.Promotional
	if _, err := f.m.Send(context.Background(), r); err != public.ErrPromotionalNotAllowed {
		t.Fatal(err)
	}
	if _, err := f.m.Send(context.Background(), public.Request{TemplateKey: "promo.news", RecipientRef: u, Category: public.Service, Reason: "x.y", DedupeKey: "n2"}); err != public.ErrPromotionalNotAllowed {
		t.Fatal(err)
	}
	// With consent it is queued; withdrawn before sending, it is blocked at send time.
	f.gate.set(u, "P-NEWS", true)
	id2, err := f.m.Send(context.Background(), public.Request{TemplateKey: "promo.news", RecipientRef: u, Category: public.Promotional, Reason: "news.feature", DedupeKey: "n3"})
	if err != nil {
		t.Fatal(err)
	}
	f.gate.set(u, "P-NEWS", false)
	f.run(t, f.now.Add(2*time.Minute))
	if f.state(t, id2) != "blocked" || len(f.visible()) != 0 {
		t.Fatal("withdrawal must block at send time")
	}
}

func TestEREM03_TenRemindersDueTogetherAreOneNotification(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	f.gate.set(u, "P-REMIND", true)
	due := f.now.Truncate(time.Minute)
	for i := 0; i < 10; i++ {
		if rec, _ := f.event(t, u, "medicine", due.Add(time.Duration(i)*time.Second)); rec.Code != http.StatusAccepted {
			t.Fatal(rec.Code)
		}
	}
	other := f.user(true)
	f.gate.set(other, "P-REMIND", true)
	f.event(t, other, "medicine", due)
	if _, err := f.m.ProcessDue(context.Background(), f.now.Add(time.Second), 1); err != nil {
		t.Fatal(err)
	}
	sent := f.visible()
	var mine []push.Message
	for _, s := range sent {
		if s.Target.Token == "tok-"+u {
			mine = append(mine, s)
		}
	}
	if len(mine) != 1 || mine[0].Body != "You have 10 reminders due now" {
		t.Fatalf("want one grouped notification, got %+v", mine)
	}
	f.run(t, f.now.Add(2*time.Second))
	if len(f.visible()) != 2 { // the other user's reminder is separate
		t.Fatalf("sent %d", len(f.visible()))
	}
	if n := f.count(t, `SELECT count(*) FROM notifications.dispatch WHERE recipient_ref=$1 AND state='sent'`, u); n != 10 {
		t.Fatalf("all ten rows must be sent, got %d", n)
	}
}

func TestTC40_CriticalItemFallsBackToSMSOnTheServer(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	f.gate.set(u, "P-REMIND", true)
	// The patient allowed push only for reminders: critical items still reach SMS.
	if err := f.m.SetPreferences(context.Background(), u, notifications.Preferences{Channels: map[string][]notifications.Channel{"reminder": {notifications.Push}},
		QuietHoursEnabled: true, TimeZone: "Asia/Kolkata", Locale: "en"}); err != nil {
		t.Fatal(err)
	}
	f.event(t, u, "medicine", f.now)
	f.run(t, f.now.Add(time.Second))
	if len(f.visible()) != 1 || f.sms.Count() != 0 {
		t.Fatal("push first")
	}
	f.run(t, f.now.Add(4*time.Minute)) // still inside the receipt window
	if f.sms.Count() != 0 {
		t.Fatal("too early for SMS")
	}
	f.run(t, f.now.Add(6*time.Minute)) // no app receipt
	f.run(t, f.now.Add(6*time.Minute+time.Second))
	if f.sms.Count() != 1 {
		t.Fatalf("SMS fallback expected, got %d", f.sms.Count())
	}
}

func TestTC40_AppReceiptCancelsFallback_AndNonCriticalNeedsSMSPermission(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	f.gate.set(u, "P-REMIND", true)
	f.event(t, u, "medicine", f.now)
	f.run(t, f.now.Add(time.Second))
	var id string
	_ = f.env.Owner.QueryRow(context.Background(), `SELECT id::text FROM notifications.dispatch WHERE recipient_ref=$1`, u).Scan(&id)
	if err := f.m.MarkDelivered(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	f.run(t, f.now.Add(10*time.Minute))
	if f.sms.Count() != 0 {
		t.Fatal("receipt received: no SMS")
	}
	// Non-critical follow-up: SMS not allowed by the patient, push fails: no SMS is sent.
	f.push.FailWith = adapters.E(adapters.Unavailable, "DOWN")
	f.event(t, u, "followup", f.now.Add(20*time.Minute))
	for i := 0; i < 40; i++ {
		f.run(t, f.now.Add(20*time.Minute+time.Duration(i)*30*time.Second))
	}
	if f.sms.Count() != 0 {
		t.Fatal("SMS only where the patient allowed it, or for critical items")
	}
}

func TestQuietHours_HoldReleaseAndExemptions(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	f.gate.set(u, "P-REMIND", true)
	night := time.Date(2026, 11, 17, 16, 30, 0, 0, time.UTC) // 22:00 in the default zone
	f.now = night
	id, err := f.m.Send(context.Background(), booking(u, "q1"))
	if err != nil {
		t.Fatal(err)
	}
	f.run(t, night)
	if len(f.visible()) != 0 || f.state(t, id) != "queued" {
		t.Fatal("must be held")
	}
	// Exempt: security alert (SMS first), and a medicine reminder at the chosen time.
	if _, err := f.m.Send(context.Background(), public.Request{TemplateKey: "security.alert", RecipientRef: u, Reason: "security.new_device", DedupeKey: "s1",
		Vars: []public.Var{{Name: "date_time", Type: public.Alphanumeric, Value: "Tue 17 Nov, 10:00 PM"}}}); err != nil {
		t.Fatal(err)
	}
	f.event(t, u, "medicine", night)
	f.event(t, u, "followup", night.Add(2*time.Minute)) // not exempt: held
	f.run(t, night.Add(time.Second))
	f.run(t, night.Add(3*time.Minute))
	if f.sms.Count() != 1 {
		t.Fatalf("security alert must go out at night, sms=%d", f.sms.Count())
	}
	if n := len(f.visible()); n != 1 {
		t.Fatalf("only the medicine reminder at night, got %d", n)
	}
	// Release at the end of quiet hours (08:00 in the zone = 02:30 UTC next day).
	f.run(t, time.Date(2026, 11, 18, 2, 29, 0, 0, time.UTC))
	if len(f.visible()) != 1 {
		t.Fatal("not yet")
	}
	f.run(t, time.Date(2026, 11, 18, 2, 31, 0, 0, time.UTC))
	if f.state(t, id) != "sent" || len(f.visible()) != 2 { // the held follow-up reminder is 10 h late: expired, not sent (review M2)
		t.Fatalf("held messages released: state=%s sent=%d", f.state(t, id), len(f.visible()))
	}
	// Quiet hours switched off by the patient: nothing is held.
	if err := f.m.SetPreferences(context.Background(), u, notifications.Preferences{QuietHoursEnabled: false, TimeZone: "Asia/Kolkata", Locale: "en"}); err != nil {
		t.Fatal(err)
	}
	f.now = night.Add(24 * time.Hour)
	id2, _ := f.m.Send(context.Background(), booking(u, "q2"))
	f.run(t, f.now)
	if f.state(t, id2) != "sent" {
		t.Fatal("quiet hours off")
	}
}

func TestRetriesThenFallbackThenDeadLetter(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	f.push.FailWith = adapters.E(adapters.Unavailable, "DOWN")
	f.sms.FailWith = adapters.E(adapters.Unavailable, "DOWN")
	r := booking(u, "d1")
	r.TemplateKey = "booking.urgent"
	id, err := f.m.Send(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	start := f.now
	for i := 0; i < 80 && f.state(t, id) != "dead_letter"; i++ {
		f.run(t, start.Add(time.Duration(i)*20*time.Second))
	}
	if f.state(t, id) != "dead_letter" {
		t.Fatal("expected dead letter")
	}
	if len(f.alerts) != 1 {
		t.Fatalf("alert hook: %v", f.alerts)
	}
	if f.count(t, `SELECT count(*) FROM notifications.event_outbox WHERE type='notification.failed'`) != 1 {
		t.Fatal("notification.failed event")
	}
	if f.count(t, `SELECT count(*) FROM notifications.delivery_log WHERE dispatch_id=$1 AND outcome='fallback'`, id) < 1 ||
		f.count(t, `SELECT count(*) FROM notifications.delivery_log WHERE dispatch_id=$1 AND outcome='retry'`, id) < 2 {
		t.Fatal("retries and fallback must be logged")
	}
	if f.count(t, `SELECT count(*) FROM notifications.dispatch WHERE id=$1 AND vars IS NULL`, id) != 1 {
		t.Fatal("variables must be cleared at the final state")
	}
	// A permanent rejection falls back at once.
	f.push.FailWith, f.sms.FailWith = nil, nil
	f.push.FailWith = adapters.E(adapters.Rejected, "DND")
	r2 := booking(u, "d2")
	r2.TemplateKey = "booking.urgent"
	id2, _ := f.m.Send(context.Background(), r2)
	f.run(t, f.now.Add(time.Hour))
	if f.state(t, id2) != "sent" || f.sms.Count() != 1 {
		t.Fatalf("fallback to SMS after rejection: %s %d", f.state(t, id2), f.sms.Count())
	}
}

type timeoutOnce struct {
	*smsfake.Sender
	n int
}

func (s *timeoutOnce) Send(ctx context.Context, m sms.Message) (string, error) {
	id, err := s.Sender.Send(ctx, m)
	if err == nil && s.n == 0 {
		s.n++
		return "", adapters.E(adapters.Timeout, "LOST") // the provider accepted it but we never heard
	}
	return id, err
}

func TestNoDuplicateSMSAfterTimeout(t *testing.T) {
	f := setup(t)
	f.smsOut = &timeoutOnce{Sender: f.sms}
	reg, _ := sms.ParseRegistry([]byte(registryYAML))
	f.build(t, config(reg))
	u := f.user(false)
	r := booking(u, "t1")
	r.TemplateKey = "booking.urgent"
	id, _ := f.m.Send(context.Background(), r)
	for i := 0; i < 10; i++ {
		f.run(t, f.now.Add(time.Duration(i)*30*time.Second))
	}
	if f.sms.Count() != 1 || f.state(t, id) != "sent" {
		t.Fatalf("exactly one SMS expected: count=%d state=%s", f.sms.Count(), f.state(t, id))
	}
}

func TestOccurrenceDueReceiver(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	f.gate.set(u, "P-REMIND", true)
	rec, eid := f.event(t, u, "medicine", f.now)
	if rec.Code != http.StatusAccepted {
		t.Fatal(rec.Code)
	}
	// Replay of the same event: acknowledged, no second dispatch.
	body := fmt.Sprintf(`{"occurrenceId":%q,"activityExternalRef":"act-1","typeKey":"medicine","dueAt":%q,"subjectRef":%q,"eventId":%q}`, ids.New(), f.now.Format(time.RFC3339), u, eid)
	if rec := f.post(t, []byte(body), strconv.FormatInt(f.now.Unix(), 10), eid, ""); rec.Code != http.StatusOK {
		t.Fatalf("replay: %d", rec.Code)
	}
	if f.count(t, `SELECT count(*) FROM notifications.dispatch WHERE recipient_ref=$1`, u) != 1 {
		t.Fatal("replay created a second dispatch")
	}
	// Stale timestamp, wrong signature, tampered body, mismatched header, unknown type.
	b := []byte(body)
	if rec := f.post(t, b, strconv.FormatInt(f.now.Add(-10*time.Minute).Unix(), 10), eid, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale: %d", rec.Code)
	}
	if rec := f.post(t, b, strconv.FormatInt(f.now.Unix(), 10), eid, "v1=deadbeef"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", rec.Code)
	}
	ts := strconv.FormatInt(f.now.Unix(), 10)
	good := notifications.SignOccurrence(secret, ts, b)
	if rec := f.post(t, append(b[:len(b)-1:len(b)-1], []byte(` }`)...), ts, eid, good); rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered: %d", rec.Code)
	}
	if rec := f.post(t, b, ts, ids.New(), ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("event id mismatch: %d", rec.Code)
	}
	if rec, _ := f.event(t, u, "surgery", f.now); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown type: %d", rec.Code)
	}
	// The wire never carries wording: the dispatch row holds references only.
	if f.count(t, `SELECT count(*) FROM notifications.dispatch WHERE vars IS NOT NULL`) != 0 {
		t.Fatal("reminder rows hold no variables")
	}
}

func TestReminderWithoutConsentIsBlocked(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	f.event(t, u, "medicine", f.now)
	f.run(t, f.now.Add(time.Second))
	if len(f.visible()) != 0 || f.count(t, `SELECT count(*) FROM notifications.dispatch WHERE state='blocked'`) != 1 {
		t.Fatal("P-REMIND off: blocked")
	}
}

func TestWhatsAppIsRefused(t *testing.T) {
	f := setup(t)
	reg, _ := sms.ParseRegistry([]byte(registryYAML))
	cfg := config(reg)
	cfg.ChannelsEnabled["whatsapp"] = true
	if _, err := notifications.New(f.env.Pools.For("notifications"), cfg, notifications.Deps{Gate: f.gate, Contacts: f.phones, Push: f.tg, Catalog: cat{}}, slog.Default()); err == nil {
		t.Fatal("enabling WhatsApp must fail")
	}
	cfg = config(reg)
	cfg.Templates = append(cfg.Templates, notifications.Template{Key: "wa.x", MessageType: "booking", Category: public.Service, Priority: public.Normal,
		Channels: []notifications.Channel{notifications.WhatsApp}, TitleKey: "notif.title", BodyKey: "notif.title"})
	if _, err := notifications.New(f.env.Pools.For("notifications"), cfg, notifications.Deps{Gate: f.gate, Contacts: f.phones, Push: f.tg, Catalog: cat{}}, slog.Default()); err == nil {
		t.Fatal("a template on WhatsApp must fail")
	}
	u := f.user(true)
	err := f.m.SetPreferences(context.Background(), u, notifications.Preferences{Channels: map[string][]notifications.Channel{"booking": {notifications.WhatsApp}},
		QuietHoursEnabled: true, TimeZone: "Asia/Kolkata", Locale: "en"})
	if err != notifications.ErrInvalidPreferences {
		t.Fatal(err)
	}
}

func TestSendIsIdempotent_AndDeliveryLogHoldsNoContent(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	id1, _ := f.m.Send(context.Background(), booking(u, "same"))
	id2, err := f.m.Send(context.Background(), booking(u, "same"))
	if err != nil || id1 != id2 {
		t.Fatalf("%s %s %v", id1, id2, err)
	}
	f.run(t, f.now.Add(time.Second))
	f.run(t, f.now.Add(2*time.Second))
	if len(f.visible()) != 1 {
		t.Fatal("sent twice")
	}
	if f.count(t, `SELECT count(*) FROM notifications.delivery_log WHERE row_to_json(delivery_log)::text ILIKE '%Rao%' OR row_to_json(delivery_log)::text ILIKE '%appointment%'`) != 0 {
		t.Fatal("delivery log must not hold variables or text")
	}
	if f.count(t, `SELECT count(*) FROM notifications.dispatch WHERE id=$1 AND vars IS NULL`, id1) != 1 {
		t.Fatal("variables kept after final state")
	}
	// Invalid variable types and unknown variables are refused.
	bad := booking(u, "bad")
	bad.Vars[1].Value = "https://example.test"
	if _, err := f.m.Send(context.Background(), bad); err == nil {
		t.Fatal("link in a variable")
	}
}

func TestPurgeDeliveryLogsAfter90Days(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	id, _ := f.m.Send(context.Background(), booking(u, "p1"))
	f.run(t, f.now.Add(time.Second))
	_, _ = f.env.Owner.Exec(context.Background(), `UPDATE notifications.delivery_log SET at = $1`, f.now.Add(-91*24*time.Hour))
	_, _ = f.env.Owner.Exec(context.Background(), `UPDATE notifications.dispatch SET updated_at = $1`, f.now.Add(-91*24*time.Hour))
	n, err := f.m.PurgeDeliveryLogs(context.Background(), f.now)
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if f.count(t, `SELECT count(*) FROM notifications.dispatch WHERE id=$1`, id) != 0 {
		t.Fatal("finished dispatch purged")
	}
}

func TestPreferencesAndEmailRules(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	p, err := f.m.Preferences(context.Background(), u)
	if err != nil || p.TimeZone != "Asia/Kolkata" || !p.QuietHoursEnabled {
		t.Fatalf("defaults %+v %v", p, err)
	}
	bad := []notifications.Preferences{
		{TimeZone: "Not/AZone", Locale: "en"},
		{TimeZone: "Asia/Kolkata", Locale: "xx"},
		{TimeZone: "Asia/Kolkata", Locale: "en", Channels: map[string][]notifications.Channel{"booking": {notifications.Email}}},
		{TimeZone: "Asia/Kolkata", Locale: "en", Channels: map[string][]notifications.Channel{"unknown": {notifications.Push}}},
	}
	for i, b := range bad {
		if err := f.m.SetPreferences(context.Background(), u, b); err == nil {
			t.Fatalf("case %d must fail", i)
		}
	}
	ok := notifications.Preferences{TimeZone: "UTC", Locale: "hi", Discreet: true, Channels: map[string][]notifications.Channel{"receipt": {notifications.Email}}}
	if err := f.m.SetPreferences(context.Background(), u, ok); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.m.Preferences(context.Background(), u); got.TimeZone != "UTC" || !got.Discreet || got.Channels["receipt"][0] != notifications.Email {
		t.Fatalf("%+v", got)
	}
	// Email is allowed for receipts only; the template carries an opaque reference.
	f2 := notifications.Template{Key: "bad.mail", MessageType: "booking", Category: public.Service, Priority: public.Normal, Channels: []notifications.Channel{notifications.Email},
		TitleKey: "notif.title", BodyKey: "notif.title", Email: &notifications.EmailBinding{TemplateKey: "x"}}
	reg, _ := sms.ParseRegistry([]byte(registryYAML))
	cfg := config(reg)
	cfg.Templates = append(cfg.Templates, f2)
	if _, err := notifications.New(f.env.Pools.For("notifications"), cfg, notifications.Deps{Gate: f.gate, Contacts: f.phones, Push: f.tg, Catalog: cat{}}, slog.Default()); err == nil {
		t.Fatal("email for a booking template must be refused")
	}
}

func TestSignOccurrenceMatchesSchedulerAlgorithm(t *testing.T) {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("1700000000.{}"))
	want := "v1=" + hex.EncodeToString(mac.Sum(nil))
	if got := notifications.SignOccurrence(secret, "1700000000", []byte("{}")); got != want {
		t.Fatal(got)
	}
}

// Review M2 (a): a caller cannot raise a priority above the template's.
func TestReviewM2CallerCannotRaisePriority(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	r := booking(u, "p1")
	r.Priority = public.Critical
	if _, err := f.m.Send(context.Background(), r); err != public.ErrInvalid {
		t.Fatalf("raising to critical must be refused: %v", err)
	}
	// Promotional is always normal, even if the caller asks otherwise (and consent is given).
	f.gate.set(u, "P-NEWS", true)
	id, err := f.m.Send(context.Background(), public.Request{TemplateKey: "promo.news", RecipientRef: u, Category: public.Promotional, Reason: "news.feature", DedupeKey: "pp1", Priority: public.Critical})
	if err == nil {
		var prio string
		if err := f.env.Owner.QueryRow(context.Background(), `SELECT priority FROM notifications.dispatch WHERE id=$1`, id).Scan(&prio); err != nil || prio != "normal" {
			t.Fatalf("priority %q %v", prio, err)
		}
	} else if err != public.ErrInvalid {
		t.Fatal(err)
	}
}

// Review M2 (c): promotional messages respect quiet hours even if the patient turned them off.
func TestReviewM2PromotionalAlwaysRespectsQuietHours(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	f.gate.set(u, "P-NEWS", true)
	if err := f.m.SetPreferences(context.Background(), u, notifications.Preferences{QuietHoursEnabled: false, TimeZone: "Asia/Kolkata", Locale: "en"}); err != nil {
		t.Fatal(err)
	}
	night := time.Date(2026, 11, 17, 16, 30, 0, 0, time.UTC) // 22:00 in the zone
	f.now = night
	id, err := f.m.Send(context.Background(), public.Request{TemplateKey: "promo.news", RecipientRef: u, Category: public.Promotional, Reason: "news.feature", DedupeKey: "pq1"})
	if err != nil {
		t.Fatal(err)
	}
	f.run(t, night)
	if len(f.visible()) != 0 || f.state(t, id) != "queued" {
		t.Fatal("promotional must be held at night")
	}
}

// Review M2 (b): a reminder held through quiet hours and released more than MaxLateness
// after its due time is expired and logged, never sent.
func TestReviewM2StaleReminderExpiresInsteadOfSending(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	f.gate.set(u, "P-REMIND", true)
	night := time.Date(2026, 11, 17, 16, 30, 0, 0, time.UTC)
	f.now = night
	f.event(t, u, "followup", night) // not quiet-exempt: held
	f.run(t, night.Add(time.Second))
	f.run(t, time.Date(2026, 11, 18, 2, 31, 0, 0, time.UTC)) // quiet hours over, 10 h late
	if len(f.visible()) != 0 || f.sms.Count() != 0 {
		t.Fatal("a stale reminder must not be sent")
	}
	if n := f.count(t, `SELECT count(*) FROM notifications.dispatch WHERE state='expired'`); n != 1 {
		t.Fatalf("expired rows: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM notifications.delivery_log WHERE outcome='expired' AND reason='stale'`); n != 1 {
		t.Fatalf("expired log rows: %d", n)
	}
	// A configured, shorter lateness is honoured; within it the reminder is still sent.
	reg, _ := sms.ParseRegistry([]byte(registryYAML))
	cfg := config(reg)
	cfg.MaxLateness = 15 * time.Minute
	f.build(t, cfg)
	due := time.Date(2026, 11, 18, 6, 30, 0, 0, time.UTC)
	f.now = due
	f.event(t, u, "followup", due)
	f.run(t, due.Add(10*time.Minute))
	if len(f.visible()) != 1 {
		t.Fatalf("within the lateness window: %d", len(f.visible()))
	}
}

// S-157: a time variable is an instant; each recipient sees it in their own time zone, in
// push and SMS alike, and a malformed time is refused when the message is queued.
func TestS157TimeVariablesUseTheRecipientZone(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	away, home := f.user(true), f.user(false)
	if err := f.m.SetPreferences(ctx, away, notifications.Preferences{Channels: map[string][]notifications.Channel{}, QuietHoursEnabled: true,
		TimeZone: "Etc/GMT-3", Locale: "en"}); err != nil {
		t.Fatal(err)
	}
	at := "2026-11-20T10:00:00Z"
	for _, u := range []string{away, home} {
		if _, err := f.m.Send(ctx, public.Request{TemplateKey: "booking.timed", RecipientRef: u, Category: public.Service, Reason: "booking.test",
			DedupeKey: "timed:" + u, Vars: []public.Var{{Name: "doctor_name", Type: public.Alphanumeric, Value: "Dr Test"},
				{Name: "date_time", Type: public.Time, Value: at}}}); err != nil {
			t.Fatal(err)
		}
	}
	f.run(t, f.now)
	var body string
	for _, p := range f.visible() {
		if p.Target.Token == "tok-"+away {
			body = p.Body
		}
	}
	if !strings.Contains(body, "Fri 20 Nov, 1:00 PM") {
		t.Fatalf("push in the recipient's zone (UTC+3): %q", body)
	}
	sent := f.sms.Sent(f.phones.phones[home])
	if len(sent) != 1 || sent[0].Vars[1].Value != "Fri 20 Nov, 3:30 PM" {
		t.Fatalf("SMS in the default zone: %+v", sent)
	}
	if _, err := f.m.Send(ctx, public.Request{TemplateKey: "booking.timed", RecipientRef: home, Category: public.Service, Reason: "booking.test",
		DedupeKey: "timed:bad", Vars: []public.Var{{Name: "doctor_name", Type: public.Alphanumeric, Value: "Dr Test"},
			{Name: "date_time", Type: public.Time, Value: "tomorrow at ten"}}}); !errors.Is(err, public.ErrInvalid) {
		t.Fatalf("a malformed time must be refused: %v", err)
	}
}

// visible returns the push notifications a person sees: silent refresh hints (S-165) carry no content and are
// checked by their own tests.
func (f *fx) visible() []push.Message {
	var out []push.Message
	for _, m := range f.push.Sent() {
		if !m.Silent {
			out = append(out, m)
		}
	}
	return out
}
func (t *targets) RevokedPushTarget(_ context.Context, u, d string) (public.PushTarget, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tg, ok := t.revoked[u+"|"+d]
	return tg, ok, nil
}

// TM-27: links, bare domains, short links and markup in a variable are refused at Send and nothing is sent.
func TestTM27LinkAndMarkupVariablesRefusedAndNothingSent(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	for i, p := range []string{"http://x", "https://x.y", "bit.ly/abc", "evil.org", "t.me/abc", "a.io", "xn--p1ai", "<script>", "{x}", "Dr a@b", "www.x"} {
		for _, idx := range []int{0, 1} { // doctor_name and date_time
			r := booking(u, fmt.Sprintf("tm27-%d-%d", i, idx))
			r.Vars[idx].Value = p
			if _, err := f.m.Send(context.Background(), r); err == nil {
				t.Errorf("var %d %q accepted", idx, p)
			}
		}
	}
	f.run(t, f.now.Add(time.Second))
	if len(f.visible()) != 0 || len(f.sms.Sent(f.phones.phones[u])) != 0 {
		t.Fatal("a refused message was sent")
	}
	// Ordinary names still go through.
	if _, err := f.m.Send(context.Background(), booking(u, "tm27-ok")); err != nil {
		t.Fatal(err)
	}
}

// TM-41: a "{other}" placeholder smuggled into a stored variable is refused at render time (leftover-brace
// check), never substituted into the text.
func TestTM41BracePayloadInStoredVariableRefusedAtRender(t *testing.T) {
	f := setup(t)
	u := f.user(true)
	id, err := f.m.Send(context.Background(), booking(u, "tm41"))
	if err != nil {
		t.Fatal(err)
	}
	vars := fmt.Sprintf(`[{"Name":"doctor_name","Type":"%s","Value":"{date_time}"},{"Name":"date_time","Type":"%s","Value":"Tue 17 Nov"},{"Name":"booking_id","Type":"%s","Value":"%s"}]`,
		public.Alphanumeric, public.Alphanumeric, public.Ref, ids.New())
	if _, err := f.env.Owner.Exec(context.Background(), `UPDATE notifications.dispatch SET vars=$2::jsonb WHERE id=$1`, id, vars); err != nil {
		t.Fatal(err)
	}
	f.run(t, f.now.Add(time.Second))
	for _, m := range f.visible() {
		if strings.Contains(m.Body, "{") || strings.Contains(m.Body, "Tue 17 Nov") {
			t.Fatalf("payload rendered: %q", m.Body)
		}
	}
	if len(f.sms.Sent(f.phones.phones[u])) != 0 {
		t.Fatal("sms sent with a placeholder payload")
	}
}

// TM-27: a resolver-supplied medicine label containing a link is not used in a notification.
func TestTM27ResolverLabelWithLinkIsDropped(t *testing.T) {
	f := setup(t)
	u := f.user(false)
	f.gate.set(u, "P-REMIND", true)
	f.gate.set(u, "P-MEDNAMES", true)
	f.res.named = "Pay at bit.ly/x"
	f.event(t, u, "medicine", f.now)
	f.run(t, f.now.Add(time.Second))
	for _, m := range f.sms.Sent(f.phones.phones[u]) {
		for _, v := range m.Vars {
			if strings.Contains(v.Value, "bit.ly") {
				t.Fatalf("link reached an SMS variable: %+v", m)
			}
		}
	}
}
