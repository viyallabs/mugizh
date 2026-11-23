package identity

import (
	"context"
	"time"
)

// PurgeResult counts removed rows.
type PurgeResult struct {
	Challenges, Counters, Locks, Sessions, Factors, RevokedTokens int64
}

// PurgeExpired removes short-lived technical data (RET-12, review L9): OTP challenges and
// counters past their window, expired locks, sessions ended more than 30 days ago (kept
// briefly for incident investigation), abandoned unconfirmed MFA set-ups, and the push token of a device signed
// out remotely once its 24-hour wipe window (D-69) is over: it has no further use.
func (m *Module) PurgeExpired(ctx context.Context, now time.Time) (PurgeResult, error) {
	var r PurgeResult
	steps := []struct {
		n   *int64
		sql string
		arg time.Time
	}{
		{&r.Challenges, `DELETE FROM otp_challenge WHERE expires_at < $1`, now.Add(-24 * time.Hour)},
		{&r.Counters, `DELETE FROM otp_counter WHERE window_start < $1`, now.Add(-48 * time.Hour)},
		{&r.Locks, `DELETE FROM sign_in_lock WHERE locked_until < $1`, now.Add(-24 * time.Hour)},
		{&r.Sessions, `DELETE FROM session WHERE (revoked_at IS NOT NULL AND revoked_at < $1) OR absolute_expires_at < $1`, now.Add(-30 * 24 * time.Hour)},
		{&r.Factors, `DELETE FROM mfa_factor WHERE confirmed_at IS NULL AND created_at < $1`, now.Add(-24 * time.Hour)},
		{&r.RevokedTokens, `UPDATE device SET push_token_ct=NULL, push_token_updated_at=NULL WHERE status='revoked' AND push_token_ct IS NOT NULL AND revoked_at < $1`,
			now.Add(-24 * time.Hour)},
	}
	for _, s := range steps {
		tag, err := m.pool.Exec(ctx, s.sql, s.arg)
		if err != nil {
			return r, err
		}
		*s.n = tag.RowsAffected()
	}
	return r, nil
}
