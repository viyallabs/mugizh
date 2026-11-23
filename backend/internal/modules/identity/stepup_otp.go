package identity

import (
	"context"
	"crypto/hmac"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	ap "gitlab.com/viyallabs/mugizh/backend/internal/modules/audit/public"
	ip "gitlab.com/viyallabs/mugizh/backend/internal/modules/identity/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/db"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// Patient step-up (S-178; TM-32, TM-33; data-rights-flows.md "step-up OTP").
//
// Export, erasure, undoing an erasure and downloading an export need a step-up within
// ip.StepUpWindow. Professionals use their authenticator (StepUp). Patients have no
// authenticator, so they re-authenticate with a one-time code sent by SMS to the number
// on their account: never to a number in the request, so someone holding only an
// unlocked phone or a stolen access token cannot redirect it. The code is bound to the
// session that asked for it and shares the sign-in limits, attempt count and lockout.

// RequestStepUpOTP sends a step-up code to the phone on file of a signed-in patient.
func (m *Module) RequestStepUpOTP(ctx context.Context, p *ip.Principal, ipHash string) (Challenge, error) {
	if p == nil || !ids.Valid(p.UserID) || !ids.Valid(p.SessionID) {
		return Challenge{}, problem.Unauthenticated
	}
	if p.App != ip.AppPatient {
		return Challenge{}, ip.ProblemWrongApp // professionals use the authenticator
	}
	if p.Restricted {
		return Challenge{}, problem.Restricted // a device still waiting for re-verification
	}
	phone, err := m.phoneOf(ctx, p.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Challenge{}, problem.Unauthenticated
		}
		return Challenge{}, err
	}
	bidx := m.phoneIndex(phone)
	now := m.now().UTC()
	ipKey := []byte(ipHash)
	if until, locked, err := m.lockedUntil(ctx, bidx, ipKey, now); err != nil {
		return Challenge{}, err
	} else if locked {
		return Challenge{}, retryAfter(ErrLocked, until.Sub(now))
	}
	code, err := sixDigits()
	if err != nil {
		return Challenge{}, err
	}
	ch := Challenge{ID: ids.New(), ExpiresAt: now.Add(m.cfg.OTPTTL)}
	err = db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := m.countOTPSend(ctx, tx, bidx, ipKey, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO otp_challenge (id, phone_bidx, purpose, app, code_hash, expires_at, session_id)
			VALUES ($1,$2,'step_up','patient',$3,$4,$5)`, ch.ID, bidx, m.codeHash(ch.ID, code), ch.ExpiresAt, p.SessionID)
		return err
	})
	if err := m.sendOTP(ctx, err, phone, code, ch.ID); err != nil {
		return Challenge{}, err
	}
	return ch, nil
}

// VerifyStepUpOTP checks a step-up code and marks this session stepped up. A wrong code
// counts towards the same limit and lockout as sign-in; every result is audited.
func (m *Module) VerifyStepUpOTP(ctx context.Context, p *ip.Principal, challengeID, code, ipHash string) error {
	if p == nil || !ids.Valid(p.UserID) || !ids.Valid(p.SessionID) {
		return problem.Unauthenticated
	}
	if p.App != ip.AppPatient {
		return ip.ProblemWrongApp
	}
	if p.Restricted {
		return problem.Restricted
	}
	if !ids.Valid(challengeID) || len(code) != 6 {
		return ErrOTPInvalid
	}
	now := m.now().UTC()
	ipKey := []byte(ipHash)
	ok := false
	err := db.InTx(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		ok = false
		var bidx, hash []byte
		var attempts int
		var exp time.Time
		var consumed *time.Time
		var session *string
		err := tx.QueryRow(ctx, `SELECT phone_bidx, code_hash, attempts, expires_at, consumed_at, session_id::text FROM otp_challenge
			WHERE id=$1 AND purpose='step_up' AND app='patient' FOR UPDATE`, challengeID).Scan(&bidx, &hash, &attempts, &exp, &consumed, &session)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOTPInvalid
		}
		if err != nil {
			return err
		}
		// Another session's challenge looks exactly like an unknown one.
		if session == nil || *session != p.SessionID {
			return ErrOTPInvalid
		}
		if consumed != nil || !now.Before(exp) || attempts >= m.cfg.OTPMaxAttempts {
			return ErrOTPInvalid
		}
		var until time.Time
		if err := tx.QueryRow(ctx, `SELECT locked_until FROM sign_in_lock WHERE phone_bidx=$1 AND ip_key=$2`, bidx, ipKey).Scan(&until); err == nil && until.After(now) {
			return retryAfter(ErrLocked, until.Sub(now))
		}
		if !hmac.Equal(hash, m.codeHash(challengeID, code)) {
			if err := m.wrongAttempt(ctx, tx, challengeID, attempts+1, bidx, ipKey, now); err != nil {
				return err
			}
			return ap.Record(ctx, tx, ap.Event{ActorRef: p.UserID, ActorRole: "patient", Action: "identity.step_up_otp", SubjectRef: p.UserID,
				ResourceType: "session", ResourceRef: p.SessionID, Result: ap.Denied})
		}
		ok = true
		if _, err := tx.Exec(ctx, `UPDATE otp_challenge SET consumed_at=$2, attempts=attempts+1 WHERE id=$1`, challengeID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE sign_in_lock SET lockouts=0 WHERE phone_bidx=$1 AND ip_key=$2`, bidx, ipKey); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE session SET step_up_at=$2 WHERE id=$1 AND user_id=$3 AND revoked_at IS NULL`, p.SessionID, now, p.UserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return problem.Unauthenticated
		}
		return ap.Record(ctx, tx, ap.Event{ActorRef: p.UserID, ActorRole: "patient", Action: "identity.step_up_otp", SubjectRef: p.UserID,
			ResourceType: "session", ResourceRef: p.SessionID, Result: ap.Allowed})
	})
	if err != nil {
		return err
	}
	if !ok {
		m.log.Info("identity.step_up_otp_wrong")
		return ErrOTPInvalid
	}
	return nil
}
