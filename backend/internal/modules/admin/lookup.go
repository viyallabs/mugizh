package admin

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	pub "gitlab.com/viyallabs/mugizh/backend/internal/modules/admin/public"
	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// LookupAccount finds an account by phone number for support triage (S-172). Only support,
// ops and the Grievance Officer may look up, always for a ticket; the answer carries no name,
// date of birth, contact detail or health data (identity's AccountSummary). Every lookup is
// recorded and audited, found or not, and a staff member is limited per hour (E-DP-16): the
// limit refuses further lookups, audits the refusal and logs an alert for the security owner.
// The count, the lookup and the record run under one per-staff lock, so parallel requests
// cannot pass the limit (review F7). Reading the patient's bookings or payments still needs a
// just-in-time grant (K-SEC-08); clinical data is never available to support
// (access-control-design §9.3, enforced by the records gate).
func (m *Module) LookupAccount(ctx context.Context, s pub.Staff, phone, ticketRef string) (ip.AccountSummary, error) {
	if err := m.holds(ctx, s, "support", "ops", "grievance_officer"); err != nil {
		return ip.AccountSummary{}, err
	}
	if !reTicket.MatchString(ticketRef) || phone == "" || len(phone) > 20 {
		return ip.AccountSummary{}, pub.ErrInvalid
	}
	if m.d.Accounts == nil {
		return ip.AccountSummary{}, pub.ErrUnavailable
	}
	now := m.now().UTC()
	var sum ip.AccountSummary
	var found, limited bool
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		found, limited, sum = false, false, ip.AccountSummary{}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 7))`, s.Ref); err != nil {
			return err
		}
		var recent int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM account_lookup WHERE staff_ref=$1 AND looked_up_at > $2`, s.Ref, now.Add(-time.Hour)).Scan(&recent); err != nil {
			return err
		}
		if recent >= m.cfg.LookupsPerHour {
			limited = true
			return m.auditStaff(ctx, tx, s, "admin.account_lookup", "", "account", "", "rate_limited", ticketRef, "", ap.Denied)
		}
		got, lerr := m.d.Accounts.LookupByPhone(ctx, phone)
		if lerr != nil && !errors.Is(lerr, ip.ErrAccountNotFound) {
			return lerr
		}
		found, sum = lerr == nil, got
		if _, err := tx.Exec(ctx, `INSERT INTO account_lookup (id, staff_ref, ticket_ref, found, looked_up_at) VALUES ($1,$2,$3,$4,$5)`,
			ids.New(), s.Ref, ticketRef, found, now); err != nil {
			return err
		}
		res, reason, subject := ap.Allowed, "", ""
		if found {
			subject = sum.UserID
		} else {
			res, reason = ap.Denied, "not_found"
		}
		return m.auditStaff(ctx, tx, s, "admin.account_lookup", subject, "account", subject, reason, ticketRef, "", res)
	})
	switch {
	case err != nil:
		return ip.AccountSummary{}, err
	case limited:
		m.log.Warn("admin.lookup_rate_exceeded", "actor_ref", s.Ref)
		return ip.AccountSummary{}, pub.ErrRateLimited
	case !found:
		return ip.AccountSummary{}, pub.ErrNotFound
	}
	return sum, nil
}
