// Package identity is the identity module (S-104 patient OTP sign-in with device binding,
// S-105 professional MFA, S-106 roles, S-115 device sign-out). It is built in-house
// instead of on Ory Kratos because Kratos keeps phone numbers in plaintext, which breaks
// K-DP-03 (decision D-04, proposed ADR-32).
package identity

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Source returns the module's migrations.
func Source() migrate.Source {
	sub, _ := fs.Sub(migrations, "migrations")
	return migrate.Source{Module: "identity", Schema: "identity", FS: sub}
}

// Config holds identity settings. Country values (phone codes) come from the pack.
type Config struct {
	PhoneCountryCodes []string // pack people.phone_country_codes, e.g. one code in v1.0 (TM-02)
	// PhonePattern is the pack's full-number pattern (people.phone_number_pattern), so
	// variants of one number (a stray leading zero) cannot create two accounts or dodge
	// per-number limits (review L7).
	PhonePattern string

	OTPTTL         time.Duration // 5 minutes (SMS-01 text says so)
	OTPMaxAttempts int           // 5 (TM-01)
	PhonePer10Min  int
	PhonePerDay    int
	IPPer10Min     int
	IPPerDay       int
	GlobalPerDay   int // daily SMS spend cap; reaching it trips the kill switch (TM-02)
	LockBase       time.Duration
	LockMax        time.Duration

	AccessTTL          time.Duration // ~15 minutes (API standards §8)
	PatientRefreshTTL  time.Duration
	PatientAbsoluteTTL time.Duration
	ProIdle            time.Duration // 30 minutes [CONFIRM] (access-control-design §6)
	ProAbsolute        time.Duration // 12 hours
	ProofSkew          time.Duration // allowed clock skew of device proofs
	MFAMaxFailures     int

	TOTPIssuer string // shown in the authenticator app; product name pending (S-016)
}

// DefaultConfig returns the documented defaults (phone codes must still be set from the pack).
func DefaultConfig() Config {
	return Config{
		OTPTTL: 5 * time.Minute, OTPMaxAttempts: 5,
		PhonePer10Min: 3, PhonePerDay: 10, IPPer10Min: 10, IPPerDay: 50, GlobalPerDay: 20000,
		LockBase: 15 * time.Minute, LockMax: 24 * time.Hour,
		AccessTTL: 15 * time.Minute, PatientRefreshTTL: 30 * 24 * time.Hour, PatientAbsoluteTTL: 180 * 24 * time.Hour,
		ProIdle: 30 * time.Minute, ProAbsolute: 12 * time.Hour, ProofSkew: 2 * time.Minute, MFAMaxFailures: 5,
		TOTPIssuer: "Mugizh",
	}
}

func (c Config) validate() error {
	if len(c.PhoneCountryCodes) == 0 {
		return errors.New("identity: phone country codes are required (country pack)")
	}
	for _, cc := range c.PhoneCountryCodes {
		if !regexp.MustCompile(`^\+[1-9][0-9]{0,2}$`).MatchString(cc) {
			return fmt.Errorf("identity: bad phone country code %q", cc)
		}
	}
	if re, err := regexp.Compile(c.PhonePattern); err != nil || !strings.HasPrefix(c.PhonePattern, "^") || !strings.HasSuffix(c.PhonePattern, "$") || re == nil {
		return errors.New("identity: an anchored phone number pattern is required (country pack)")
	}
	if c.OTPTTL <= 0 || c.OTPMaxAttempts <= 0 || c.AccessTTL <= 0 || c.ProIdle <= 0 || c.ProAbsolute <= 0 ||
		c.PatientRefreshTTL <= 0 || c.PatientAbsoluteTTL <= 0 || c.ProofSkew <= 0 || c.GlobalPerDay <= 0 || c.MFAMaxFailures <= 0 {
		return errors.New("identity: incomplete config")
	}
	return nil
}

// Module is the identity module.
type Module struct {
	pool       *pgxpool.Pool
	cfg        Config
	cr         *crypto.Service
	sms        sms.Sender
	phoneRe    *regexp.Regexp
	log        *slog.Logger
	profileCfg ProfileConfig // S-108, set by SetProfileConfig
	now        func() time.Time
	// Alert is called on security events (SMS cap reached, refresh-token reuse).
	Alert func(ctx context.Context, code string)
}

// New builds the module. pool must use the identity role.
func New(pool *pgxpool.Pool, cfg Config, cr *crypto.Service, sender sms.Sender, log *slog.Logger) (*Module, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if pool == nil || cr == nil || sender == nil {
		return nil, errors.New("identity: pool, crypto and SMS sender are required")
	}
	return &Module{pool: pool, cfg: cfg, cr: cr, sms: sender, log: log, now: time.Now, phoneRe: regexp.MustCompile(cfg.PhonePattern),
		Alert: func(context.Context, string) {}}, nil
}

// SetClock replaces the clock (tests).
func (m *Module) SetClock(now func() time.Time) { m.now = now }

// Errors, as problems the HTTP layer returns directly.
var (
	ErrPhoneNotAllowed = problem.New(http.StatusBadRequest, "identity.phone_not_supported", "This phone number cannot be used")
	ErrOTPInvalid      = problem.New(http.StatusUnauthorized, "identity.otp_invalid", "The code is wrong or has expired")
	ErrLocked          = problem.New(http.StatusTooManyRequests, "identity.locked", "Too many attempts; try again later")
	ErrRateLimited     = problem.New(http.StatusTooManyRequests, "identity.otp_rate_limited", "Too many codes requested; try again later")
	ErrSMSUnavailable  = problem.New(http.StatusServiceUnavailable, "identity.otp_unavailable", "Sign-in codes cannot be sent right now")
	ErrTokenInvalid    = problem.New(http.StatusUnauthorized, "auth.token_invalid", "Sign in again")
	ErrDeviceKey       = problem.New(http.StatusBadRequest, "identity.device_key_invalid", "The device key is not valid")
	ErrStaffSignIn     = problem.New(http.StatusForbidden, "identity.staff_sign_in_required", "Staff sign in with single sign-on")
	ErrMFAInvalid      = problem.New(http.StatusUnauthorized, "identity.mfa_invalid", "The authenticator code is wrong")
	ErrMFAState        = problem.New(http.StatusConflict, "identity.mfa_state", "Multi-factor set-up is not in the right state")
	ErrNotFound        = problem.NotFound
)

var reE164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

// normalisePhone accepts E.164 with optional spaces or hyphens and checks the country
// code against the pack's allow-list.
func (m *Module) normalisePhone(raw string) (string, error) {
	p := strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(raw))
	if !reE164.MatchString(p) {
		return "", ErrPhoneNotAllowed
	}
	for _, cc := range m.cfg.PhoneCountryCodes {
		if strings.HasPrefix(p, cc) && m.phoneRe.MatchString(p) {
			return p, nil
		}
	}
	return "", ErrPhoneNotAllowed
}

func (m *Module) phoneIndex(e164 string) []byte { return m.cr.BlindIndex("identity.phone", e164) }

func phoneAAD(userID string) crypto.AAD {
	return crypto.AAD{Table: "identity.user_account", Column: "phone_ct", RowID: userID}
}
