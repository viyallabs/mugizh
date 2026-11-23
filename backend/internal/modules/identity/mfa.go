package identity

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/crypto"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
)

// TOTP parameters (RFC 6238 defaults; accepted by common authenticator apps).
const (
	totpPeriod = 30
	totpSkew   = 1 // accept the previous and next 30-second step
)

var totpOpts = totp.ValidateOpts{Period: totpPeriod, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}

func mfaAAD(factorID string) crypto.AAD {
	return crypto.AAD{Table: "identity.mfa_factor", Column: "secret_ct", RowID: factorID}
}

// Enrolment is returned once, when a professional sets up an authenticator app.
type Enrolment struct {
	FactorID string
	Secret   string // base32; shown once, never logged
	URI      string // otpauth:// URI for a QR code
}

// EnrolTOTP starts authenticator set-up for a professional (K-SEC-03). The secret is
// field-encrypted with the person's key (K-DP-03 for credentials, data inventory PER-K).
func (m *Module) EnrolTOTP(ctx context.Context, p *ip.Principal) (*Enrolment, error) {
	if p.App != ip.AppPro {
		return nil, ip.ProblemWrongApp
	}
	var confirmed bool
	if err := m.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mfa_factor WHERE user_id=$1 AND confirmed_at IS NOT NULL AND disabled_at IS NULL)`, p.UserID).Scan(&confirmed); err != nil {
		return nil, err
	}
	if confirmed {
		return nil, ErrMFAState // replacing a factor needs a separate, step-up protected flow
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: m.cfg.TOTPIssuer, AccountName: "account", Period: totpPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		return nil, err
	}
	id := ids.New()
	ct, err := crypto.SealPatient(ctx, m.cr, p.UserID, mfaAAD(id), []byte(key.Secret()))
	if err != nil {
		return nil, err
	}
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Drop earlier unconfirmed attempts.
		if _, err := tx.Exec(ctx, `UPDATE mfa_factor SET disabled_at=now() WHERE user_id=$1 AND confirmed_at IS NULL AND disabled_at IS NULL`, p.UserID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO mfa_factor (id, user_id, type, secret_ct) VALUES ($1,$2,'totp',$3)`, id, p.UserID, ct.Bytes())
		return err
	})
	if err != nil {
		return nil, err
	}
	return &Enrolment{FactorID: id, Secret: key.Secret(), URI: key.URL()}, nil
}

// ConfirmTOTP finishes set-up with a first code and completes MFA for this session.
func (m *Module) ConfirmTOTP(ctx context.Context, p *ip.Principal, code string) error {
	return m.checkTOTP(ctx, p, code, "confirm")
}

// VerifyMFA completes MFA for this session (every new professional sign-in).
func (m *Module) VerifyMFA(ctx context.Context, p *ip.Principal, code string) error {
	return m.checkTOTP(ctx, p, code, "verify")
}

// StepUp re-authenticates a professional with a fresh code before signing (P08) or
// other sensitive actions. It requires a session that already completed MFA.
func (m *Module) StepUp(ctx context.Context, p *ip.Principal, code string) error {
	if p.MFAAt == nil {
		return ErrMFAState
	}
	return m.checkTOTP(ctx, p, code, "step_up")
}

func (m *Module) checkTOTP(ctx context.Context, p *ip.Principal, code, mode string) error {
	if p.App != ip.AppPro {
		return ip.ProblemWrongApp
	}
	if len(code) != 6 {
		return ErrMFAInvalid
	}
	now := m.now().UTC()
	var failed, locked bool
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		failed, locked = false, false
		q := `SELECT id, secret_ct, last_step FROM mfa_factor WHERE user_id=$1 AND type='totp' AND disabled_at IS NULL AND confirmed_at IS NOT NULL FOR UPDATE`
		if mode == "confirm" {
			q = `SELECT id, secret_ct, last_step FROM mfa_factor WHERE user_id=$1 AND type='totp' AND disabled_at IS NULL AND confirmed_at IS NULL
				ORDER BY created_at DESC LIMIT 1 FOR UPDATE`
		}
		var fid string
		var ct crypto.Sealed[crypto.PatientKey]
		var lastStep int64
		if err := tx.QueryRow(ctx, q, p.UserID).Scan(&fid, &ct, &lastStep); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrMFAState
			}
			return err
		}
		secret, err := crypto.Open(ctx, m.cr, mfaAAD(fid), ct)
		if err != nil {
			return err
		}
		step, ok := matchStep(string(secret), code, now)
		clear(secret)
		if !ok || step <= lastStep { // a code may be used once (replay protection)
			failed = true
			var n int
			if err := tx.QueryRow(ctx, `UPDATE session SET mfa_failures = mfa_failures + 1 WHERE id=$1 RETURNING mfa_failures`, p.SessionID).Scan(&n); err != nil {
				return err
			}
			if n >= m.cfg.MFAMaxFailures {
				locked = true
				if _, err := tx.Exec(ctx, `UPDATE session SET revoked_at=$2, revoke_reason='mfa_failures' WHERE id=$1`, p.SessionID, now); err != nil {
					return err
				}
			}
			return ap.Record(ctx, tx, ap.Event{ActorRef: p.UserID, ActorRole: "professional", Action: "identity.mfa_" + mode, SubjectRef: p.UserID,
				ResourceType: "mfa_factor", ResourceRef: fid, Result: ap.Denied})
		}
		if mode == "confirm" {
			if _, err := tx.Exec(ctx, `UPDATE mfa_factor SET confirmed_at=$2, last_step=$3 WHERE id=$1`, fid, now, step); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE mfa_factor SET last_step=$2 WHERE id=$1`, fid, step); err != nil {
			return err
		}
		set := `UPDATE session SET mfa_at=$2, mfa_failures=0 WHERE id=$1`
		if mode == "step_up" {
			set = `UPDATE session SET step_up_at=$2, mfa_failures=0 WHERE id=$1`
		}
		if _, err := tx.Exec(ctx, set, p.SessionID, now); err != nil {
			return err
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: p.UserID, ActorRole: "professional", Action: "identity.mfa_" + mode, SubjectRef: p.UserID,
			ResourceType: "mfa_factor", ResourceRef: fid, Result: ap.Allowed})
	})
	if err != nil {
		return err
	}
	if locked {
		m.log.Warn("identity.mfa_locked")
		m.Alert(ctx, "identity.mfa_failures")
		return ErrTokenInvalid
	}
	if failed {
		return ErrMFAInvalid
	}
	return nil
}

// matchStep returns the 30-second step whose code equals code, within ±totpSkew.
func matchStep(secret, code string, now time.Time) (int64, bool) {
	base := now.Unix() / totpPeriod
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		step := base + d
		want, err := totp.GenerateCodeCustom(secret, time.Unix(step*totpPeriod, 0), totpOpts)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return step, true
		}
	}
	return 0, false
}
