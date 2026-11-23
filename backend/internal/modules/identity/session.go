package identity

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

const (
	accessPrefix  = "at_"
	refreshPrefix = "rt_"
)

func tokenHash(tok string) []byte { h := sha256.Sum256([]byte(tok)); return h[:] }

func newToken(prefix string) (string, error) {
	t, err := ids.Token(32)
	if err != nil {
		return "", err
	}
	return prefix + t, nil
}

func (m *Module) expiries(app ip.App, now time.Time) (refresh, absolute time.Time) {
	if app == ip.AppPro {
		return now.Add(m.cfg.ProAbsolute), now.Add(m.cfg.ProAbsolute)
	}
	return now.Add(m.cfg.PatientRefreshTTL), now.Add(m.cfg.PatientAbsoluteTTL)
}

// openSession issues a new access and refresh token pair for out.UserID/out.DeviceID.
func (m *Module) openSession(ctx context.Context, tx pgx.Tx, out *Tokens, app ip.App, now time.Time) error {
	at, err := newToken(accessPrefix)
	if err != nil {
		return err
	}
	rt, err := newToken(refreshPrefix)
	if err != nil {
		return err
	}
	refreshExp, absExp := m.expiries(app, now)
	out.SessionID = ids.New()
	out.AccessToken, out.RefreshToken = at, rt
	out.AccessExpiresAt, out.RefreshExpiresAt = now.Add(m.cfg.AccessTTL), refreshExp
	out.MFARequired = app == ip.AppPro
	_, err = tx.Exec(ctx, `INSERT INTO session (id, user_id, device_id, app, access_hash, access_expires_at, refresh_hash, refresh_expires_at,
		absolute_expires_at, restricted, created_at, last_used_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)`,
		out.SessionID, out.UserID, out.DeviceID, string(app), tokenHash(at), out.AccessExpiresAt, tokenHash(rt), refreshExp, absExp, out.Restricted, now)
	return err
}

// Authenticate resolves a bearer access token (ip.Authenticator). Expired, revoked or
// idle sessions, revoked devices and closed accounts are all rejected.
func (m *Module) Authenticate(ctx context.Context, bearer string) (*ip.Principal, error) {
	if !strings.HasPrefix(bearer, accessPrefix) || len(bearer) != len(accessPrefix)+64 {
		return nil, ErrTokenInvalid
	}
	now := m.now().UTC()
	p := &ip.Principal{}
	var app string
	var accessExp, absExp, lastUsed time.Time
	var revoked *time.Time
	var devStatus, userStatus string
	err := m.pool.QueryRow(ctx, `SELECT s.id, s.user_id, s.device_id, s.app, s.access_expires_at, s.absolute_expires_at, s.last_used_at,
			s.revoked_at, s.mfa_at, s.step_up_at, s.restricted, d.status, u.status
		FROM session s JOIN device d ON d.id = s.device_id JOIN user_account u ON u.id = s.user_id
		WHERE s.access_hash = $1`, tokenHash(bearer)).Scan(&p.SessionID, &p.UserID, &p.DeviceID, &app, &accessExp, &absExp, &lastUsed,
		&revoked, &p.MFAAt, &p.StepUpAt, &p.Restricted, &devStatus, &userStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	p.App = ip.App(app)
	if revoked != nil || !now.Before(accessExp) || !now.Before(absExp) || devStatus == "revoked" || userStatus != "active" {
		return nil, ErrTokenInvalid
	}
	if p.App == ip.AppPro && now.Sub(lastUsed) > m.cfg.ProIdle {
		_ = m.revokeSession(ctx, p.SessionID, "idle_timeout")
		return nil, ErrTokenInvalid
	}
	if p.Restricted && devStatus == "active" {
		p.Restricted = false // device approved since the session started
	}
	err = db.InTx(ctx, m.pool, pgx.TxOptions{AccessMode: pgx.ReadWrite}, func(tx pgx.Tx) error {
		roles, err := userRoles(ctx, tx, p.UserID)
		p.Roles = roles
		if err != nil {
			return err
		}
		if now.Sub(lastUsed) > time.Minute {
			_, err = tx.Exec(ctx, `UPDATE session SET last_used_at=$2 WHERE id=$1`, p.SessionID, now)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

// RefreshInput rotates the token pair. The device proves possession of its bound key by
// signing "mugizh-refresh|v1|<hex sha256(refresh token)>|<unix seconds>" (ECDSA P-256,
// SHA-256, ASN.1 signature).
type RefreshInput struct {
	RefreshToken string
	Timestamp    int64
	Signature    []byte
}

// RefreshMessage returns the bytes a device signs for a refresh.
func RefreshMessage(refreshToken string, ts int64) []byte {
	return []byte(fmt.Sprintf("mugizh-refresh|v1|%s|%d", hex.EncodeToString(tokenHash(refreshToken)), ts))
}

// Refresh rotates tokens. Re-use of an old refresh token revokes the whole session
// (stolen-token detection).
func (m *Module) Refresh(ctx context.Context, in RefreshInput) (*Tokens, error) {
	if !strings.HasPrefix(in.RefreshToken, refreshPrefix) || len(in.RefreshToken) != len(refreshPrefix)+64 {
		return nil, ErrTokenInvalid
	}
	now := m.now().UTC()
	h := tokenHash(in.RefreshToken)
	var out *Tokens
	var reused string
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		out, reused = nil, ""
		var sid, uid, did, app string
		var refreshExp, absExp, lastUsed time.Time
		var revoked *time.Time
		var lastTS int64
		var pubDER []byte
		var restricted bool
		var devStatus string
		err := tx.QueryRow(ctx, `SELECT s.id, s.user_id, s.device_id, s.app, s.refresh_expires_at, s.absolute_expires_at, s.last_used_at,
				s.revoked_at, s.last_proof_ts, s.restricted, d.public_key, d.status
			FROM session s JOIN device d ON d.id = s.device_id JOIN user_account u ON u.id = s.user_id
			WHERE s.refresh_hash=$1 AND u.status='active' FOR UPDATE OF s`, h).Scan(
			&sid, &uid, &did, &app, &refreshExp, &absExp, &lastUsed, &revoked, &lastTS, &restricted, &pubDER, &devStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.QueryRow(ctx, `SELECT id FROM session WHERE prev_refresh_hash=$1 AND revoked_at IS NULL`, h).Scan(&reused); err == nil {
				return nil
			}
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		if revoked != nil || !now.Before(refreshExp) || !now.Before(absExp) || devStatus == "revoked" {
			return ErrTokenInvalid
		}
		if ip.App(app) == ip.AppPro && now.Sub(lastUsed) > m.cfg.ProIdle {
			return ErrTokenInvalid
		}
		pub, err := parseDeviceKey(pubDER)
		if err != nil {
			return ErrTokenInvalid
		}
		skew := m.cfg.ProofSkew
		ts := time.Unix(in.Timestamp, 0)
		if ts.Before(now.Add(-skew)) || ts.After(now.Add(skew)) || in.Timestamp <= lastTS {
			return ErrTokenInvalid
		}
		digest := sha256.Sum256(RefreshMessage(in.RefreshToken, in.Timestamp))
		if !ecdsa.VerifyASN1(pub, digest[:], in.Signature) {
			return ErrTokenInvalid
		}
		at, err := newToken(accessPrefix)
		if err != nil {
			return err
		}
		rt, err := newToken(refreshPrefix)
		if err != nil {
			return err
		}
		newRefreshExp := refreshExp
		if ip.App(app) == ip.AppPatient {
			newRefreshExp = now.Add(m.cfg.PatientRefreshTTL)
			if newRefreshExp.After(absExp) {
				newRefreshExp = absExp
			}
		}
		out = &Tokens{UserID: uid, SessionID: sid, DeviceID: did, AccessToken: at, AccessExpiresAt: now.Add(m.cfg.AccessTTL),
			RefreshToken: rt, RefreshExpiresAt: newRefreshExp, Restricted: restricted && devStatus != "active"}
		_, err = tx.Exec(ctx, `UPDATE session SET access_hash=$2, access_expires_at=$3, refresh_hash=$4, prev_refresh_hash=$5,
			refresh_expires_at=$6, last_proof_ts=$7, last_used_at=$8, restricted=$9 WHERE id=$1`,
			sid, tokenHash(at), out.AccessExpiresAt, tokenHash(rt), h, newRefreshExp, in.Timestamp, now, out.Restricted)
		return err
	})
	if reused != "" {
		_ = m.revokeSession(ctx, reused, "refresh_reuse")
		m.log.Warn("identity.refresh_reuse_detected")
		m.Alert(ctx, "identity.refresh_reuse")
		return nil, ErrTokenInvalid
	}
	return out, err
}

func (m *Module) revokeSession(ctx context.Context, sessionID, reason string) error {
	_, err := m.pool.Exec(ctx, `UPDATE session SET revoked_at=now(), revoke_reason=$2 WHERE id=$1 AND revoked_at IS NULL`, sessionID, reason)
	return err
}

// Logout ends the caller's session.
func (m *Module) Logout(ctx context.Context, p *ip.Principal) error {
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE session SET revoked_at=now(), revoke_reason='logout' WHERE id=$1 AND revoked_at IS NULL`, p.SessionID); err != nil {
			return err
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: p.UserID, ActorRole: string(p.App), Action: "identity.sign_out", SubjectRef: p.UserID,
			ResourceType: "session", ResourceRef: p.SessionID, Result: ap.Allowed})
	})
}

// Device is a bound device as shown to its owner.
type Device struct {
	ID         string
	Platform   string
	App        string
	Status     string
	BoundAt    time.Time
	LastSeenAt time.Time
	Current    bool
}

// Devices lists the caller's devices (S-115).
func (m *Module) Devices(ctx context.Context, p *ip.Principal) ([]Device, error) {
	rows, err := m.pool.Query(ctx, `SELECT id, platform, app, status, bound_at, last_seen_at FROM device WHERE user_id=$1 AND status <> 'revoked' ORDER BY bound_at`, p.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.Platform, &d.App, &d.Status, &d.BoundAt, &d.LastSeenAt); err != nil {
			return nil, err
		}
		d.Current = d.ID == p.DeviceID
		out = append(out, d)
	}
	return out, rows.Err()
}

// RevokeDevice signs a lost device out (S-115, TC-07): the device and all its sessions
// are revoked at once; the next request with its tokens fails. Another user's device
// answers "not found" (K-SEC-04).
func (m *Module) RevokeDevice(ctx context.Context, p *ip.Principal, deviceID string) error {
	if !ids.Valid(deviceID) {
		return ErrNotFound
	}
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE device SET status='revoked', revoked_at=now() WHERE id=$1 AND user_id=$2 AND status <> 'revoked'`, deviceID, p.UserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `UPDATE session SET revoked_at=now(), revoke_reason='device_revoked' WHERE device_id=$1 AND revoked_at IS NULL`, deviceID); err != nil {
			return err
		}
		if err := outbox.Emit(ctx, tx, outbox.Event{Type: ip.EventDeviceRevoked, SubjectRef: p.UserID, Payload: map[string]string{"deviceRef": deviceID}}); err != nil {
			return err
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: p.UserID, ActorRole: string(p.App), Action: "identity.device_revoked", SubjectRef: p.UserID,
			ResourceType: "device", ResourceRef: deviceID, Result: ap.Allowed})
	})
}

// ProblemApproveFromRestricted is returned when a not-yet-verified device tries to approve.
var ProblemApproveFromRestricted = problem.New(403, "identity.approve_needs_verified_device", "Approve from a device that is already verified")

// ApproveDevice re-verifies a pending device from another, already verified device of
// the same account (E-DP-22). The owner of a recycled number has no such device, so the
// old owner's data stays out of reach; they use account recovery (S-115) instead.
func (m *Module) ApproveDevice(ctx context.Context, p *ip.Principal, deviceID string) error {
	if !ids.Valid(deviceID) {
		return ErrNotFound
	}
	// The approver must be a verified device; a pro session must also have completed MFA.
	if p.Restricted || p.DeviceID == deviceID || (p.App == ip.AppPro && p.MFAAt == nil) {
		return ProblemApproveFromRestricted
	}
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var approverStatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM device WHERE id=$1 AND user_id=$2`, p.DeviceID, p.UserID).Scan(&approverStatus); err != nil || approverStatus != "active" {
			return ProblemApproveFromRestricted
		}
		tag, err := tx.Exec(ctx, `UPDATE device SET status='active', approved_at=now() WHERE id=$1 AND user_id=$2 AND status='pending_reverification'`, deviceID, p.UserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `UPDATE session SET restricted=false WHERE device_id=$1 AND revoked_at IS NULL`, deviceID); err != nil {
			return err
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: p.UserID, ActorRole: string(p.App), Action: "identity.device_approved", SubjectRef: p.UserID,
			ResourceType: "device", ResourceRef: deviceID, Result: ap.Allowed})
	})
}

// AssignRole grants a scoped role (ip.RoleAssigner). Staff roles and the reserved nurse
// role cannot be granted through this path.
func (m *Module) AssignRole(ctx context.Context, userID, role, clinicRef, byRef string) error {
	switch role {
	case ip.RoleDoctor, ip.RoleClinicAdmin, ip.RoleFrontDesk, ip.RolePatient:
	default:
		return problem.Forbidden
	}
	if !ids.Valid(userID) || !ids.Valid(byRef) || (clinicRef != "" && !ids.Valid(clinicRef)) {
		return problem.BadRequest
	}
	if (role == ip.RoleClinicAdmin || role == ip.RoleFrontDesk) && clinicRef == "" {
		return problem.BadRequest
	}
	var clinic any
	if clinicRef != "" {
		clinic = clinicRef
	}
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO role_assignment (id, user_id, role, clinic_ref, granted_by) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
			ids.New(), userID, role, clinic, byRef); err != nil {
			return err
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: byRef, ActorRole: "system", Action: "identity.role_granted", SubjectRef: userID,
			ResourceType: "role", ResourceRef: clinicRef, Purpose: role, Result: ap.Allowed})
	})
}

// RevokeRole removes a scoped role.
func (m *Module) RevokeRole(ctx context.Context, userID, role, clinicRef, byRef string) error {
	if !ids.Valid(userID) || !ids.Valid(byRef) {
		return problem.BadRequest
	}
	return db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE role_assignment SET revoked_at=now() WHERE user_id=$1 AND role=$2 AND coalesce(clinic_ref::text,'')=$3 AND revoked_at IS NULL`,
			userID, role, clinicRef); err != nil {
			return err
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: byRef, ActorRole: "system", Action: "identity.role_revoked", SubjectRef: userID,
			ResourceType: "role", ResourceRef: clinicRef, Purpose: role, Result: ap.Allowed})
	})
}

var (
	_ ip.Authenticator = (*Module)(nil)
	_ ip.RoleAssigner  = (*Module)(nil)
)
