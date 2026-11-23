-- Erasure journal (S-185; backup-and-restore.md sections 2 and 3, E-DP-04). Every erasure
-- request, undo and completion is also written to write-once storage, outside this
-- database, and replayed before the service opens after a restore. This column records
-- which entries are already written, so the worker can catch up after an object-store
-- outage. The journal holds references and times only.
ALTER TABLE rights_request ADD COLUMN journaled text[] NOT NULL DEFAULT '{}'
  CHECK (journaled <@ ARRAY['requested','cancelled','completed']::text[]);
CREATE INDEX rights_request_journal_due_idx ON rights_request (received_at)
  WHERE type = 'erasure' AND NOT ('requested' = ANY (journaled));
