// Package notifications is the notifications module (S-141, S-166): templates, preferences,
// quiet hours, dispatch with retry and fallback, and the delivery log.
//
// Privacy rules (K-DP-09) hold at send time, not at scheduling time: consent is checked and
// reminder wording is resolved again on every attempt. SMS goes only through registered
// templates (K-MSG-01). WhatsApp is on hold: the channel exists in the enum but nothing sends
// on it and New refuses a configuration that enables it (K-MSG-02).
package notifications

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones must resolve in minimal containers (K-ARC-04)

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/email"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/push"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/records/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Source returns the module's migrations.
func Source() migrate.Source {
	sub, _ := fs.Sub(migrations, "migrations")
	return migrate.Source{Module: "notifications", Schema: "notifications", FS: sub}
}

// Channel a message can travel on.
type Channel string

// Channels. WhatsApp is on hold and always refused.
const (
	Push     Channel = "push"
	SMS      Channel = "sms"
	Email    Channel = "email"
	WhatsApp Channel = "whatsapp"
)

// Consent purposes checked at send time (consent-texts.md).
const (
	PurposeRemind   = "P-REMIND"
	PurposeMedNames = "P-MEDNAMES"
	PurposeNews     = "P-NEWS"
)

// Dispatch states (module-boundaries §2).
const (
	stQueued     = "queued"
	stSent       = "sent"
	stDelivered  = "delivered"
	stFailed     = "failed"
	stFallback   = "fallback"
	stDeadLetter = "dead_letter"
	stBlocked    = "blocked"
	stExpired    = "expired"
)

// emailTypes are the only message types that may use email (adapter-interfaces §8).
var emailTypes = map[string]bool{"receipt": true, "invoice": true, "staff_alert": true}

// VarSpec declares a template variable.
type VarSpec struct {
	Name string
	Type public.VarType
}

// SMSVar says where an SMS variable's value comes from. Source is "var:<name>" (a request
// variable), "time_of_day" (a word from the catalog), "due_at" (formatted due time) or "named"
// (the resolver's named text; only in a named binding).
type SMSVar struct{ Name, Source string }

// SMSBinding maps a template to a registered SMS template.
type SMSBinding struct {
	TemplateID string
	Vars       []SMSVar
}

// EmailBinding maps a template to an email template; Vars are request variable names.
type EmailBinding struct {
	TemplateKey string
	Vars        []string
}

// Template is one message definition. All user-visible text lives in the catalog under the
// keys below (K-GLB-02); no wording is written in Go.
type Template struct {
	Key         string
	MessageType string // preference key: booking, reminder, prescription, refund, receipt, ...
	Category    public.Category
	Priority    public.Priority // default priority
	Reminder    bool            // reminder templates are sent only by the occurrence.due receiver
	Channels    []Channel       // default order, e.g. push then sms
	QuietExempt bool            // exempt from quiet hours (consult starting, 2 h reminder, security alert)
	Vars        []VarSpec

	TitleKey        string // push title translation key
	BodyKey         string // push body, with {name} placeholders for variables
	DiscreetBodyKey string // used instead of BodyKey in discreet mode, when set

	LinkScreen string // deep-link screen
	LinkRefVar string // name of the Ref variable carrying the opaque ID (messages only)

	SMS      *SMSBinding
	SMSNamed *SMSBinding // reminder with the medicine name; only with P-MEDNAMES
	Email    *EmailBinding
	// Audience is whose app gets the push: AudiencePatient (the default when empty), AudiencePro or AudienceAny
	// (account messages such as data rights). A patient message never reaches a pro-app device of the same person
	// (a reminder may name a medicine, K-DP-09), and a professional message never reaches the patient app (D-73).
	Audience string
}

// Audiences of a template.
const (
	AudiencePatient = "patient"
	AudiencePro     = "pro"
	AudienceAny     = "any"
)

// pushApp is the app whose devices get a push for the template ("" = every app).
func (t Template) pushApp() string {
	switch t.Audience {
	case AudiencePro:
		return AudiencePro
	case AudienceAny:
		return ""
	}
	return AudiencePatient
}

// Catalog returns translated text for a key. Missing keys return ok=false.
type Catalog interface {
	Text(locale, key string) (string, bool)
}

// ContactResolver returns a user's phone number (identity implements it:
// *identity.Module.PhoneFor). An empty number with nil error means no contact.
type ContactResolver interface {
	PhoneFor(ctx context.Context, userID string) (string, error)
}

// EmailResolver returns a user's email address; optional (nil disables the email channel).
type EmailResolver interface {
	EmailFor(ctx context.Context, userID string) (string, error)
}

// PushTargets lists a user's push devices. Tokens live in identity (data inventory 4.1);
// notifications never stores them.
type PushTargets interface {
	// PushTargets lists the user's active devices of one app ("patient" or "pro"; "" = every app).
	PushTargets(ctx context.Context, userID, app string) ([]public.PushTarget, error)
	InvalidatePushToken(ctx context.Context, deviceID string) error
	// RevokedPushTarget is a device of the user signed out remotely in the last 24 hours (S-165).
	RevokedPushTarget(ctx context.Context, userID, deviceID string) (public.PushTarget, bool, error)
}

// Deps are the collaborators.
type Deps struct {
	Gate     rp.Gate
	Resolver public.ReminderContentResolver
	Contacts ContactResolver
	Emails   EmailResolver
	Push     PushTargets
	SMS      sms.Sender
	PushSend push.Sender
	Email    email.Sender
	Catalog  Catalog
	// Occurrences reads and acts on the reminder occurrences held by scheduler-core (S-153, S-154). Optional:
	// without it the reminder API answers 503.
	Occurrences schedulercore.Occurrences
}

// Config holds values from the country pack and decisions.
type Config struct {
	Templates         []Template
	GroupedTemplate   string            // template key for merged reminders
	ReminderTemplates map[string]string // scheduler typeKey -> reminder template key
	ChannelsEnabled   map[string]bool   // pack messaging.channels_enabled
	QuietStart        string            // HH:MM, pack messaging.quiet_hours
	QuietEnd          string
	DefaultTimeZone   string // pack locale.default_time_zone
	DefaultLocale     string
	SupportedLocales  []string
	DateTimeLayout    string // Go layout for the due_at SMS variable
	Denylist          []string
	SMSRegistry       *sms.Registry

	LogRetention time.Duration // RET-10 (90 days)
	RetryBudget  time.Duration // per channel, 5 minutes (Performance sheet)
	ReceiptWait  time.Duration // push without an app receipt falls back after this
	GroupHold    time.Duration // reminders wait this long so same-minute ones merge
	BackoffBase  time.Duration
	BackoffCap   time.Duration
	Lease        time.Duration
	// MaxLateness is how long after its due time a reminder may still be sent (scheduler-core rule
	// D-28). Later than that it is marked expired and logged, never sent. Default 1 hour.
	MaxLateness time.Duration
}

// Module is the notifications module.
type Module struct {
	pool  *pgxpool.Pool
	cfg   Config
	deps  Deps
	log   *slog.Logger
	now   func() time.Time
	rnd   func() float64 // jitter in [0,1)
	tmpl  map[string]Template
	deny  []*regexp.Regexp
	qS    int
	qE    int
	types map[string]bool
	// Alert is called when a dispatch is dead-lettered (alert hook).
	Alert func(ctx context.Context, dispatchID, reason string)
}

var reHHMM = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)

func parseHHMM(s string) (int, error) {
	m := reHHMM.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("notifications: bad HH:MM %q", s)
	}
	var h, mi int
	_, _ = fmt.Sscanf(m[1]+" "+m[2], "%d %d", &h, &mi)
	return h*60 + mi, nil
}

// New builds the module. pool must use the notifications role.
func New(pool *pgxpool.Pool, cfg Config, deps Deps, log *slog.Logger) (*Module, error) {
	if cfg.ChannelsEnabled[string(WhatsApp)] {
		return nil, errors.New("notifications: WhatsApp is on hold and cannot be enabled (K-MSG-02)")
	}
	if cfg.SMSRegistry == nil || deps.Gate == nil || deps.Catalog == nil || deps.Contacts == nil || deps.Push == nil {
		return nil, errors.New("notifications: registry, gate, catalog, contacts and push targets are required")
	}
	if cfg.DefaultTimeZone == "" || cfg.DefaultLocale == "" || cfg.DateTimeLayout == "" {
		return nil, errors.New("notifications: default time zone, locale and date layout come from the country pack")
	}
	if _, err := time.LoadLocation(cfg.DefaultTimeZone); err != nil {
		return nil, fmt.Errorf("notifications: default time zone: %w", err)
	}
	qs, err := parseHHMM(cfg.QuietStart)
	if err != nil {
		return nil, err
	}
	qe, err := parseHHMM(cfg.QuietEnd)
	if err != nil {
		return nil, err
	}
	if cfg.RetryBudget <= 0 || cfg.BackoffBase <= 0 || cfg.BackoffCap <= 0 || cfg.Lease <= 0 || cfg.LogRetention <= 0 || cfg.ReceiptWait <= 0 {
		return nil, errors.New("notifications: timing configuration is required")
	}
	if cfg.MaxLateness <= 0 {
		cfg.MaxLateness = time.Hour
	}
	m := &Module{pool: pool, cfg: cfg, deps: deps, log: log, now: time.Now, rnd: randFloat, qS: qs, qE: qe,
		tmpl: map[string]Template{}, types: map[string]bool{}}
	for _, t := range cfg.Templates {
		if err := m.checkTemplate(t); err != nil {
			return nil, err
		}
		m.tmpl[t.Key] = t
		m.types[t.MessageType] = true
	}
	if _, ok := m.tmpl[cfg.GroupedTemplate]; !ok {
		return nil, errors.New("notifications: grouped reminder template is not defined")
	}
	for typ, key := range cfg.ReminderTemplates {
		if t, ok := m.tmpl[key]; !ok || !t.Reminder {
			return nil, fmt.Errorf("notifications: reminder type %q needs a reminder template", typ)
		}
	}
	for _, d := range cfg.Denylist {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		m.deny = append(m.deny, regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}])`+regexp.QuoteMeta(d)+`($|[^\p{L}\p{N}])`))
	}
	return m, nil
}

// SetClock replaces the clock (tests).
func (m *Module) SetClock(f func() time.Time) { m.now = f }

// SetJitter replaces the backoff jitter source (tests).
func (m *Module) SetJitter(f func() float64) { m.rnd = f }

var reKey = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,80}$`)

func (m *Module) checkTemplate(t Template) error {
	if !reKey.MatchString(t.Key) || !reKey.MatchString(t.TitleKey) || !reKey.MatchString(t.BodyKey) || t.MessageType == "" {
		return fmt.Errorf("notifications: template %q: keys are required", t.Key)
	}
	switch t.Audience {
	case "", AudiencePatient, AudiencePro, AudienceAny:
	default:
		return fmt.Errorf("notifications: template %s: audience %q", t.Key, t.Audience)
	}
	if t.Reminder && t.pushApp() != AudiencePatient {
		return fmt.Errorf("notifications: template %s: reminders go to the patient app only", t.Key)
	}
	if t.Category != public.Service && t.Category != public.Promotional {
		return fmt.Errorf("notifications: template %s: category", t.Key)
	}
	if t.Priority != public.Critical && t.Priority != public.Normal {
		return fmt.Errorf("notifications: template %s: priority", t.Key)
	}
	if t.Category == public.Promotional && (t.Priority != public.Normal || t.QuietExempt) {
		return fmt.Errorf("notifications: template %s: promotional messages are never critical or quiet-hours exempt", t.Key)
	}
	for _, c := range t.Channels {
		switch c {
		case Push, SMS, Email:
		default:
			return fmt.Errorf("notifications: template %s: channel %q is not available", t.Key, c)
		}
		if c == Email && !emailTypes[t.MessageType] {
			return fmt.Errorf("notifications: template %s: email only for receipts, invoices and staff alerts", t.Key)
		}
	}
	if slices.Contains(t.Channels, SMS) != (t.SMS != nil) || slices.Contains(t.Channels, Email) != (t.Email != nil) {
		return fmt.Errorf("notifications: template %s: channel list and bindings disagree", t.Key)
	}
	if t.SMS != nil {
		cat, ok := m.cfg.SMSRegistry.Category(t.SMS.TemplateID)
		if !ok {
			return fmt.Errorf("notifications: template %s: SMS template %s is not registered", t.Key, t.SMS.TemplateID)
		}
		if (cat == "promotional") != (t.Category == public.Promotional) {
			return fmt.Errorf("notifications: template %s: category disagrees with the SMS registry", t.Key)
		}
	}
	if t.SMSNamed != nil {
		if _, ok := m.cfg.SMSRegistry.Category(t.SMSNamed.TemplateID); !ok || !t.Reminder {
			return fmt.Errorf("notifications: template %s: named SMS binding", t.Key)
		}
	}
	return nil
}

func randFloat() float64 { return rand.Float64() } // #nosec G404 -- backoff jitter, not security
