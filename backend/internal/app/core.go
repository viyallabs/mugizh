package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/email"
	emailfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/email/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/geocoding"
	gfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/geocoding/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/malwarescan"
	msfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/malwarescan/fake"
	paymentsadapter "gitlab.com/viyallabs/mugizh/backend/internal/adapters/payments"
	pfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/payments/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/push"
	pushfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/push/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms"
	smsfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms/fake"
	vfake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/verification/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/video"
	videofake "gitlab.com/viyallabs/mugizh/backend/internal/adapters/video/fake"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/admin"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/audit"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/booking"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/clinical"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/config"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/consult"
	cp "gitlab.com/viyallabs/mugizh/backend/internal/modules/consult/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/directory"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/identity"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/notifications"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/payments"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/refundpolicy"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/records"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/verification"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/blobstore"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/dbschema"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/idempotency"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/keys"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
)

// Settings come from the environment. Secrets are injected by the secrets manager as
// environment variables at start-up; none are in files or the repository (K-SEC-05).
type Settings struct {
	Env            config.Env
	HTTPAddr       string
	PackDir        string
	VendorRegister string
	DBURL          map[string]string // module → DSN of that module's login role
	QueueURL       string
	KeystoreURL    string
	BlindIndexKey  []byte
	LocalRootKey   []byte // local/test/dev only
	BlobDir        string // local/test/dev only
	// SchedulerSecret verifies scheduler-core's signed occurrence.due callbacks (D-29).
	SchedulerSecret []byte
	// SchedulerURL and SchedulerToken reach scheduler-core's API; an empty URL uses the in-memory
	// fake (local, test, dev only).
	SchedulerURL, SchedulerToken string
	TrustedProxies               int
	// Now replaces the clock of every module (end-to-end tests only; nil means the real clock).
	// Never set from the environment.
	Now func() time.Time
	// Catalogs replaces the embedded server text catalogs (tests with a synthetic country pack only; nil means the
	// embedded i18n/*.yaml). Never set from the environment.
	Catalogs fs.FS
}

// LoadSettings reads MUGIZH_* variables.
func LoadSettings(getenv func(string) string) (Settings, error) {
	s := Settings{Env: config.Env(getenv("APP_ENV")), HTTPAddr: getenv("HTTP_ADDR"), PackDir: getenv("MUGIZH_PACK_DIR"),
		VendorRegister: getenv("MUGIZH_VENDOR_REGISTER"), QueueURL: getenv("MUGIZH_DB_URL_QUEUE"), KeystoreURL: getenv("MUGIZH_KEYSTORE_URL"),
		BlobDir: getenv("MUGIZH_BLOB_DIR"), DBURL: map[string]string{}, SchedulerURL: getenv("MUGIZH_SCHEDULER_URL"), SchedulerToken: getenv("MUGIZH_SCHEDULER_TOKEN")}
	if s.HTTPAddr == "" {
		s.HTTPAddr = ":8080"
	}
	for _, m := range db.Modules {
		if v := getenv("MUGIZH_DB_URL_" + strings.ToUpper(m)); v != "" {
			s.DBURL[m] = v
		}
	}
	var err error
	if s.BlindIndexKey, err = b64(getenv("MUGIZH_BLIND_INDEX_KEY")); err != nil || len(s.BlindIndexKey) < 32 {
		return s, errors.New("settings: MUGIZH_BLIND_INDEX_KEY must be base64 of at least 32 bytes")
	}
	if s.SchedulerSecret, err = b64(getenv("MUGIZH_SCHEDULER_CALLBACK_SECRET")); err != nil || len(s.SchedulerSecret) < 32 {
		return s, errors.New("settings: MUGIZH_SCHEDULER_CALLBACK_SECRET must be base64 of at least 32 bytes")
	}
	if v := getenv("MUGIZH_LOCAL_ROOT_KEY"); v != "" {
		if s.LocalRootKey, err = b64(v); err != nil {
			return s, errors.New("settings: MUGIZH_LOCAL_ROOT_KEY is not base64")
		}
	}
	if v := getenv("TRUSTED_PROXIES"); v == "" && !s.devLike() {
		// Behind a load balancer the default of 0 would put every user in one rate-limit
		// bucket; too high a value would trust client-made headers (review M2).
		return s, errors.New("settings: TRUSTED_PROXIES must be set explicitly outside local, test and dev")
	} else if v != "" {
		if s.TrustedProxies, err = strconv.Atoi(v); err != nil || s.TrustedProxies < 0 || s.TrustedProxies > 5 {
			return s, errors.New("settings: TRUSTED_PROXIES must be 0..5")
		}
	}
	return s, nil
}

func b64(v string) ([]byte, error) { return base64.StdEncoding.DecodeString(strings.TrimSpace(v)) }

func (s Settings) devLike() bool {
	return s.Env == config.EnvLocal || s.Env == config.EnvTest || s.Env == config.EnvDev
}

// Core holds everything built at start-up.
type Core struct {
	Settings     Settings
	Log          *slog.Logger
	Config       *config.Config
	Pools        *db.Pools
	Keystore     *pgxpool.Pool
	Crypto       *crypto.Service
	Blob         blobstore.Store
	SMS          sms.Sender
	PushSender   push.Sender
	EmailSender  email.Sender
	Scanner      malwarescan.Scanner // one scanner for every upload path (records documents, verification evidence)
	Identity     *identity.Module
	Records      *records.Module
	Verification *verification.Module
	Directory    *directory.Module
	Payments     *payments.Module
	Booking      *booking.Module
	Notify       *notifications.Module
	Clinical     *clinical.Module
	Rx           *clinical.Rx
	Consult      *consult.Module
	resolver     *lateResolver
	BookingMsgs  *bookingMessages     // S-157 booking, consult and refund message composer
	Scheduler    schedulerClient      // scheduler-core (reminder activities and occurrences)
	Video        video.Client         // video vendor adapter (the fake in local, test and dev)
	PayVendor    paymentsadapter.Port // payment aggregator adapter (the fake in local, test and dev)
	Sharing      *records.Sharing     // S-168 history shares and extension requests
	History      *clinical.History    // S-169 doctor's view of a shared history
	ShareMsgs    *shareMessages
	Rights       *records.Rights
	Admin        *admin.Module
	Audit        *audit.Module
	Idem         *idempotency.Store
}

// Close releases pools.
func (c *Core) Close() {
	if c.Pools != nil {
		c.Pools.Close()
	}
	if c.Keystore != nil {
		c.Keystore.Close()
	}
}

// BuildCore validates configuration and builds the modules. Every failure stops start-up:
// the service never runs on partial configuration (country-pack-model §3 rule 1).
func BuildCore(ctx context.Context, s Settings, log *slog.Logger) (*Core, error) {
	cfg, err := config.Load(config.Options{PackDir: s.PackDir, VendorRegisterPath: s.VendorRegister, Env: s.Env})
	if err != nil {
		return nil, err
	}
	// Warnings may name vendors, so only their count is logged.
	log.Info("config.pack_loaded", "pack", cfg.Pack().Code(), "version", cfg.Pack().Version(), "count", len(cfg.Warnings()))
	pack := cfg.Pack()

	// Review F8: the test clock exists for end-to-end tests only; outside local, test and dev it is refused.
	if s.Now != nil && !s.devLike() {
		return nil, errors.New("start-up refused: a replaced clock is for tests only")
	}
	if s.Catalogs != nil && !s.devLike() {
		return nil, errors.New("start-up refused: replaced text catalogs are for tests only")
	}
	// Infrastructure adapters for staging and production are not built yet: refuse to
	// start rather than fall back to development stand-ins (fail closed).
	if !s.devLike() {
		return nil, fmt.Errorf("start-up refused in %s: the cloud KMS, S3-compatible storage and SMS gateway adapters are not built yet (S-093, S-098, S-041)", s.Env)
	}

	c := &Core{Settings: s, Log: log, Config: cfg}
	pools := map[string]*pgxpool.Pool{}
	for _, m := range db.Modules {
		dsn, ok := s.DBURL[m]
		if !ok {
			return nil, fmt.Errorf("settings: MUGIZH_DB_URL_%s is required", strings.ToUpper(m))
		}
		p, err := db.Open(ctx, dsn, db.Options{MaxConns: 8, RequireTLS: !s.devLike(), SearchPath: m})
		if err != nil {
			return nil, fmt.Errorf("db %s: %w", m, err)
		}
		pools[m] = p
	}
	c.Pools = db.NewPools(pools)
	if c.Keystore, err = db.Open(ctx, s.KeystoreURL, db.Options{MaxConns: 4, RequireTLS: !s.devLike(), SearchPath: "keystore"}); err != nil {
		c.Close()
		return nil, fmt.Errorf("keystore: %w", err)
	}
	kp, err := keys.NewLocal(string(s.Env), s.LocalRootKey)
	if err != nil {
		c.Close()
		return nil, err
	}
	if s.BlobDir == "" {
		c.Close()
		return nil, errors.New("settings: MUGIZH_BLOB_DIR is required in development")
	}
	c.Blob = blobstore.LocalDir{Root: s.BlobDir}
	if c.Crypto, err = crypto.NewService(c.Keystore, kp, c.Blob, s.BlindIndexKey); err != nil {
		c.Close()
		return nil, err
	}
	// Re-apply key destruction tombstones before serving (encryption-design §4 step 4).
	if n, err := c.Crypto.Replay(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("key tombstone replay: %w", err)
	} else if n > 0 {
		log.Info("crypto.tombstones_replayed", "count", n)
	}
	reg, err := sms.ParseRegistry(pack.Messaging().SMSRegistry)
	if err != nil {
		c.Close()
		return nil, err
	}
	c.SMS = &smsfake.Sender{Registry: reg}
	c.Scanner = &msfake.Scanner{}
	c.PushSender, c.EmailSender = &pushfake.Sender{}, &emailfake.Sender{}

	icfg := identity.DefaultConfig()
	icfg.PhoneCountryCodes = pack.People().PhoneCountryCodes
	icfg.PhonePattern = pack.People().PhoneNumberPattern
	if c.Identity, err = identity.New(c.Pools.For("identity"), icfg, c.Crypto, c.SMS, log); err != nil {
		c.Close()
		return nil, err
	}
	legal := pack.Legal()
	if legal.NoticeVersions["patient"] == "" || legal.NoticeVersions["provider"] == "" { // review F5: never record consent without its notice
		c.Close()
		return nil, errors.New("legal.notice_versions needs both patient and provider versions (country pack)")
	}
	if c.Records, err = records.New(c.Pools.For("records"), records.Config{ConsentVersions: legal.ConsentTextVersions,
		NoticeVersion: legal.NoticeVersions["patient"], ProviderNoticeVersion: legal.NoticeVersions["provider"], T1Window: 24 * time.Hour, JITMax: 4 * time.Hour}, log); err != nil {
		c.Close()
		return nil, err
	}
	validators := make([]string, 0, len(pack.People().ProhibitedIdentifiers))
	for _, pi := range pack.People().ProhibitedIdentifiers {
		validators = append(validators, pi.Validator)
	}
	vcfg := verification.DefaultConfig()
	vcfg.ProhibitedIDValidators = validators
	vcfg.IDTypes = pack.People().AcceptedPhotoIDTypes
	// Development stand-ins only (BuildCore refuses other environments above).
	if c.Verification, err = verification.New(c.Pools.For("verification"), vcfg, verification.Deps{Crypto: c.Crypto, Blob: c.Blob,
		Checker: &vfake.Checker{}, Scanner: c.Scanner, Consent: c.Records}, log); err != nil {
		c.Close()
		return nil, err
	}
	if c.Directory, err = directory.New(c.Pools.For("directory"), directory.Config{Currency: pack.Money().Currency, CountryCode: pack.Code(),
		DefaultTimeZone: pack.Locale().DefaultTimeZone, ProhibitedIDValidators: validators, MaxRadiusKm: 50, DefaultRadiusKm: 10,
		MaxFeeMinor: 100_000_000, ClaimWords: directory.DefaultClaimWords, MinGeocodeAccuracy: geocoding.Street},
		&gfake.Geocoder{}, c.Identity, log); err != nil {
		c.Close()
		return nil, err
	}
	pol, err := refundpolicy.Parse(pack.Payments().RefundPolicy)
	if err != nil {
		c.Close()
		return nil, err
	}
	win, err := parseWindows(pack.Payments().RefundPolicy)
	if err != nil {
		c.Close()
		return nil, err
	}
	docs, err := documentConfig(s, pack, validators)
	if err != nil {
		c.Close()
		return nil, err
	}
	// Development stand-in aggregator only (BuildCore refuses other environments above).
	c.PayVendor = pfake.New()
	if c.Payments, err = payments.New(c.Pools.For("payments"), c.PayVendor, payments.Config{Currency: pack.Money().Currency,
		AllowedModes: pack.Payments().SettlementModesAllowed, Policy: pol, Documents: docs}, log); err != nil {
		c.Close()
		return nil, err
	}
	if c.Booking, err = booking.New(c.Pools.For("booking"), booking.Config{RefundPolicyVersion: win.Version, HoldTTL: win.holdTTL(),
		RescheduleMinMinutes: win.Windows.FreeCancelBeforeMinutes, FreeReschedules: win.Windows.FreeReschedules,
		PatientGraceMinutes: win.Windows.PatientGraceMinutes, DoctorJoinTimeoutMinutes: win.Windows.DoctorJoinTimeoutMinutes},
		booking.Deps{Directory: c.Directory, Payments: c.Payments, Gate: c.Records, Refunds: c.Payments, Verifier: c.Verification}, log); err != nil {
		c.Close()
		return nil, err
	}
	c.Directory.SetAvailability(c.Booking)
	// S-108: profile age gate and prohibited-identifier check come from the pack.
	c.Identity.SetProfileConfig(identity.ProfileConfig{MinAccountAge: pack.People().MinAccountAge, ProhibitedValidators: validators})
	// S-135 to S-148: the "I am in India now" declaration stands for exactly one pack country
	// (P10); a pack listing several would change what the patient declares, so it is refused.
	teleVersion := legal.ConsentTextVersions[cp.ConsentTextTeleconsult]
	countries := pack.Clinical().OnlineConsultPatientCountries
	if teleVersion <= 0 || len(countries) != 1 {
		c.Close()
		return nil, errors.New("consult: the pack needs the teleconsultation consent version and exactly one online-consult patient country")
	}
	ccfg := consult.DefaultConfig()
	ccfg.ConsentTextVersion, ccfg.DeclaredCountry = teleVersion, countries[0]
	// The same windows booking uses (refund policy file), so consult and booking agree on when a
	// doctor or patient no-show can be decided.
	ccfg.DoctorJoinTimeout = time.Duration(win.Windows.DoctorJoinTimeoutMinutes) * time.Minute
	ccfg.PatientNoShowGrace = time.Duration(win.Windows.PatientGraceMinutes) * time.Minute
	// Development stand-in video only (BuildCore refuses other environments above).
	c.Video = &videofake.Client{}
	if c.Consult, err = consult.New(c.Pools.For("consult"), ccfg, consult.Deps{Booking: c.Booking, Video: c.Video,
		Gate: c.Records, Profiles: c.Identity, Verifier: c.Verification}, log); err != nil {
		c.Close()
		return nil, err
	}
	if c.Scheduler, err = newSchedulerClient(s); err != nil {
		c.Close()
		return nil, err
	}
	c.resolver = &lateResolver{}
	if c.Notify, err = buildNotifications(c, reg, s.Catalogs); err != nil {
		c.Close()
		return nil, err
	}
	c.BookingMsgs = &bookingMessages{send: c.Notify, booking: c.Booking, names: c.Directory, minorUnits: pack.Money().MinorUnits, log: log}
	if err := buildClinical(ctx, c, validators); err != nil {
		c.Close()
		return nil, err
	}
	c.resolver.set(c.Rx)
	// S-168/S-169 (consent-design T3/T4, E-DP-01/E-DP-02): shares tie to the patient's live appointment with
	// a verified doctor; default 7 days after it; extensions up to 24 h need the patient's approval
	// ([CONFIRM] D-59); the doctor is warned 10 minutes before a share ends.
	if c.Sharing, err = records.NewSharing(c.Records, records.ShareConfig{DefaultAfterAppointment: 7 * 24 * time.Hour, ExtensionMax: 24 * time.Hour,
		WarnBefore: 10 * time.Minute, RequestTTL: 7 * 24 * time.Hour}, shareAppointments{c.Booking}, c.Verification); err != nil {
		c.Close()
		return nil, err
	}
	if c.History, err = clinical.NewHistory(c.Clinical, clinical.HistoryConfig{WarnBefore: 10 * time.Minute},
		clinical.HistoryDeps{Crypto: c.Crypto, Gate: c.Records, Windows: c.Records, Verifier: c.Verification, Log: log, Now: s.Now}); err != nil {
		c.Close()
		return nil, err
	}
	c.ShareMsgs = &shareMessages{send: c.Notify, names: c.Directory, log: log}
	c.Records.SetRoleChecker(c.Identity)
	c.Audit = audit.New(c.Pools.For("audit"), c.Blob, log)
	c.Idem = idempotency.New(c.Pools.For("platform"), c.Crypto)
	regHold, procHold, procFrom, err := ret18(pack.Retention())
	if err != nil {
		c.Close()
		return nil, err
	}
	g := legal.Grievance
	if g.DataRightsResponseDays <= 0 || g.MedicalRecordCopyHours <= 0 || g.AcknowledgeHours <= 0 || g.ResolveDays <= 0 {
		c.Close()
		return nil, errors.New("legal.grievance timers are incomplete (country pack)")
	}
	hours := func(h int) time.Duration { return time.Duration(h) * time.Hour }
	if c.Rights, err = records.NewRights(c.Records, records.RightsConfig{
		// Every module that holds patient data reports its erasure task (K-DP-07). An erasure
		// stays restricted until all have reported, so a missing consumer can never produce a
		// false "erased".
		Modules:       []string{"records", "identity", "booking", "payments", "clinical", "consult", "notifications"},
		RespondWithin: hours(24 * g.DataRightsResponseDays), RecordCopyWithin: hours(g.MedicalRecordCopyHours), AckWithin: hours(g.AcknowledgeHours),
		UndoWindow:       7 * 24 * time.Hour, // [VALIDATE] decisions log
		HoldRegistration: regHold, HoldProcessing: procHold, HoldProcessingFrom: procFrom,
		ExportAvailability: 24 * time.Hour, LinkTTL: 5 * time.Minute, UploadMaxBytes: UploadMaxBytes, ScanTimeout: 30 * time.Second,
		ProhibitedValidators: validators,
	}, records.RightsDeps{Crypto: c.Crypto, Blobs: c.Blob, Scanner: c.Scanner, Notifier: rightsNotifier{c.Notify}, AccessLog: c.Audit}); err != nil {
		c.Close()
		return nil, err
	}
	if c.Admin, err = admin.New(c.Pools.For("admin"), admin.Config{JITMax: 4 * time.Hour, ApprovalWindow: time.Hour, JustificationWithin: 24 * time.Hour,
		AckWithin: hours(g.AcknowledgeHours), ResolveWithin: hours(24 * g.ResolveDays), ContentTakedown: hours(g.UnlawfulContentRemovalHours),
		OrderCompliance: hours(g.OrderComplianceHours), IntimateImage: hours(g.IntimateImageRemovalHours), LawfulRequest: 72 * time.Hour,
		MedicalRecordCopy: hours(g.MedicalRecordCopyHours), DataRightsResponse: hours(24 * g.DataRightsResponseDays),
		DataRightsAlertAt: hours(24 * (g.DataRightsResponseDays * 2 / 3)), AlertFraction: 0.8, ProhibitedValidators: validators},
		admin.Deps{Records: c.Records, Roles: c.Identity, Crypto: c.Crypto, Accounts: c.Identity}, log); err != nil {
		c.Close()
		return nil, err
	}
	if s.Now != nil { // end-to-end tests: one clock for the whole system
		for _, set := range []func(func() time.Time){c.Identity.SetClock, c.Records.SetClock, c.Verification.SetClock, c.Directory.SetClock,
			c.Payments.SetClock, c.Booking.SetClock, c.Notify.SetClock, c.Consult.SetClock, c.Admin.SetClock, c.Audit.SetClock} {
			set(s.Now)
		}
	}
	c.wireAlerts(log)
	// Re-apply erasure requests, undos and completions that a database restore rolled back,
	// after the key tombstones and before anything is served (S-185, E-DP-04). A journal
	// that cannot be read stops start-up.
	if _, err := c.Rights.ReplayJournal(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("erasure journal replay: %w", err)
	}
	return c, nil
}

// Ready checks every pool.
func (c *Core) Ready(ctx context.Context) error {
	for _, n := range c.Pools.Names() {
		if err := c.Pools.For(n).Ping(ctx); err != nil {
			return err
		}
	}
	return c.Keystore.Ping(ctx)
}

// MigrationSources lists the Core database migrations in order (bootstrap first).
func MigrationSources() []migrate.Source {
	return []migrate.Source{dbschema.Source(), audit.Source(), idempotency.Source(), identity.Source(), records.Source(),
		verification.Source(), directory.Source(), payments.Source(), booking.Source(), notifications.Source(), admin.Source(), clinical.Source(), consult.Source()}
}

// RequireTLS reports whether database connections must use TLS (everything except
// local, test and dev; encryption-design §1).
func (s Settings) RequireTLS() bool { return !s.devLike() }

// UploadMaxBytes is the largest patient upload (S-159). The upload route gets a matching
// body limit; every other route keeps the 1 MB default (API standards §8).
const UploadMaxBytes = 10 << 20

// EvidenceMaxBytes is the largest doctor identity or registration document (S-110).
const EvidenceMaxBytes = 10 << 20
