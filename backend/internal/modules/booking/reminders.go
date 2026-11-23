package booking

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	bp "gitlab.com/viyallabs/mugizh/backend/internal/modules/booking/public"
)

type reminderKind struct {
	kind, column string
	offset       func(c Config) time.Duration
	onlineOnly   bool
}

var reminderKinds = []reminderKind{
	{bp.ReminderDayBefore, "reminder_day_at", func(c Config) time.Duration { return c.ReminderDayBefore }, false},
	{bp.ReminderSoon, "reminder_soon_at", func(c Config) time.Duration { return c.ReminderSoon }, false},
	{bp.ReminderConsultStart, "consult_notice_at", func(c Config) time.Duration { return c.ConsultStartNotice }, true},
}

// EmitDueReminders is a job (every minute, S-157). For each confirmed appointment whose
// message point has passed, it marks the message handled and emits appointment.reminder_due
// in the same transaction (never lost, never doubled; the notifications composer sends it).
//
// A message is skipped (marked, no event) when the appointment was confirmed after its point,
// so a booking made two hours ahead gets its confirmation, not an immediate "reminder"; a
// day-before reminder whose time has run into the short-reminder window is skipped too.
func (m *Module) EmitDueReminders(ctx context.Context, now time.Time) (int, error) {
	sent := 0
	for _, k := range reminderKinds {
		n, err := m.emitReminders(ctx, k, now)
		if err != nil {
			return sent, err
		}
		sent += n
	}
	return sent, nil
}

func (m *Module) emitReminders(ctx context.Context, k reminderKind, now time.Time) (int, error) {
	off := k.offset(m.cfg)
	sent := 0
	err := m.inTx(ctx, func(tx pgx.Tx) error {
		sent = 0
		online := ""
		if k.onlineOnly {
			online = " AND mode <> 'in_person'"
		}
		// The column name comes from the fixed table above, never from input.
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT id, patient_ref, professional_ref, practice_id, mode, start_at, confirmed_at FROM appointment
			WHERE state='confirmed' AND %s IS NULL AND start_at - make_interval(secs => $2) <= $1 AND start_at > $1%s
			ORDER BY start_at LIMIT 500 FOR UPDATE SKIP LOCKED`, k.column, online), now, off.Seconds()) // #nosec G201 -- fixed column names
		if err != nil {
			return err
		}
		type due struct {
			id, patient, doctor, practice, mode string
			start                               time.Time
			confirmed                           *time.Time
		}
		var list []due
		for rows.Next() {
			var d due
			if err := rows.Scan(&d.id, &d.patient, &d.doctor, &d.practice, &d.mode, &d.start, &d.confirmed); err != nil {
				rows.Close()
				return err
			}
			list = append(list, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, d := range list {
			send := d.confirmed != nil && !d.confirmed.After(d.start.Add(-off))
			if k.kind == bp.ReminderDayBefore && !now.Before(d.start.Add(-m.cfg.ReminderSoon)) {
				send = false // the short reminder covers it
			}
			if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE appointment SET %s=$2 WHERE id=$1`, k.column), d.id, now); err != nil { // #nosec G201 -- fixed column names
				return err
			}
			if !send {
				continue
			}
			if err := m.emit(ctx, tx, bp.EventReminderDue, d.id, map[string]string{"appointmentRef": d.id, "patientRef": d.patient,
				"doctorRef": d.doctor, "practiceRef": d.practice, "mode": d.mode, "slotStart": utc(d.start), "kind": k.kind}); err != nil {
				return err
			}
			sent++
		}
		return nil
	})
	return sent, err
}
