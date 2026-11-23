DROP INDEX IF EXISTS rights_request_journal_due_idx;
ALTER TABLE rights_request DROP COLUMN IF EXISTS journaled;
