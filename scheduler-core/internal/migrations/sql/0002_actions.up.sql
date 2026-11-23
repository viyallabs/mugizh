-- S-154: a patient marks an occurrence taken, skipped or snoozed (also offline, synced later).
-- K-ARC-02: only enum-like values, counters and instants are added; no health content.
ALTER TABLE occurrences
  ADD COLUMN action       text CHECK (action IN ('taken', 'skipped')),
  ADD COLUMN acted_at     timestamptz,
  ADD COLUMN notify_at    timestamptz,
  ADD COLUMN snooze_count integer NOT NULL DEFAULT 0 CHECK (snooze_count BETWEEN 0 AND 10);

-- A snoozed occurrence is claimed again at COALESCE(notify_at, due_at).
DROP INDEX occurrences_scan_idx;
CREATE INDEX occurrences_scan_idx ON occurrences ((COALESCE(notify_at, due_at))) WHERE state = 'pending' AND enqueued_at IS NULL;

-- One row per accepted client action ID: replay protection for offline sync (the same ID returns the same answer).
CREATE TABLE occurrence_actions (
  client_action_id uuid PRIMARY KEY,
  occurrence_id    uuid NOT NULL REFERENCES occurrences (id) ON DELETE CASCADE,
  action           text NOT NULL CHECK (action IN ('taken', 'skipped', 'snoozed')),
  acted_at         timestamptz NOT NULL,
  snooze_minutes   integer CHECK (snooze_minutes BETWEEN 1 AND 240),
  created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX occurrence_actions_occ_idx ON occurrence_actions (occurrence_id);
