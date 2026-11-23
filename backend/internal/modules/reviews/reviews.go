// Package reviews is the reviews module (F15): patients rate a doctor after a completed visit
// (S-209, backend part) and the rating is shown as a Bayesian average (S-214).
//
// Rules built in (docs/legal/review-policy.md REV-1.0-draft, CD-05, CD-08):
//   - E-REV-01 / TC-51: only the patient of a completed appointment can review, inside the
//     window, once per visit. Another patient's or an unknown appointment is "not found"
//     (K-SEC-04). The eligibility read model comes from booking's appointment.completed (B5).
//   - E-REV-03: reviewing is never forced; the rating is 1 to 5 and the text is optional.
//   - K-DP-03: the text is sealed with the patient's key and never stored or returned in
//     plaintext; only its author gets it back. K-DP-04: it is never logged.
//   - Text is pending_moderation and is never published here: the human moderation of S-213
//     needs staff sign-in, which does not exist yet. Star ratings count at once.
//   - K-DP-05: text holding an identity number (the pack's prohibited identifiers) is refused.
//   - CD-05: the rating takes no input from payment or anything commercial.
//   - K-DP-07: erasure deletes the subject's reviews and visit rows and fixes the aggregates.
//   - K-CS-01: the public rating is shown only for a Verified doctor.
package reviews

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/reviews/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Source returns the module's migrations.
func Source() migrate.Source {
	sub, _ := fs.Sub(migrations, "migrations")
	return migrate.Source{Module: "reviews", Schema: "reviews", FS: sub}
}

const rolePatient = "patient"

// Config holds the review window, the text limit and the rating constants.
type Config struct {
	// Window is how long after the visit a review can be written (review policy: 14 days). [CONFIRM]
	Window time.Duration
	// MaxTextRunes is the longest review text in characters (the API says 1000).
	MaxTextRunes int
	// BayesianC is C in (C x m + sum) / (C + n) (algorithms.md: 10). [CONFIRM]
	BayesianC float64
	// PriorMean is m while the platform has fewer than MinPlatformRatings ratings (3.0, the scale
	// midpoint). [CONFIRM]
	PriorMean float64
	// MinPlatformRatings is how many ratings the platform needs before its own mean is used as m (50). [CONFIRM]
	MinPlatformRatings int
	// ProhibitedIDValidators are the pack's prohibited-identifier validators (people.prohibited_identifiers; K-DP-05).
	ProhibitedIDValidators []string
}

// DefaultConfig returns the documented defaults. ProhibitedIDValidators must still be set from the pack.
func DefaultConfig() Config {
	return Config{Window: 14 * 24 * time.Hour, MaxTextRunes: 1000, BayesianC: 10, PriorMean: 3.0, MinPlatformRatings: 50}
}

func (c Config) validate() error {
	if c.Window <= 0 || c.MaxTextRunes <= 0 || c.MinPlatformRatings <= 0 || c.BayesianC <= 0 || c.PriorMean < 1 || c.PriorMean > 5 {
		return errors.New("reviews: window, text limit, rating constants (C > 0, prior mean 1 to 5) and the platform minimum are required")
	}
	return nil
}

// Verifier is satisfied by *verification.Module (K-CS-01).
type Verifier interface {
	IsVerified(ctx context.Context, professionalRef string) (bool, error)
}

// Deps are the other modules and services reviews uses, through public interfaces.
type Deps struct {
	Booking  bp.Service
	Verifier Verifier
	Crypto   *crypto.Service
}

// Module is the reviews module.
type Module struct {
	pool *pgxpool.Pool
	cfg  Config
	d    Deps
	log  *slog.Logger
	now  func() time.Time
}

var _ rp.RatingReader = (*Module)(nil)

// New builds the module. pool must use the reviews role.
func New(pool *pgxpool.Pool, cfg Config, d Deps, log *slog.Logger) (*Module, error) {
	if pool == nil || log == nil {
		return nil, errors.New("reviews: pool and logger are required")
	}
	if d.Booking == nil || d.Verifier == nil || d.Crypto == nil {
		return nil, errors.New("reviews: booking, verifier and crypto are required")
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Module{pool: pool, cfg: cfg, d: d, log: log, now: func() time.Time { return time.Now().UTC() }}, nil
}

// SetClock replaces the clock (tests).
func (m *Module) SetClock(now func() time.Time) { m.now = now }

// Problems.
var (
	ErrNotEligible     = problem.New(http.StatusConflict, "review.not_eligible", "This visit cannot be reviewed")
	ErrAlreadyReviewed = problem.New(http.StatusConflict, "review.already_reviewed", "This visit has already been reviewed")
	ErrInvalid         = problem.New(http.StatusUnprocessableEntity, "review.invalid", "The review is not valid")
)

// Actor is who is acting, built by the HTTP layer from the principal.
type Actor struct {
	UserID    string
	Role      string // patient only
	RequestID string
}

func (m *Module) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, fn)
}
