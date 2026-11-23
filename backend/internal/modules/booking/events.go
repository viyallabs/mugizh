package booking

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	vp "gitlab.com/viyallabs/mugizh/backend/internal/modules/verification/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/outbox"
)

// OnPaymentCaptured confirms an appointment from a verified payment.captured event
// (K-PAY-02). Subscribe as bp.SubscriberPaymentCaptured to "payment.captured".
//
//   - held or pending_payment and the hold is still live and the practice still listed:
//     confirmed (appointment.confirmed).
//   - the hold had already timed out: NOT confirmed. The appointment is expired and
//     appointment.expired is emitted, so payments refunds the late payment in full (TC-28).
//   - the practice is no longer listed (K-CS-01): NOT confirmed; cancelled by the system with
//     rule RF04 and appointment.cancelled, so payments refunds in full.
//   - already expired or cancelled before it was ever confirmed: appointment.expired is
//     emitted again (payments' refund key makes this safe), so the money always comes back.
//   - already confirmed (a duplicate): nothing; payments handles duplicate payments.
func (m *Module) OnPaymentCaptured(ctx context.Context, e outbox.Event) error {
	ref := e.Payload["appointmentRef"]
	if !ids.Valid(ref) {
		return fmt.Errorf("booking: payment.captured without a valid appointmentRef")
	}
	a, err := loadAppt(ctx, m.pool, ref, false)
	if errors.Is(err, pgx.ErrNoRows) {
		m.log.Warn("booking.capture_for_unknown_appointment", "event_id", e.ID)
		return nil
	}
	if err != nil {
		return err
	}
	listed := true
	pr, err := m.d.Directory.GetPractice(ctx, a.PracticeID)
	if err != nil {
		if !errors.Is(err, dp.ErrNotFound) {
			return err // retry; never confirm on an unknown answer
		}
		listed = false
	} else {
		// Review H3: the licence is checked at the source, not only in the event-fed read model.
		ok, verr := m.stillVerified(ctx, pr.ProfessionalRef)
		if verr != nil {
			return verr // retry; never confirm on an unknown answer
		}
		listed = ok
	}
	return outbox.Once(ctx, m.pool, bp.SubscriberPaymentCaptured, e, func(tx pgx.Tx) error {
		cur, err := loadAppt(ctx, tx, ref, true)
		if err != nil {
			return err
		}
		now := m.now()
		switch cur.State {
		case StateHeld, StatePendingPayment:
			if cur.HoldExpiresAt == nil || !cur.HoldExpiresAt.After(now) {
				if _, err := m.expireTx(ctx, tx, ref, "late_capture"); err != nil {
					return err
				}
				if cur.State == StateHeld { // expireTx emits only for pending_payment
					return m.emit(ctx, tx, bp.EventExpired, ref, m.expiredPayload(cur))
				}
				return nil
			}
			if !listed {
				return m.cancelConfirmedTx(ctx, tx, cur, "system", RuleClinicOrDoctorCancelled, SystemActor, "system", "", now)
			}
			return m.confirmTx(ctx, tx, cur, SystemActor, "system", "")
		case StateExpired, StateCancelled:
			var everConfirmed bool
			var rule string
			if err := tx.QueryRow(ctx, `SELECT confirmed_at IS NOT NULL, cancel_rule FROM appointment WHERE id=$1`, ref).Scan(&everConfirmed, &rule); err != nil {
				return err
			}
			if everConfirmed || rule != "" {
				return nil // refunded through its own cancellation event
			}
			return m.emit(ctx, tx, bp.EventExpired, ref, m.expiredPayload(cur))
		}
		return nil
	})
}

// OnPaymentFailed records a failed payment attempt. The hold stays: the patient may try
// again until it expires. Subscribe as bp.SubscriberPaymentFailed to "payment.failed".
func (m *Module) OnPaymentFailed(ctx context.Context, e outbox.Event) error {
	ref := e.Payload["appointmentRef"]
	if !ids.Valid(ref) {
		return fmt.Errorf("booking: payment.failed without a valid appointmentRef")
	}
	return outbox.Once(ctx, m.pool, bp.SubscriberPaymentFailed, e, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE appointment SET payment_failed_at=$2, updated_at=$2 WHERE id=$1 AND state='pending_payment'`, ref, m.now())
		return err
	})
}

// OnVerificationStatusChanged cancels every future held, pending or confirmed appointment of
// a professional (or clinic) that was suspended or whose registration expired: byWhom
// system, rule RF04 (full refund), appointment.cancelled per confirmed appointment so the
// patient and the clinic can be told and records revokes access (E-VER-06, TC-66).
// Subscribe as bp.SubscriberVerification to "verification.status_changed".
func (m *Module) OnVerificationStatusChanged(ctx context.Context, e outbox.Event) error {
	status := vp.Status(e.Payload[vp.PayloadStatus])
	ref := e.Payload[vp.PayloadProfessional]
	if status != vp.StatusSuspended && status != vp.StatusExpired {
		return nil
	}
	if !ids.Valid(ref) {
		return fmt.Errorf("booking: verification.status_changed without a valid ref")
	}
	return outbox.Once(ctx, m.pool, bp.SubscriberVerification, e, func(tx pgx.Tx) error {
		now := m.now()
		rows, err := tx.Query(ctx, `SELECT id FROM appointment WHERE (professional_ref=$1 OR clinic_ref=$1)
			AND state IN ('held','pending_payment','confirmed') AND start_at > $2 ORDER BY start_at, id`, ref, now)
		if err != nil {
			return err
		}
		var todo []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			todo = append(todo, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range todo {
			cur, err := loadAppt(ctx, tx, id, true)
			if err != nil {
				return err
			}
			switch cur.State {
			case StateConfirmed:
				err = m.cancelConfirmedTx(ctx, tx, cur, "system", RuleClinicOrDoctorCancelled, SystemActor, "system", "", now)
			case StateHeld, StatePendingPayment:
				err = m.cancelUnpaidTx(ctx, tx, cur, "system", SystemActor, "system", "")
			}
			if err != nil {
				return err
			}
		}
		if len(todo) > 0 {
			m.log.Info("booking.appointments_cancelled_on_suspension", "count", len(todo))
		}
		return nil
	})
}

// OnPracticeUpdated refreshes the copied practice refs and zone and reconciles slots
// (subscribe as bp.SubscriberPracticeUpdated to "practice.updated").
func (m *Module) OnPracticeUpdated(ctx context.Context, e outbox.Event) error {
	ref := e.Payload["practiceRef"]
	if !ids.Valid(ref) {
		return fmt.Errorf("booking: practice.updated without a valid practiceRef")
	}
	if err := m.RefreshPractice(ctx, ref); err != nil {
		m.log.Error("booking.practice_refresh_failed", logging.Err(err))
		return err
	}
	return nil
}
