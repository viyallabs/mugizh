package identity

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

func pushAAD(deviceID string) crypto.AAD {
	return crypto.AAD{Table: "identity.device", Column: "push_token_ct", RowID: deviceID}
}

// PhoneFor returns a user's phone number for sending a message (notifications only).
// The number is decrypted in memory and never logged (K-DP-04).
func (m *Module) PhoneFor(ctx context.Context, userID string) (string, error) {
	p, err := m.phoneOf(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return p, err
}

// PushTarget is one device that can receive push notifications.
type PushTarget struct {
	DeviceID string
	Platform string
	Token    string
}

// PushTargets lists the active, verified devices of a user with a push token, of one app ("patient" or "pro"; ""
// for every app: a patient message never reaches the same person's pro-app device, D-73). Devices waiting for
// re-verification are left out: a recycled number's new owner must not receive the old owner's reminders (E-DP-22).
func (m *Module) PushTargets(ctx context.Context, userID, app string) ([]PushTarget, error) {
	if app != "" && app != string(ip.AppPatient) && app != string(ip.AppPro) {
		return nil, nil
	}
	rows, err := m.pool.Query(ctx, `SELECT id, platform, push_token_ct FROM device
		WHERE user_id=$1 AND status='active' AND push_token_ct IS NOT NULL AND ($2 = '' OR app = $2) ORDER BY last_seen_at DESC`, userID, app)
	if err != nil {
		return nil, err
	}
	type row struct {
		id, platform string
		ct           crypto.Sealed[crypto.PatientKey]
	}
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.platform, &r.ct); err != nil {
			rows.Close()
			return nil, err
		}
		rs = append(rs, r)
	}
	rows.Close()
	out := make([]PushTarget, 0, len(rs))
	for _, r := range rs {
		tok, err := crypto.Open(ctx, m.cr, pushAAD(r.id), r.ct)
		if err != nil {
			return nil, err
		}
		out = append(out, PushTarget{DeviceID: r.id, Platform: r.platform, Token: string(tok)})
	}
	return out, nil
}

// ErrPushToken is returned for a malformed push token.
var ErrPushToken = problem.New(400, "identity.push_token_invalid", "The push token is not valid")

// SetPushToken stores (or replaces) the push token of the caller's current device.
func (m *Module) SetPushToken(ctx context.Context, p *ip.Principal, token string) error {
	if len(token) < 20 || len(token) > 4096 {
		return ErrPushToken
	}
	ct, err := crypto.SealPatient(ctx, m.cr, p.UserID, pushAAD(p.DeviceID), []byte(token))
	if err != nil {
		return err
	}
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE device SET push_token_ct=$3, push_token_updated_at=now() WHERE id=$1 AND user_id=$2 AND status <> 'revoked'`,
			p.DeviceID, p.UserID, ct.Bytes())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: p.UserID, ActorRole: string(p.App), Action: "identity.push_token_set", SubjectRef: p.UserID,
			ResourceType: "device", ResourceRef: p.DeviceID, Result: ap.Allowed})
	})
}

// RevokedPushTarget returns the push target of one of the user's devices that was signed out remotely in the last
// 24 hours, so it can be told to wipe itself (S-165). ok is false for an active device, someone else's device, an
// older revocation or a device without a token.
func (m *Module) RevokedPushTarget(ctx context.Context, userID, deviceID string) (PushTarget, bool, error) {
	if !ids.Valid(userID) || !ids.Valid(deviceID) {
		return PushTarget{}, false, nil
	}
	var platform string
	var ct crypto.Sealed[crypto.PatientKey]
	err := m.pool.QueryRow(ctx, `SELECT platform, push_token_ct FROM device WHERE id=$1 AND user_id=$2 AND status='revoked'
		AND push_token_ct IS NOT NULL AND revoked_at > now() - interval '24 hours'`, deviceID, userID).Scan(&platform, &ct)
	if errors.Is(err, pgx.ErrNoRows) {
		return PushTarget{}, false, nil
	}
	if err != nil {
		return PushTarget{}, false, err
	}
	tok, err := crypto.Open(ctx, m.cr, pushAAD(deviceID), ct)
	if err != nil {
		return PushTarget{}, false, err
	}
	return PushTarget{DeviceID: deviceID, Platform: platform, Token: string(tok)}, true, nil
}

// InvalidatePushToken removes a token the push provider reported as dead (adapter
// callback "invalidToken").
func (m *Module) InvalidatePushToken(ctx context.Context, deviceID string) error {
	_, err := m.pool.Exec(ctx, `UPDATE device SET push_token_ct=NULL, push_token_updated_at=now() WHERE id=$1`, deviceID)
	return err
}

// HasRole reports whether a user holds an active role (any clinic scope). Used by records
// to verify staff and approver roles for just-in-time grants (review M5).
func (m *Module) HasRole(ctx context.Context, userID, role string) (bool, error) {
	var ok bool
	err := m.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM role_assignment WHERE user_id=$1 AND role=$2 AND revoked_at IS NULL)`, userID, role).Scan(&ok)
	return ok, err
}
