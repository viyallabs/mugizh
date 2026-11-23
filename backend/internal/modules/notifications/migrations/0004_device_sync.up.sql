-- S-165: requests to tell a patient's devices to refresh their on-device reminder schedule (a silent push with
-- no content). One row per patient: requests that arrive before the push goes out merge into one. Data
-- inventory 4.10: a user reference and instants only. A finished row is deleted after a day; erasure step 1
-- deletes it at once.
CREATE TABLE device_sync (
  recipient_ref   uuid        PRIMARY KEY,
  pending         boolean     NOT NULL,
  requested_at    timestamptz NOT NULL,
  next_attempt_at timestamptz NOT NULL,
  attempts        integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  last_sent_at    timestamptz,
  lease_until     timestamptz,
  updated_at      timestamptz NOT NULL
);
CREATE INDEX device_sync_due_idx ON device_sync (next_attempt_at) WHERE pending;
