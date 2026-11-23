-- S-154: a patient marks a reminder taken, skipped or snoozed. The state itself lives in scheduler-core; this
-- module keeps one row per accepted client action ID, only to audit each action exactly once, and a dispatch the
-- patient already acted on is finished as 'acted' (no SMS fallback for a dose already taken).
ALTER TABLE dispatch DROP CONSTRAINT dispatch_state_check;
ALTER TABLE dispatch ADD CONSTRAINT dispatch_state_check
  CHECK (state IN ('queued','sent','delivered','failed','fallback','dead_letter','blocked','expired','acted'));

-- Data inventory 4.10: references, the action word and instants only (RET-10, 90 days; DEL-ERASE).
CREATE TABLE reminder_action (
  client_action_id uuid        PRIMARY KEY,
  user_ref         uuid        NOT NULL,
  occurrence_ref   uuid        NOT NULL,
  action           text        NOT NULL CHECK (action IN ('taken','skipped','snoozed')),
  acted_at         timestamptz NOT NULL,
  created_at       timestamptz NOT NULL
);
CREATE INDEX reminder_action_user_idx ON reminder_action (user_ref);
CREATE INDEX reminder_action_created_idx ON reminder_action (created_at);
