-- Review M2: a reminder held through quiet hours is never sent once it is too late to matter.
ALTER TABLE dispatch DROP CONSTRAINT dispatch_state_check;
ALTER TABLE dispatch ADD CONSTRAINT dispatch_state_check
  CHECK (state IN ('queued','sent','delivered','failed','fallback','dead_letter','blocked','expired'));
