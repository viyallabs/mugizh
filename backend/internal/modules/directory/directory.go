// Package directory is the directory module: doctor and clinic profiles, practices and
// radius search (S-112, S-117, S-119, S-120, S-162, module-boundaries §2).
//
// Safety rules built in:
//   - K-CS-01: a practice is listed only while its doctor, its clinic and the clinic's
//     lead doctor are all Verified in the local verification read model (B5). A missing
//     or any other status means not listed; a suspension takes effect on the next read.
//   - K-CS-08 / CD-05: ranking uses distance and availability only. See RankingFactors.
//   - RG-04: a specialty is accepted only with a recognised postgraduate qualification.
//   - CD-06: free text is checked against a configured word list of superlatives, awards
//     and success-rate claims.
//   - RET-12: the search location is used for the query and is never stored or logged.
package directory

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"time"
	_ "time/tzdata" // IANA zone data for validation, independent of the host image

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/geocoding"
	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Source returns the module's migrations.
func Source() migrate.Source {
	sub, _ := fs.Sub(migrations, "migrations")
	return migrate.Source{Module: "directory", Schema: "directory", FS: sub}
}

// Consult modes a practice can offer.
const (
	ModeInPerson = "in_person"
	ModeVideo    = "video"
	ModeAudio    = "audio"
)

var validModes = map[string]bool{ModeInPerson: true, ModeVideo: true, ModeAudio: true}

// StatusVerified is the only verification status that lists a doctor or a clinic.
const StatusVerified = "verified"

// DefaultClaimWords is a starting word list for CD-06 (no "best", "top", awards or
// success rates). It is only a default for tests and the composition root: the list in
// force is Config.ClaimWords, reviewed by the founder and the lawyer [VALIDATE].
// Matching is by whole words after lower-casing and removing punctuation, so "No. 1"
// matches "no 1". It will sometimes refuse an innocent text ("top floor"); a refused
// text is fixed by rewording, which is the safe side of the error.
var DefaultClaimWords = []string{
	"best", "top", "no 1", "number one", "number 1", "leading", "foremost", "finest", "greatest",
	"famous", "renowned", "celebrated", "premier", "unmatched", "unrivalled", "unparalleled", "world class",
	"award", "awards", "awarded", "award winning", "winner", "medal", "medalist", "medallist",
	"success rate", "success rates", "successful", "guarantee", "guaranteed", "cure", "cures", "cured", "miracle", "percent",
}

// Config holds values from the country pack and decisions. Nothing here is a code constant.
type Config struct {
	Currency        string // pack money.currency
	CountryCode     string // pack code, sent to the geocoder
	DefaultTimeZone string // pack locale.default_time_zone
	// ProhibitedIDValidators are the pack's prohibited-identifier validators (K-DP-05):
	// free text holding such a number is refused.
	ProhibitedIDValidators []string
	// PostalCodePattern validates the normalised (upper-case, no spaces) PIN or postal
	// code; empty means letters, digits and hyphen, 3 to 10 characters.
	PostalCodePattern string
	MaxRadiusKm       float64
	DefaultRadiusKm   float64
	MaxFeeMinor       int64
	ClaimWords        []string
	// RequireRegistrationDoc makes a clinic submission carry a registration document
	// reference (country pack decision).
	RequireRegistrationDoc bool
	// MinGeocodeAccuracy is the coarsest accepted geocoding result; coarser results ask
	// for a manual pin.
	MinGeocodeAccuracy geocoding.Accuracy
	GeocodeTimeout     time.Duration // default 3 s (adapter-interfaces §10)
	// MaxRankCandidates caps the nearest practices re-ranked in memory when an
	// availability source is present. Default 500.
	MaxRankCandidates int
	// VerificationChecks are codes shown as "what was checked" (S-161), supplied by the
	// verification owner. Empty means the badge shows no detail.
	VerificationChecks []string
}

// Module implements public.Reader and the directory operations.
type Module struct {
	pool  *pgxpool.Pool
	cfg   Config
	geo   geocoding.Geocoder
	roles ip.RoleAssigner
	log   *slog.Logger
	now   func() time.Time

	claims  *claimFilter
	postal  *regexp.Regexp
	avail   availabilityHolder
	handler string
}

// New builds the module. pool must use the directory role. geo and roles are injected
// ports (a fake in tests).
func New(pool *pgxpool.Pool, cfg Config, geo geocoding.Geocoder, roles ip.RoleAssigner, log *slog.Logger) (*Module, error) {
	switch {
	case pool == nil || geo == nil || roles == nil || log == nil:
		return nil, errors.New("directory: pool, geocoder, role assigner and logger are required")
	case cfg.Currency == "" || cfg.CountryCode == "":
		return nil, errors.New("directory: currency and country code are required (country pack)")
	case len(cfg.ProhibitedIDValidators) == 0:
		return nil, errors.New("directory: prohibited-identifier validators are required (country pack, K-DP-05)")
	case cfg.MaxRadiusKm <= 0 || cfg.DefaultRadiusKm <= 0 || cfg.DefaultRadiusKm > cfg.MaxRadiusKm:
		return nil, errors.New("directory: radius limits are required")
	case cfg.MaxFeeMinor <= 0:
		return nil, errors.New("directory: maximum fee is required")
	case len(cfg.ClaimWords) == 0:
		return nil, errors.New("directory: the claim word list is required (CD-06)")
	case !cfg.MinGeocodeAccuracy.Valid():
		return nil, errors.New("directory: minimum geocoding accuracy is required")
	}
	if _, err := time.LoadLocation(cfg.DefaultTimeZone); err != nil || cfg.DefaultTimeZone == "" || cfg.DefaultTimeZone == "Local" {
		return nil, errors.New("directory: default time zone must be an IANA zone")
	}
	if cfg.GeocodeTimeout <= 0 {
		cfg.GeocodeTimeout = 3 * time.Second
	}
	if cfg.MaxRankCandidates <= 0 {
		cfg.MaxRankCandidates = 500
	}
	pat := cfg.PostalCodePattern
	if pat == "" {
		pat = `^[A-Z0-9\-]{3,10}$`
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, fmt.Errorf("directory: postal code pattern: %w", err)
	}
	return &Module{pool: pool, cfg: cfg, geo: geo, roles: roles, log: log, now: func() time.Time { return time.Now().UTC() },
		claims: newClaimFilter(cfg.ClaimWords), postal: re, handler: "directory.verification_status_view"}, nil
}

// SetClock replaces the clock (tests).
func (m *Module) SetClock(now func() time.Time) { m.now = now }

// Actor is who is changing something, built by the HTTP layer from the principal.
type Actor struct {
	UserID    string
	Role      string // acting role: doctor, clinic_admin or authenticated
	ClinicRef string // clinic scope of the acting role
	RequestID string
}

func (m *Module) audit(ctx context.Context, tx pgx.Tx, a Actor, action, resourceType, resourceRef, subject string) error {
	return ap.Record(ctx, tx, ap.Event{ActorRef: a.UserID, ActorRole: a.Role, Action: action, SubjectRef: subject,
		ResourceType: resourceType, ResourceRef: resourceRef, Result: ap.Allowed, RequestID: a.RequestID})
}

func (m *Module) emitPracticeUpdated(ctx context.Context, tx pgx.Tx, practiceID string) error {
	return outbox.Emit(ctx, tx, outbox.Event{Type: "practice.updated", SubjectRef: practiceID, Payload: map[string]string{"practiceRef": practiceID}})
}

func (m *Module) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, fn)
}

// listedFrom is the single definition of "listed" for reads that join (K-CS-01): the
// practice is active, its doctor and the clinic's lead doctor accepted their links (review M5),
// and its doctor, its clinic and the clinic's lead doctor are all
// Verified in the read model, and its branch is active (S-130: a branch is listed only
// while its clinic is Verified, which the clinic join already requires). An absent row is
// not verified. Callers add WHERE pr.active.
const listedFrom = `
 FROM practice pr
 JOIN clinic c ON c.id = pr.clinic_id AND c.lead_accepted AND pr.doctor_accepted
 JOIN branch b ON b.id = pr.branch_id AND b.active
 JOIN professional pf ON pf.id = pr.professional_id
 JOIN verification_status_view vp ON vp.professional_ref = pr.professional_id AND vp.status = 'verified'
 JOIN verification_status_view vc ON vc.professional_ref = c.id AND vc.status = 'verified'
 JOIN verification_status_view vl ON vl.professional_ref = c.lead_professional_id AND vl.status = 'verified'`

// listedExpr is the same rule as a boolean expression, for management views that must
// also show unlisted rows to their owners (aliases pr and c).
const listedExpr = `(pr.active AND pr.doctor_accepted AND c.lead_accepted
  AND EXISTS (SELECT 1 FROM branch b WHERE b.id = pr.branch_id AND b.active)
  AND EXISTS (SELECT 1 FROM verification_status_view v WHERE v.professional_ref = pr.professional_id AND v.status = 'verified')
  AND EXISTS (SELECT 1 FROM verification_status_view v WHERE v.professional_ref = c.id AND v.status = 'verified')
  AND EXISTS (SELECT 1 FROM verification_status_view v WHERE v.professional_ref = c.lead_professional_id AND v.status = 'verified'))`
