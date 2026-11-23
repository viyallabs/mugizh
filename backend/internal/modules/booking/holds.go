package booking

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
	dp "gitlab.com/viyallabs/mugizh/backend/internal/modules/directory/public"
	pp "gitlab.com/viyallabs/mugizh/backend/internal/modules/payments/public"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/ids"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/logging"
	"gitlab.com/viyallabs/mugizh/backend/internal/platform/problem"
)

// HoldInput is a patient's request to take a slot.
type HoldInput struct {
	PatientRef string
	PracticeID string
	SlotID     string
	Mode       string
	// PatientTimeZone is the patient's IANA zone, for showing the time in their own zone.
	PatientTimeZone string
	// PatientInIndiaConfirmed is the P10 confirmation "I am in India now". Required for video
	// and audio; in-person bookings need no location confirmation.
	PatientInIndiaConfirmed bool
	RequestID               string
}

var errSlotTaken = errors.New("booking: slot not open")

// Hold takes a slot for a patient for the hold time and creates the appointment in state
// held (S-121, S-122). The take is one UPDATE ... WHERE state = 'open', so under any
// number of parallel callers exactly one wins and the rest get a *SlotTakenError (TC-22).
// For a pay-at-clinic in-person booking no money is handled and the appointment is
// confirmed at once. Everything fails closed: no consent, an unlisted practice or an
// unknown payment mode means no hold.
func (m *Module) Hold(ctx context.Context, in HoldInput) (Appointment, error) {
	if !ids.Valid(in.PatientRef) || !ids.Valid(in.PracticeID) || !ids.Valid(in.SlotID) || !validModes[in.Mode] {
		return Appointment{}, problem.BadRequest
	}
	if _, err := loadZone(in.PatientTimeZone); err != nil {
		return Appointment{}, ProblemInvalidTimeZone
	}
	// P-CARE consent is needed to book (K-DP-02 consent model). Fail closed on any error.
	ok, err := m.d.Gate.HasConsent(ctx, in.PatientRef, ConsentCare)
	if err != nil {
		return Appointment{}, err
	}
	if !ok {
		return Appointment{}, problem.ConsentRequired
	}
	// K-CS-01: only a listed practice is booked.
	p, err := m.d.Directory.GetPractice(ctx, in.PracticeID)
	if errors.Is(err, dp.ErrNotFound) {
		return Appointment{}, problem.NotFound
	}
	if err != nil {
		return Appointment{}, err
	}
	if !p.Listed || !slices.Contains(p.Modes, in.Mode) {
		if !p.Listed {
			return Appointment{}, problem.NotFound
		}
		return Appointment{}, ProblemModeNotOffered
	}
	if ok, err := m.stillVerified(ctx, p.ProfessionalRef); err != nil || !ok {
		return Appointment{}, ProblemPracticeGone // unverified or unknown: fail closed (review H3)
	}
	// P10: an online consult needs the patient to confirm they are in India now.
	if in.Mode != ModeInPerson && !in.PatientInIndiaConfirmed {
		return Appointment{}, ProblemOutsideIndia
	}
	settle, err := m.d.Payments.SettlementMode(ctx, p.ClinicRef)
	if err != nil {
		return Appointment{}, err
	}
	switch settle {
	case pp.PayAtClinic:
		if in.Mode != ModeInPerson {
			return Appointment{}, ProblemPayAtClinicOnline
		}
	case pp.ClinicCollected:
		paused, err := m.d.Payments.ClinicPaymentsPaused(ctx, p.ClinicRef)
		if err != nil {
			return Appointment{}, err
		}
		if paused {
			return Appointment{}, ProblemPaymentsPaused
		}
	default:
		return Appointment{}, ProblemPaymentUnavail
	}
	now := m.now()
	_, err = loadZone(p.TimeZone)
	if err != nil {
		return Appointment{}, problem.NotFound
	}

	var appt Appointment
	err = m.inTx(ctx, func(tx pgx.Tx) error {
		// Serialise this patient's holds so the limit cannot be beaten by parallel requests.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, in.PatientRef); err != nil {
			return err
		}
		// Replay: the patient already holds this slot.
		var existing string
		err := tx.QueryRow(ctx, `SELECT appointment_id FROM hold WHERE slot_id=$1 AND patient_ref=$2 AND expires_at > $3`, in.SlotID, in.PatientRef, now).Scan(&existing)
		if err == nil {
			appt, err = loadAppt(ctx, tx, existing, false)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// A hold that timed out but was not yet released does not block the slot.
		var dead string
		err = tx.QueryRow(ctx, `SELECT appointment_id FROM hold WHERE slot_id=$1 AND expires_at <= $2`, in.SlotID, now).Scan(&dead)
		if err == nil {
			if _, err := m.expireTx(ctx, tx, dead, "hold_expired"); err != nil {
				return err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// E-SEC-04: at most MaxActiveHolds live holds per patient.
		var live int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM hold WHERE patient_ref=$1 AND expires_at > $2`, in.PatientRef, now).Scan(&live); err != nil {
			return err
		}
		if live >= m.cfg.MaxActiveHolds {
			return ProblemTooManyHolds
		}
		// Pay-at-clinic bookings confirm at once and hold no payment, so they would escape the
		// hold limit; cap the patient's upcoming ones as well (E-SEC-04, slot squatting).
		if settle == pp.PayAtClinic {
			var upcoming int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM appointment WHERE patient_ref=$1 AND settlement_mode='pay_at_clinic'
				AND state='confirmed' AND start_at > $2`, in.PatientRef, now).Scan(&upcoming); err != nil {
				return err
			}
			if upcoming >= m.cfg.MaxPayAtClinicUpcoming {
				return ProblemTooManyHolds
			}
		}
		apptID := ids.New()
		start, end, err := takeSlot(ctx, tx, in.SlotID, in.PracticeID, apptID, in.Mode, now)
		if err != nil {
			return err
		}
		var india *bool
		if in.Mode != ModeInPerson {
			t := true
			india = &t
		}
		expires := now.Add(m.cfg.HoldTTL)
		if _, err := tx.Exec(ctx, `INSERT INTO appointment (id, patient_ref, practice_id, professional_ref, clinic_ref, slot_id, start_at, end_at, mode,
			practice_time_zone, patient_time_zone, patient_in_india_confirmed, fee_minor, currency, fee_version, policy_version, settlement_mode, state, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,'held',$18,$18)`,
			apptID, in.PatientRef, in.PracticeID, p.ProfessionalRef, p.ClinicRef, in.SlotID, start, end, in.Mode,
			p.TimeZone, in.PatientTimeZone, india, p.FeeAmount, p.Currency, p.FeeVersion, m.cfg.RefundPolicyVersion, string(settle), now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hold (hold_id, slot_id, appointment_id, patient_ref, expires_at, created_at) VALUES ($1,$2,$3,$4,$5,$6)`,
			ids.New(), in.SlotID, apptID, in.PatientRef, expires, now); err != nil {
			return err
		}
		if err := m.audit(ctx, tx, in.PatientRef, "patient", "booking.hold", in.PatientRef, apptID, in.RequestID); err != nil {
			return err
		}
		if err := m.emit(ctx, tx, bp.EventHeld, apptID, map[string]string{"appointmentRef": apptID, "practiceRef": in.PracticeID}); err != nil {
			return err
		}
		appt, err = loadAppt(ctx, tx, apptID, true)
		if err != nil {
			return err
		}
		if settle == pp.PayAtClinic {
			// No money is handled: confirm now (a re-check of listing is the one above, same call).
			if err := m.confirmTx(ctx, tx, appt, in.PatientRef, "patient", in.RequestID); err != nil {
				return err
			}
			appt, err = loadAppt(ctx, tx, apptID, false)
		}
		return err
	})
	if errors.Is(err, errSlotTaken) {
		return Appointment{}, &SlotTakenError{Next: m.nextOpen(ctx, in.PracticeID, now)}
	}
	return appt, err
}

// ExpireHolds releases every hold whose time has passed: the slot reopens and, for a hold
// that already had an order (pending_payment), appointment.expired is emitted so a late
// capture is refunded in full (E-PAY-01, TC-28). Run it every minute; it is idempotent.
func (m *Module) ExpireHolds(ctx context.Context, now time.Time) (int, error) {
	total := 0
	for {
		rows, err := m.pool.Query(ctx, `SELECT appointment_id FROM hold WHERE expires_at <= $1 ORDER BY expires_at LIMIT 100`, now)
		if err != nil {
			return total, err
		}
		var refs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return total, err
			}
			refs = append(refs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return total, err
		}
		done := 0
		for _, id := range refs {
			var changed bool
			err := m.inTx(ctx, func(tx pgx.Tx) error {
				var e error
				changed, e = m.expireTx(ctx, tx, id, "hold_expired")
				return e
			})
			if err != nil {
				m.log.Error("booking.hold_expire_failed", logging.Err(err))
				continue
			}
			if changed {
				done++
			}
		}
		total += done
		if len(refs) < 100 || done == 0 {
			return total, nil
		}
	}
}
