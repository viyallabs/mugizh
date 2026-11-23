package app

import (
	"context"
	"errors"
	"sync"

	"go.yaml.in/yaml/v3"

	esignfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/esign/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore"
	schedfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/schedulercore/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical/rules"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/config"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications"
	np "gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications/public"
)

// rulesFile is the prescribing rules file inside the country pack.
const rulesFile = "rules/telemedicine-rules.yaml"

// loadRules loads the prescribing rules with the pack's checksum (TC-59); production
// refuses a draft rule file (K-CS-07: approved by the doctor reviewer first).
func loadRules(c *Core) (*rules.Engine, error) {
	pack := c.Config.Pack()
	sum := pack.RuleSetVersions().FileSHA256[rulesFile]
	if sum == "" {
		return nil, errors.New("clinical: no checksum for the prescribing rules in the pack")
	}
	return rules.Load(pack.Clinical().PrescribingRules, rules.Options{RequireApproved: c.Settings.Env == config.EnvProduction, ExpectedSHA256: sum})
}

// lateResolver breaks the construction cycle: notifications needs clinical's reminder
// resolver, and clinical needs notifications to tell patients. It fails closed until set.
type lateResolver struct {
	mu sync.RWMutex
	r  np.ReminderContentResolver
}

func (l *lateResolver) set(r np.ReminderContentResolver) { l.mu.Lock(); l.r = r; l.mu.Unlock() }

func (l *lateResolver) Resolve(ctx context.Context, occurrenceRef, externalRef, typeKey, recipientRef string, allowNames bool) (string, string, error) {
	l.mu.RLock()
	r := l.r
	l.mu.RUnlock()
	if r == nil {
		return "", "", errors.New("reminder resolver not ready")
	}
	return r.Resolve(ctx, occurrenceRef, externalRef, typeKey, recipientRef, allowNames)
}

// buildClinical constructs the rules engine, imports the pack's drug classification and
// builds the prescriptions service.
func buildClinical(ctx context.Context, c *Core, validators []string) error {
	engine, err := loadRules(c)
	if err != nil {
		return err
	}
	if c.Clinical, err = clinical.New(c.Pools.For("clinical"), engine); err != nil {
		return err
	}
	imp, err := c.Clinical.ImportClassification(ctx, clinical.Actor{Ref: "00000000-0000-7000-8000-000000000001", Role: "system"},
		c.Config.Pack().Clinical().DrugClassification)
	if err != nil {
		return err
	}
	// Production prescribes only from a drug classification the doctor reviewer approved
	// (K-CS-07, review H4); the rules file is already checked by loadRules.
	if c.Settings.Env == config.EnvProduction {
		var meta struct {
			Status string `yaml:"status"`
		}
		if err := yaml.Unmarshal(c.Config.Pack().Clinical().DrugClassification, &meta); err != nil || meta.Status != "approved" {
			return errors.New("clinical: the drug classification is not approved; production refuses to start")
		}
	}
	cfg := clinical.DefaultConfig()
	// P09 fails closed: signing is possible only when the pack names an e-signature vendor (N5).
	cfg.ESignEnabled = c.Config.Pack().Adapter("esign") != "none" && c.Config.Pack().Adapter("esign") != ""
	cfg.ProhibitedValidators = validators
	cfg.ClassificationVersion = imp.Version
	c.Rx, err = clinical.NewRx(c.Clinical, cfg, clinical.Deps{Crypto: c.Crypto, Gate: c.Records, Granter: c.Records, Verifier: c.Verification,
		Consults: consultFacts{booking: c.Booking, sessions: c.Consult, profiles: c.Identity, changes: c.Identity, prescribers: c.Verification, practices: c.Directory}, Signer: &esignfake.Signer{}, Scheduler: c.Scheduler, Notifier: c.Notify, Log: c.Log, Now: c.Settings.Now})
	return err
}

func prescriptionTemplates() []notifications.Template {
	push, smsCh := notifications.Push, notifications.SMS
	return []notifications.Template{
		{Key: "prescription.ready", MessageType: "prescription", Category: np.Service, Priority: np.Normal,
			Channels: []notifications.Channel{push, smsCh}, TitleKey: "notif.title", BodyKey: "notif.prescription.ready", LinkScreen: "prescriptions",
			SMS: &notifications.SMSBinding{TemplateID: "SMS-09"}},
		{Key: "prescription.amended", MessageType: "prescription", Category: np.Service, Priority: np.Normal,
			Channels: []notifications.Channel{push}, TitleKey: "notif.title", BodyKey: "notif.prescription.amended", LinkScreen: "prescriptions"},
	}
}

// schedulerClient is the scheduler-core client shared by clinical (reminder activities) and
// notifications (the patient's reminder calendar and dose actions, S-153/S-154). Without a
// scheduler URL the in-process fake is used (local, test and dev only; BuildCore refuses others).
type schedulerClient interface {
	schedulercore.Client
	schedulercore.Occurrences
}

func newSchedulerClient(s Settings) (schedulerClient, error) {
	if s.SchedulerURL == "" {
		return &schedfake.Client{Now: s.Now}, nil // the end-to-end test clock, when set
	}
	return schedulercore.NewHTTP(s.SchedulerURL, s.SchedulerToken)
}
