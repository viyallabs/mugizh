DROP TABLE IF EXISTS occurrence_actions;
DROP INDEX IF EXISTS occurrences_scan_idx;
CREATE INDEX occurrences_scan_idx ON occurrences (due_at) WHERE state = 'pending' AND enqueued_at IS NULL;
ALTER TABLE occurrences DROP COLUMN IF EXISTS snooze_count, DROP COLUMN IF EXISTS notify_at, DROP COLUMN IF EXISTS acted_at, DROP COLUMN IF EXISTS action;
