package app

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"go.yaml.in/yaml/v3"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/identity"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications"
	np "gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
)

//go:embed i18n/*.yaml
var i18nFiles embed.FS

// catalog is the server-side translation catalog (K-GLB-02).
type catalog map[string]map[string]string // locale → key → text

func loadCatalog(files fs.FS, locales []string) (catalog, error) {
	if files == nil {
		files = i18nFiles
	}
	c := catalog{}
	for _, l := range locales {
		b, err := fs.ReadFile(files, "i18n/"+l+".yaml")
		if err != nil {
			continue // a supported locale without server text falls back to the default
		}
		m := map[string]string{}
		if err := yaml.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("i18n %s: %w", l, err)
		}
		c[l] = m
	}
	if len(c) == 0 {
		return nil, errors.New("i18n: no notification catalog for any supported locale")
	}
	return c, nil
}

// Text implements notifications.Catalog.
func (c catalog) Text(locale, key string) (string, bool) {
	s, ok := c[locale][key]
	return s, ok
}

// pushTargets adapts identity's push targets to the notifications port (B4: notifications
// may not import identity's root package).
type pushTargets struct{ id *identity.Module }

func (p pushTargets) PushTargets(ctx context.Context, userID, app string) ([]np.PushTarget, error) {
	ts, err := p.id.PushTargets(ctx, userID, app)
	if err != nil {
		return nil, err
	}
	out := make([]np.PushTarget, 0, len(ts))
	for _, t := range ts {
		out = append(out, np.PushTarget{DeviceID: t.DeviceID, Platform: t.Platform, Token: t.Token})
	}
	return out, nil
}

func (p pushTargets) InvalidatePushToken(ctx context.Context, deviceID string) error {
	return p.id.InvalidatePushToken(ctx, deviceID)
}

func (p pushTargets) RevokedPushTarget(ctx context.Context, userID, deviceID string) (np.PushTarget, bool, error) {
	t, ok, err := p.id.RevokedPushTarget(ctx, userID, deviceID)
	return np.PushTarget{DeviceID: t.DeviceID, Platform: t.Platform, Token: t.Token}, ok, err
}

// contacts adapts identity's phone lookup: an unknown user means "no contact".
type contacts struct{ id *identity.Module }

func (c contacts) PhoneFor(ctx context.Context, userID string) (string, error) {
	p, err := c.id.PhoneFor(ctx, userID)
	if errors.Is(err, identity.ErrNotFound) {
		return "", nil
	}
	return p, err
}

// notificationTemplates are the reminder templates. Reminder wording is resolved at send time
// through clinical's resolver; booking, consult and refund messages are in bookingmsg.go.
func notificationTemplates() []notifications.Template {
	push, smsCh := notifications.Push, notifications.SMS
	return []notifications.Template{
		{Key: "reminder.medicine", MessageType: "reminder", Category: np.Service, Priority: np.Critical, Reminder: true, QuietExempt: true,
			Channels: []notifications.Channel{push, smsCh}, TitleKey: "notif.title", BodyKey: "notif.reminder.body", LinkScreen: "reminder",
			SMS:      &notifications.SMSBinding{TemplateID: "SMS-10", Vars: []notifications.SMSVar{{Name: "time_of_day", Source: "time_of_day"}}},
			SMSNamed: &notifications.SMSBinding{TemplateID: "SMS-10-N", Vars: []notifications.SMSVar{{Name: "medicine_name", Source: "named"}}}},
		{Key: "reminder.followup", MessageType: "reminder_followup", Category: np.Service, Priority: np.Normal, Reminder: true,
			Channels: []notifications.Channel{push, smsCh}, TitleKey: "notif.title", BodyKey: "notif.followup.body", DiscreetBodyKey: "notif.followup.discreet.body",
			LinkScreen: "reminder", SMS: &notifications.SMSBinding{TemplateID: "SMS-11", Vars: []notifications.SMSVar{{Name: "date_time", Source: "due_at"}}}},
		{Key: "reminder.grouped", MessageType: "reminder", Category: np.Service, Priority: np.Critical, Reminder: true,
			Channels: []notifications.Channel{push}, TitleKey: "notif.title", BodyKey: "notif.reminders.grouped", LinkScreen: "reminders"},
	}
}

// buildNotifications constructs the module from the country pack.
func buildNotifications(c *Core, reg *sms.Registry, files fs.FS) (*notifications.Module, error) {
	pack := c.Config.Pack()
	loc := pack.Locale()
	msg := pack.Messaging()
	cat, err := loadCatalog(files, append([]string{loc.DefaultLocale}, loc.SupportedLocales...))
	if err != nil {
		return nil, err
	}
	cfg := notifications.Config{
		Templates: append(append(append(append(notificationTemplates(), rightsTemplates()...), prescriptionTemplates()...), bookingMessageTemplates()...), shareMessageTemplates()...), GroupedTemplate: "reminder.grouped",
		// scheduler-core typeKeys (coarse categories only, K-ARC-02) → reminder templates.
		ReminderTemplates: map[string]string{"med.dose": "reminder.medicine", "injection.dose": "reminder.medicine", "test.due": "reminder.followup"},
		ChannelsEnabled:   msg.ChannelsEnabled, QuietStart: msg.QuietHours.Start, QuietEnd: msg.QuietHours.End,
		DefaultTimeZone: loc.DefaultTimeZone, DefaultLocale: loc.DefaultLocale, SupportedLocales: loc.SupportedLocales,
		DateTimeLayout: "2 Jan, 3:04 PM", Denylist: notificationDenylist, SMSRegistry: reg,
		LogRetention: 90 * 24 * time.Hour, RetryBudget: 5 * time.Minute, ReceiptWait: 5 * time.Minute, GroupHold: 3 * time.Second,
		BackoffBase: 10 * time.Second, BackoffCap: time.Minute, Lease: 30 * time.Second,
	}
	return notifications.New(c.Pools.For("notifications"), cfg, notifications.Deps{Gate: c.Records, Contacts: contacts{c.Identity},
		Push: pushTargets{c.Identity}, SMS: c.SMS, PushSend: c.PushSender, Email: c.EmailSender, Catalog: cat, Resolver: c.resolver, Occurrences: c.Scheduler}, c.Log)
}

// notificationDenylist is defence in depth: rendered text containing any of these words is
// refused (K-DP-09). The primary control is that templates never carry such data. [VALIDATE
// the list with the doctor reviewer.]
var notificationDenylist = []string{
	"cardiology", "cardiologist", "oncology", "oncologist", "psychiatry", "psychiatrist", "gynaecology", "gynecologist",
	"dermatology", "urology", "neurology", "hiv", "cancer", "diabetes", "depression", "pregnancy", "std", "tuberculosis",
}
