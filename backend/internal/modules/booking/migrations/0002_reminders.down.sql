DROP INDEX appointment_reminder_due_idx;
ALTER TABLE appointment DROP COLUMN reminder_day_at, DROP COLUMN reminder_soon_at, DROP COLUMN consult_notice_at;
