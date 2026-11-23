DROP INDEX IF EXISTS document_hidden_idx;
ALTER TABLE document DROP CONSTRAINT IF EXISTS document_hidden_complete;
ALTER TABLE document DROP COLUMN IF EXISTS hidden_by_role;
ALTER TABLE document DROP COLUMN IF EXISTS hidden_reason;
ALTER TABLE document DROP COLUMN IF EXISTS hidden_at;
