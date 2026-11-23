// Package payments is the payments module: orders on each clinic's own merchant account
// (mode B, K-PAY-03), signature-verified webhooks, durable operation records for orders and
// refunds (K-PAY-02), the append-only double-entry ledger (K-PAY-04), the refund policy
// (K-PAY-05) and daily reconciliation (S-146).
//
// Money safety rules this file family keeps:
//   - Every order and refund is a row with a UNIQUE operation key and OUR merchant reference.
//     Retries reuse both; nothing is ever created twice.
//   - A Timeout (or any answer that does not prove the vendor did NOT act) moves the record
//     to `unknown`. It is resolved with findOrder / findRefund, never by a blind retry. Only
//     when the vendor's settle time has passed and the lookup still says NotFound is the call
//     repeated, with the SAME idempotency key and merchant reference.
//   - State changes come only from signature-verified webhooks or status polls; a vendor
//     answer to create/refund never marks money as captured or refunded.
//   - Amounts are chosen by the server (booking), and every webhook must match our order's
//     amount and currency.
package payments

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	adp "gitlab.com/viyallabs/mugizh/backend/internal/adapters/payments"
	rp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/refundpolicy"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/migrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Source returns the module's migrations.
func Source() migrate.Source {
	sub, _ := fs.Sub(migrations, "migrations")
	return migrate.Source{Module: "payments", Schema: "payments", FS: sub}
}

// Config holds values from the country pack and decisions log. Nothing country-specific is
// a constant here (K-GLB-01).
type Config struct {
	// Currency is the pack's currency code (pack money.currency). Orders in any other
	// currency are refused.
	Currency string
	// AllowedModes is the pack's payments.settlement_modes_allowed. "split" is refused
	// whatever the pack says (v1.2).
	AllowedModes []string
	// Policy is the refund policy version in force (pack payments.refund_policy).
	Policy *refundpolicy.Policy
	// PolicyByVersion returns an older policy version stored with a booking (optional).
	// A version that is neither current nor returned here fails closed.
	PolicyByVersion func(version string) (*refundpolicy.Policy, bool)
	// OverrideFor returns a clinic's own (more generous) refund override, or nil.
	OverrideFor func(ctx context.Context, clinicRef string) *refundpolicy.Override
	// Documents configures patient payment documents (S-134); nil means none are issued.
	Documents *DocumentConfig

	// SettleAfter is the vendor's settle time: a findOrder/findRefund NotFound is only
	// believed after this long since the call (adapter-interfaces section 4). Default 10 min.
	SettleAfter time.Duration
	// CallLease stops two workers calling the vendor for one record at once. Default 2 min.
	CallLease time.Duration
	// CallBackoff is the wait after a definite "not applied" answer (rate limit). Default 30 s.
	CallBackoff time.Duration
	// PollAfter: an open order is polled once it is this old. Default 1 min.
	PollAfter time.Duration
	// LatePollWindow: how long after the hold expires an unpaid order is still polled, to
	// catch a late debit (E-PAY-01). Default 24 h.
	LatePollWindow time.Duration
	// PauseAfter: unpaid refunds for lack of clinic balance pause new bookings after this
	// long (E-PAY-05 [CONFIRM] 48 h). Default 48 h.
	PauseAfter time.Duration
	// RefundBackoff is the retry schedule for refunds rejected for insufficient balance.
	RefundBackoff []time.Duration
}

// Module is the payments module.
type Module struct {
	pool *pgxpool.Pool
	port adp.Port
	cfg  Config
	log  *slog.Logger
	now  func() time.Time

	// Alert is called (after the change is committed) when something needs a person:
	// clinic balance shortfall, reconciliation mismatch, rejected webhook, failed order.
	Alert func(ctx context.Context, code string)
}

// Alert codes.
const (
	AlertRefundShortfall   = "payments.refund_shortfall"
	AlertRefundFailed      = "payments.refund_failed"
	AlertReconMismatch     = "payments.reconciliation_mismatch"
	AlertWebhookRejected   = "payments.webhook_rejected"
	AlertOrderFailed       = "payments.order_failed"
	AlertAuthFailed        = "payments.vendor_auth_failed"
	AlertRefundNotPossible = "payments.refund_not_possible"
	// AlertDocumentProfileMissing: a payment was captured for a clinic whose document profile is
	// incomplete, so no patient document could be issued (S-134).
	AlertDocumentProfileMissing = "payments.document_profile_missing"
)

// New builds the module. pool must use the payments role.
func New(pool *pgxpool.Pool, port adp.Port, cfg Config, log *slog.Logger) (*Module, error) {
	if pool == nil || port == nil {
		return nil, errors.New("payments: pool and payments adapter are required")
	}
	if cfg.Currency == "" || cfg.Policy == nil {
		return nil, errors.New("payments: currency and refund policy are required (country pack)")
	}
	if cfg.Documents != nil {
		if err := cfg.Documents.validate(); err != nil {
			return nil, err
		}
	}
	if cfg.Policy.Currency != cfg.Currency {
		return nil, errors.New("payments: refund policy currency differs from the pack currency")
	}
	if len(cfg.AllowedModes) == 0 {
		return nil, errors.New("payments: settlement modes allowed are required (country pack)")
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&cfg.SettleAfter, 10*time.Minute)
	def(&cfg.CallLease, 2*time.Minute)
	def(&cfg.CallBackoff, 30*time.Second)
	def(&cfg.PollAfter, time.Minute)
	def(&cfg.LatePollWindow, 24*time.Hour)
	def(&cfg.PauseAfter, 48*time.Hour)
	if len(cfg.RefundBackoff) == 0 {
		cfg.RefundBackoff = []time.Duration{15 * time.Minute, time.Hour, 4 * time.Hour, 12 * time.Hour, 24 * time.Hour}
	}
	return &Module{pool: pool, port: port, cfg: cfg, log: log, now: func() time.Time { return time.Now().UTC() },
		Alert: func(context.Context, string) {}}, nil
}

// SetClock replaces the clock (tests).
func (m *Module) SetClock(now func() time.Time) { m.now = func() time.Time { return now().UTC() } }

var _ rp.Service = (*Module)(nil)

// effects are the things to do AFTER a transaction commits: send refunds to the vendor and
// raise alerts. If the process dies first, PollPending finds the `requested` refunds.
type effects struct {
	refunds []string
	alerts  []string
}

func (m *Module) flush(ctx context.Context, fx *effects) {
	for _, code := range fx.alerts {
		m.Alert(ctx, code)
	}
	for _, ref := range fx.refunds {
		if err := m.dispatchRefund(ctx, ref); err != nil {
			m.log.Error("payments.refund_dispatch_failed", "op", "dispatch_refund", logging.Err(err))
		}
	}
}

type queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ticket opens a once-only ticket and raises an alert the first time.
func (m *Module) ticket(ctx context.Context, tx pgx.Tx, fx *effects, key, kind, clinicRef, subject, alertCode string) error {
	now := m.now()
	tag, err := tx.Exec(ctx, `INSERT INTO ops_ticket (ticket_ref, ticket_key, kind, clinic_ref, subject_ref, created_at)
		VALUES ($1,$2,$3,NULLIF($4,'')::uuid,$5,$6) ON CONFLICT (ticket_key) DO NOTHING`, ids.New(), key, kind, clinicRef, subject, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO payment_alert (alert_id, code, clinic_ref, subject_ref, created_at) VALUES ($1,$2,NULLIF($3,'')::uuid,$4,$5)`,
		ids.New(), alertCode, clinicRef, subject, now); err != nil {
		return err
	}
	fx.alerts = append(fx.alerts, alertCode)
	return nil
}

// alertOnly records an alert without a ticket.
func (m *Module) alertOnly(ctx context.Context, tx pgx.Tx, fx *effects, code, clinicRef, subject string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO payment_alert (alert_id, code, clinic_ref, subject_ref, created_at) VALUES ($1,$2,NULLIF($3,'')::uuid,$4,$5)`,
		ids.New(), code, clinicRef, subject, m.now()); err != nil {
		return err
	}
	fx.alerts = append(fx.alerts, code)
	return nil
}

var reVendorID = regexp.MustCompile(`^[A-Za-z0-9_]{1,60}$`)

func (m *Module) policyFor(version string) (*refundpolicy.Policy, error) {
	if version == m.cfg.Policy.Version {
		return m.cfg.Policy, nil
	}
	if m.cfg.PolicyByVersion != nil {
		if p, ok := m.cfg.PolicyByVersion(version); ok && p.Version == version {
			return p, nil
		}
	}
	return nil, fmt.Errorf("%w: refund policy version %q is not loaded", rp.ErrInvalid, version)
}
