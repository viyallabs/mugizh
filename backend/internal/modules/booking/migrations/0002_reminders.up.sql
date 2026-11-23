-- S-157: when each appointment message was handled (day-before reminder, 2-hour reminder,
-- online consult starting). Set in the same transaction that emits appointment.reminder_due,
-- so a reminder is never lost or doubled; cleared when the appointment is rescheduled.
ALTER TABLE appointment
  ADD COLUMN reminder_day_at   timestamptz,
  ADD COLUMN reminder_soon_at  timestamptz,
  ADD COLUMN consult_notice_at timestamptz;
CREATE INDEX appointment_reminder_due_idx ON appointment (start_at)
  WHERE state = 'confirmed' AND (reminder_day_at IS NULL OR reminder_soon_at IS NULL OR consult_notice_at IS NULL);
