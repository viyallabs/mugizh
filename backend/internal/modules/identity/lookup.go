package identity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
)

// LookupByPhone implements ip.AccountLookup for the support lookup (S-172). It answers only
// for an active account and returns no name, date of birth, contact detail or health data:
// the opaque account reference (so support can raise a just-in-time request), the account's
// age, its roles and how many active devices it has. The caller audits the lookup.
func (m *Module) LookupByPhone(ctx context.Context, raw string) (ip.AccountSummary, error) {
	phone, err := m.normalisePhone(raw)
	if err != nil {
		return ip.AccountSummary{}, ip.ErrAccountNotFound
	}
	var s ip.AccountSummary
	var created time.Time
	err = m.pool.QueryRow(ctx, `SELECT u.id::text, u.created_at,
		  COALESCE(ARRAY(SELECT r.role FROM role_assignment r WHERE r.user_id=u.id AND r.revoked_at IS NULL ORDER BY r.role), '{}'),
		  (SELECT count(*) FROM device d WHERE d.user_id=u.id AND d.status='active')
		FROM user_account u WHERE u.phone_bidx=$1 AND u.status='active'`, m.phoneIndex(phone)).Scan(&s.UserID, &created, &s.Roles, &s.ActiveDevices)
	if errors.Is(err, pgx.ErrNoRows) {
		return ip.AccountSummary{}, ip.ErrAccountNotFound
	}
	if err != nil {
		return ip.AccountSummary{}, err
	}
	s.CreatedOn = created.UTC().Format(time.DateOnly)
	return s, nil
}

var _ ip.AccountLookup = (*Module)(nil)
