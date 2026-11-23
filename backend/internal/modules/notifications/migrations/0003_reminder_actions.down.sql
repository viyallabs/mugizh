DROP TABLE IF EXISTS reminder_action;
UPDATE dispatch SET state = 'failed', fail_reason = 'acted' WHERE state = 'acted';
ALTER TABLE dispatch DROP CONSTRAINT dispatch_state_check;
ALTER TABLE dispatch ADD CONSTRAINT dispatch_state_check
  CHECK (state IN ('queued','sent','delivered','failed','fallback','dead_letter','blocked','expired'));
