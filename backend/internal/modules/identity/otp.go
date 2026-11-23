package identity

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"time"

	"crypto/ecdsa"
	"crypto/elliptic"

	"github.com/jackc/pgx/v5"

	"gitlab.com/viyallabs/mugizh/backend/internal/adapters/sms"
	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// OTPRequest asks for a sign-in code.
type OTPRequest struct {
	Phone  string
	App    ip.App
	IPHash string // keyed hash of the client IP (never the IP)
}

// Challenge is returned to the client. The same answer is given whether or not an
// account exists for the phone, so numbers cannot be enumerated.
type Challenge struct {
	ID        string
	ExpiresAt time.Time
}

// RequestOTP creates a challenge and sends SMS-01 (Service: authentication OTP).
func (m *Module) RequestOTP(ctx context.Context, in OTPRequest) (Challenge, error) {
	if in.App != ip.AppPatient && in.App != ip.AppPro {
		return Challenge{}, problem.BadRequest
	}
	phone, err := m.normalisePhone(in.Phone)
	if err != nil {
		return Challenge{}, err
	}
	bidx := m.phoneIndex(phone)
	now := m.now().UTC()
	if until, locked, err := m.lockedUntil(ctx, bidx, []byte(in.IPHash), now); err != nil {
		return Challenge{}, err
	} else if locked {
		return Challenge{}, retryAfter(ErrLocked, until.Sub(now))
	}
	code, err := sixDigits()
	if err != nil {
		return Challenge{}, err
	}
	ch := Challenge{ID: ids.New(), ExpiresAt: now.Add(m.cfg.OTPTTL)}
	ipKey := []byte(in.IPHash)
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := m.countOTPSend(ctx, tx, bidx, ipKey, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO otp_challenge (id, phone_bidx, purpose, app, code_hash, expires_at) VALUES ($1,$2,'sign_in',$3,$4,$5)`,
			ch.ID, bidx, string(in.App), m.codeHash(ch.ID, code), ch.ExpiresAt)
		return err
	})
	if err := m.sendOTP(ctx, err, phone, code, ch.ID); err != nil {
		return Challenge{}, err
	}
	return ch, nil
}

// countOTPSend applies the per-number, per-IP and global send limits (TM-02) to one
// code about to be sent. Sign-in and step-up codes share the same counters.
func (m *Module) countOTPSend(ctx context.Context, tx pgx.Tx, bidx, ipKey []byte, now time.Time) error {
	checks := []struct {
		scope  string
		key    []byte
		window time.Duration
		limit  int
	}{
		{"phone_10m", bidx, 10 * time.Minute, m.cfg.PhonePer10Min},
		{"phone_day", bidx, 24 * time.Hour, m.cfg.PhonePerDay},
		{"ip_10m", ipKey, 10 * time.Minute, m.cfg.IPPer10Min},
		{"ip_day", ipKey, 24 * time.Hour, m.cfg.IPPerDay},
		{"global_day", []byte("all"), 24 * time.Hour, m.cfg.GlobalPerDay},
	}
	for _, c := range checks {
		if len(c.key) == 0 {
			continue
		}
		ws := now.Truncate(c.window)
		var n int
		if err := tx.QueryRow(ctx, `INSERT INTO otp_counter (scope, key, window_start, count) VALUES ($1,$2,$3,1)
			ON CONFLICT (scope, key, window_start) DO UPDATE SET count = otp_counter.count + 1 RETURNING count`,
			c.scope, c.key, ws).Scan(&n); err != nil {
			return err
		}
		if n > c.limit {
			if c.scope == "global_day" {
				return errSMSCap
			}
			return retryAfter(ErrRateLimited, ws.Add(c.window).Sub(now))
		}
	}
	return nil
}

// sendOTP sends SMS-01 for a challenge whose transaction ended with txErr. A failed send
// burns the challenge.
func (m *Module) sendOTP(ctx context.Context, txErr error, phone, code, challengeID string) error {
	if errors.Is(txErr, errSMSCap) {
		m.log.Error("identity.sms_cap_reached")
		m.Alert(ctx, "identity.sms_cap_reached")
		return ErrSMSUnavailable
	}
	if txErr != nil {
		return txErr
	}
	if _, err := m.sms.Send(ctx, sms.Message{To: phone, Template: "SMS-01", Vars: []sms.Var{{Name: "otp", Value: code}}, IdempotencyKey: challengeID}); err != nil {
		m.log.Warn("identity.otp_send_failed", logging.Err(err))
		_, _ = m.pool.Exec(context.WithoutCancel(ctx), `UPDATE otp_challenge SET consumed_at=now() WHERE id=$1`, challengeID)
		return ErrSMSUnavailable
	}
	return nil
}

var errSMSCap = errors.New("identity: daily SMS cap reached")

func retryAfter(p *problem.Problem, d time.Duration) *problem.Problem {
	c := *p
	c.RetryAfter = int(d.Seconds()) + 1
	return &c
}

func sixDigits() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// codeHash is keyed with the service secret, so a database copy alone cannot be used to
// brute-force the six-digit codes offline.
func (m *Module) codeHash(challengeID, code string) []byte {
	return m.cr.Keyed("identity.otp", challengeID+"|"+code)
}

func (m *Module) lockedUntil(ctx context.Context, bidx, ipKey []byte, now time.Time) (time.Time, bool, error) {
	var until time.Time
	err := m.pool.QueryRow(ctx, `SELECT locked_until FROM sign_in_lock WHERE phone_bidx=$1 AND ip_key=$2`, bidx, ipKey).Scan(&until)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return until, until.After(now), nil
}

// DeviceInfo identifies the device signing in. PublicKey is a DER SubjectPublicKeyInfo
// of an ECDSA P-256 key held in the device keystore (K-SEC-03).
type DeviceInfo struct {
	PublicKey []byte
	Platform  string // android, ios, web
	// Proof is the device's ECDSA signature (ASN.1, SHA-256) over SignInMessage(challengeID):
	// it proves the caller holds the private key, so a known public key cannot be replayed
	// to impersonate an already-bound device (review L5).
	Proof []byte
}

// SignInMessage returns the bytes a device signs when completing sign-in.
func SignInMessage(challengeID string) []byte { return []byte("mugizh-signin|v1|" + challengeID) }

// VerifyInput completes sign-in.
type VerifyInput struct {
	ChallengeID string
	Phone       string // the same number the code was sent to (checked against the challenge)
	Code        string
	App         ip.App
	// ProIntent is "doctor" for professional sign-up, or "" (invited clinic staff get
	// their role from the invitation, never from sign-up).
	ProIntent string
	Device    DeviceInfo
	IPHash    string
	Client    string
	RequestID string
}

// Tokens are issued on sign-in and refresh. Only hashes are stored.
type Tokens struct {
	UserID           string
	SessionID        string
	DeviceID         string
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
	Restricted       bool // new device: re-verification needed before health data
	MFARequired      bool // pro session: MFA must be completed
	NewAccount       bool
}

// VerifyOTP checks the code and signs the user in on the presented device.
func (m *Module) VerifyOTP(ctx context.Context, in VerifyInput) (*Tokens, error) {
	if in.App != ip.AppPatient && in.App != ip.AppPro {
		return nil, problem.BadRequest
	}
	if !ids.Valid(in.ChallengeID) || len(in.Code) != 6 {
		return nil, ErrOTPInvalid
	}
	phone, err := m.normalisePhone(in.Phone)
	if err != nil {
		return nil, ErrOTPInvalid
	}
	wantBidx := m.phoneIndex(phone)
	pub, err := parseDeviceKey(in.Device.PublicKey)
	if err != nil {
		return nil, err
	}
	proofDigest := sha256.Sum256(SignInMessage(in.ChallengeID))
	if !ecdsa.VerifyASN1(pub, proofDigest[:], in.Device.Proof) {
		return nil, ErrDeviceKey
	}
	switch in.Device.Platform {
	case "android", "ios", "web":
	default:
		return nil, ErrDeviceKey
	}
	keyHash := sha256.Sum256(in.Device.PublicKey)
	now := m.now().UTC()

	var bidx []byte
	var ok bool
	// Step 1: check the code in its own transaction, so a wrong attempt is counted even
	// though the request fails.
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		ok = false
		var hash []byte
		var attempts int
		var exp time.Time
		var consumed *time.Time
		var app string
		err := tx.QueryRow(ctx, `SELECT phone_bidx, code_hash, attempts, expires_at, consumed_at, app FROM otp_challenge
			WHERE id=$1 AND purpose='sign_in' FOR UPDATE`, in.ChallengeID).Scan(&bidx, &hash, &attempts, &exp, &consumed, &app)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOTPInvalid
		}
		if err != nil {
			return err
		}
		if consumed != nil || !now.Before(exp) || attempts >= m.cfg.OTPMaxAttempts || app != string(in.App) {
			return ErrOTPInvalid
		}
		var until time.Time
		if err := tx.QueryRow(ctx, `SELECT locked_until FROM sign_in_lock WHERE phone_bidx=$1 AND ip_key=$2`, bidx, []byte(in.IPHash)).Scan(&until); err == nil && until.After(now) {
			return retryAfter(ErrLocked, until.Sub(now))
		}
		// A different number than the one the code was sent to counts as a wrong attempt.
		if hmac.Equal(bidx, wantBidx) && hmac.Equal(hash, m.codeHash(in.ChallengeID, in.Code)) {
			ok = true
			_, err := tx.Exec(ctx, `UPDATE otp_challenge SET consumed_at=$2, attempts=attempts+1 WHERE id=$1`, in.ChallengeID, now)
			if err == nil {
				_, err = tx.Exec(ctx, `UPDATE sign_in_lock SET lockouts=0 WHERE phone_bidx=$1 AND ip_key=$2`, bidx, []byte(in.IPHash))
			}
			return err
		}
		return m.wrongAttempt(ctx, tx, in.ChallengeID, attempts+1, bidx, []byte(in.IPHash), now)
	})
	if err != nil {
		return nil, err
	}
	if !ok {
		m.log.Info("identity.otp_wrong")
		return nil, ErrOTPInvalid
	}
	return m.signIn(ctx, phone, bidx, in, keyHash[:], now)
}

// wrongAttempt records a wrong code (attempts is the new count). The challenge is burnt
// at the limit and the number is locked with doubling backoff (TM-01, E-SEC-01).
func (m *Module) wrongAttempt(ctx context.Context, tx pgx.Tx, challengeID string, attempts int, bidx, ipKey []byte, now time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE otp_challenge SET attempts=$2::int, consumed_at=CASE WHEN $2::int >= $3::int THEN $4::timestamptz ELSE NULL END WHERE id=$1`,
		challengeID, attempts, m.cfg.OTPMaxAttempts, now); err != nil {
		return err
	}
	if attempts < m.cfg.OTPMaxAttempts {
		return nil
	}
	var lockouts int
	if err := tx.QueryRow(ctx, `INSERT INTO sign_in_lock (phone_bidx, ip_key, lockouts, locked_until) VALUES ($1, $3, 1, $2)
		ON CONFLICT (phone_bidx, ip_key) DO UPDATE SET lockouts = sign_in_lock.lockouts + 1 RETURNING lockouts`, bidx, now.Add(m.cfg.LockBase), ipKey).Scan(&lockouts); err != nil {
		return err
	}
	d := m.cfg.LockBase << min(lockouts-1, 10)
	if d > m.cfg.LockMax || d <= 0 {
		d = m.cfg.LockMax
	}
	_, err := tx.Exec(ctx, `UPDATE sign_in_lock SET locked_until=$2 WHERE phone_bidx=$1 AND ip_key=$3`, bidx, now.Add(d), ipKey)
	return err
}

func parseDeviceKey(der []byte) (*ecdsa.PublicKey, error) {
	if len(der) < 32 || len(der) > 200 {
		return nil, ErrDeviceKey
	}
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, ErrDeviceKey
	}
	pk, ok := k.(*ecdsa.PublicKey)
	if !ok || pk.Curve != elliptic.P256() {
		return nil, ErrDeviceKey
	}
	return pk, nil
}

// signIn finds or creates the account, binds the device and opens a session.
func (m *Module) signIn(ctx context.Context, phone string, bidx []byte, in VerifyInput, keyHash []byte, now time.Time) (*Tokens, error) {
	// Create the account first if the number is new, with the number field-encrypted
	// (K-DP-03). Encryption happens outside the transaction (separate key store).
	created, err := m.ensureAccount(ctx, phone, bidx, in, now)
	if err != nil {
		return nil, err
	}
	var out *Tokens
	var newDeviceNotice string
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		out = &Tokens{}
		newDeviceNotice = ""
		var userID string
		err := tx.QueryRow(ctx, `SELECT id FROM user_account WHERE phone_bidx=$1 AND status='active' FOR UPDATE`, bidx).Scan(&userID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return ErrOTPInvalid // account closed between the two steps
		case err != nil:
			return err
		}
		out.UserID = userID
		roles, err := userRoles(ctx, tx, userID)
		if err != nil {
			return err
		}
		if hasStaffRole(roles) {
			return ErrStaffSignIn
		}
		// Device binding.
		var devID, devStatus string
		err = tx.QueryRow(ctx, `SELECT id, status FROM device WHERE user_id=$1 AND key_hash=$2 AND app=$3 AND status <> 'revoked'`, userID, keyHash, string(in.App)).Scan(&devID, &devStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			devID = ids.New()
			devStatus = "active"
			if !created {
				// A new device on an existing account cannot act until re-verified: a recycled
				// number must never expose the old owner's records or let the new holder
				// approve their own device (E-DP-22, TM-03). The one exception is a pro
				// device on an account with a confirmed authenticator: possession of that
				// factor (MFA, required on every pro sign-in) is the re-verification.
				pending := true
				if in.App == ip.AppPro {
					var hasTOTP bool
					if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mfa_factor WHERE user_id=$1 AND confirmed_at IS NOT NULL AND disabled_at IS NULL)`,
						userID).Scan(&hasTOTP); err != nil {
						return err
					}
					pending = !hasTOTP
				}
				if pending {
					devStatus = "pending_reverification"
				}
				// Either way the owner is told by SMS-14 (TM-04: a SIM swap or stolen
				// credentials on a doctor's number must not go unnoticed).
				newDeviceNotice = userID
			}
			if _, err := tx.Exec(ctx, `INSERT INTO device (id, user_id, public_key, key_hash, platform, app, status, approved_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7, CASE WHEN $7='active' THEN now() ELSE NULL END)`,
				devID, userID, in.Device.PublicKey, keyHash, in.Device.Platform, string(in.App), devStatus); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		out.DeviceID = devID
		out.Restricted = devStatus == "pending_reverification"
		// Roles are added only from a verified device; a restricted sign-in changes nothing
		// on the account (it cannot, for example, give an old patient account a doctor role).
		if !out.Restricted {
			if err := m.ensureAppRole(ctx, tx, userID, roles, in); err != nil {
				return err
			}
		}
		if err := m.openSession(ctx, tx, out, in.App, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE user_account SET last_seen_at=$2 WHERE id=$1`, userID, now); err != nil {
			return err
		}
		action := "identity.sign_in"
		if out.Restricted {
			action = "identity.sign_in_new_device"
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: userID, ActorRole: string(in.App), Action: action, SubjectRef: userID,
			ResourceType: "device", ResourceRef: devID, Result: ap.Allowed, Client: in.Client, IPHash: in.IPHash, RequestID: in.RequestID})
	})
	if err != nil {
		return nil, err
	}
	out.NewAccount = created
	if newDeviceNotice != "" {
		m.notifyNewDevice(ctx, newDeviceNotice, now)
	}
	return out, nil
}

func hasStaffRole(roles []ip.RoleGrant) bool {
	for _, r := range roles {
		for _, s := range ip.StaffRoles {
			if r.Role == s {
				return true
			}
		}
	}
	return false
}

// ensureAppRole gives a patient-app user the patient role, and a pro-app user who signs
// up as a doctor the (unverified) doctor role. Verification gates everything clinical
// (K-CS-01); invited clinic staff get roles only from their invitation.
func (m *Module) ensureAppRole(ctx context.Context, tx pgx.Tx, userID string, roles []ip.RoleGrant, in VerifyInput) error {
	want := ""
	switch {
	case in.App == ip.AppPatient:
		want = ip.RolePatient
	case in.App == ip.AppPro && in.ProIntent == "doctor":
		want = ip.RoleDoctor
	}
	if want == "" {
		return nil
	}
	for _, r := range roles {
		if r.Role == want {
			return nil
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO role_assignment (id, user_id, role) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, ids.New(), userID, want); err != nil {
		return err
	}
	return ap.Record(ctx, tx, ap.Event{ActorRef: userID, ActorRole: "system", Action: "identity.role_granted", SubjectRef: userID,
		ResourceType: "role", Purpose: want, Result: ap.Allowed, RequestID: in.RequestID})
}

func userRoles(ctx context.Context, tx pgx.Tx, userID string) ([]ip.RoleGrant, error) {
	rows, err := tx.Query(ctx, `SELECT role, coalesce(clinic_ref::text,'') FROM role_assignment WHERE user_id=$1 AND revoked_at IS NULL ORDER BY role`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ip.RoleGrant
	for rows.Next() {
		var g ip.RoleGrant
		if err := rows.Scan(&g.Role, &g.ClinicRef); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (m *Module) notifyNewDevice(ctx context.Context, userID string, now time.Time) {
	phone, err := m.phoneOf(ctx, userID)
	if err != nil {
		m.log.Warn("identity.new_device_notice_failed", logging.Err(err))
		return
	}
	// SMS-14 security notice. The date is in UTC ISO form here; the notifications module
	// will localise it once it exists (K-GLB-02).
	if _, err := m.sms.Send(ctx, sms.Message{To: phone, Template: "SMS-14", Vars: []sms.Var{{Name: "date_time", Value: now.Format("2006-01-02 15:04 UTC")}},
		IdempotencyKey: "sms14:" + userID + ":" + now.Format(time.RFC3339)}); err != nil {
		m.log.Warn("identity.new_device_notice_failed", logging.Err(err))
	}
}

func (m *Module) phoneOf(ctx context.Context, userID string) (string, error) {
	var ct crypto.Sealed[crypto.PatientKey]
	if err := m.pool.QueryRow(ctx, `SELECT phone_ct FROM user_account WHERE id=$1 AND status='active'`, userID).Scan(&ct); err != nil {
		return "", err
	}
	b, err := crypto.Open(ctx, m.cr, phoneAAD(userID), ct)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ensureAccount creates an account for a new number. It reports whether it created one.
func (m *Module) ensureAccount(ctx context.Context, phone string, bidx []byte, in VerifyInput, now time.Time) (bool, error) {
	var exists bool
	if err := m.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_account WHERE phone_bidx=$1 AND status='active')`, bidx).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	userID := ids.New()
	ct, err := crypto.SealPatient(ctx, m.cr, userID, phoneAAD(userID), []byte(phone))
	if err != nil {
		return false, err // fail closed: no account without an encrypted number
	}
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO user_account (id, phone_ct, phone_bidx, last_seen_at) VALUES ($1,$2,$3,$4)`, userID, ct.Bytes(), bidx, now); err != nil {
			return err
		}
		if err := ap.Record(ctx, tx, ap.Event{ActorRef: userID, ActorRole: string(in.App), Action: "identity.account_created", SubjectRef: userID,
			ResourceType: "user_account", ResourceRef: userID, Result: ap.Allowed, Client: in.Client, IPHash: in.IPHash, RequestID: in.RequestID}); err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, outbox.Event{Type: "account.created", SubjectRef: userID})
	})
	if db.IsUniqueViolation(err, "user_account_phone_active") {
		return false, nil // created concurrently by the same person's other request
	}
	return err == nil, err
}
