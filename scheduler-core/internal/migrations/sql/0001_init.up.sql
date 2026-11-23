-- scheduler-core schema (S-127). K-ARC-02: no health data. The only free-form text columns are rrule and
-- time_zone; every other text column is an opaque reference or an enum-like value constrained by CHECK.
-- Times are timestamptz (UTC) plus the IANA zone (K-ARC-04).
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mugizh_scheduler') THEN
    CREATE ROLE mugizh_scheduler NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
  END IF;
END $$;

REVOKE ALL ON SCHEMA scheduler FROM PUBLIC;
GRANT USAGE ON SCHEMA scheduler TO mugizh_scheduler;
ALTER DEFAULT PRIVILEGES IN SCHEMA scheduler GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO mugizh_scheduler;

-- River's tables (created by rivermigrate, run by the migration owner after this file).
CREATE SCHEMA IF NOT EXISTS scheduler_queue;
REVOKE ALL ON SCHEMA scheduler_queue FROM PUBLIC;
GRANT USAGE ON SCHEMA scheduler_queue TO mugizh_scheduler;
ALTER DEFAULT PRIVILEGES IN SCHEMA scheduler_queue GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO mugizh_scheduler;
ALTER DEFAULT PRIVILEGES IN SCHEMA scheduler_queue GRANT USAGE, SELECT ON SEQUENCES TO mugizh_scheduler;
ALTER DEFAULT PRIVILEGES IN SCHEMA scheduler_queue GRANT EXECUTE ON FUNCTIONS TO mugizh_scheduler;

CREATE TABLE subjects (
  subject_ref uuid PRIMARY KEY,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE activities (
  id                uuid PRIMARY KEY,
  subject_ref       uuid NOT NULL REFERENCES subjects (subject_ref) ON DELETE CASCADE,
  app_ref           text NOT NULL CHECK (app_ref ~ '^[a-z][a-z0-9_-]{0,31}$'),
  external_ref      text NOT NULL CHECK (external_ref ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
  type_key          text NOT NULL CHECK (type_key IN ('med.dose', 'injection.dose', 'test.due', 'appointment', 'custom')),
  rrule             text NOT NULL CHECK (length(rrule) BETWEEN 1 AND 512),
  time_zone         text NOT NULL CHECK (length(time_zone) BETWEEN 1 AND 64),
  starts_at         timestamptz NOT NULL,
  ends_at           timestamptz,
  state             text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'paused', 'ended', 'cancelled')),
  generated_through timestamptz NOT NULL,
  version           integer NOT NULL DEFAULT 1,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX activities_subject_idx ON activities (subject_ref);
CREATE INDEX activities_generation_idx ON activities (generated_through) WHERE state = 'active';

CREATE TABLE occurrences (
  id              uuid PRIMARY KEY,
  activity_id     uuid NOT NULL REFERENCES activities (id) ON DELETE CASCADE,
  subject_ref     uuid NOT NULL,
  due_at          timestamptz NOT NULL,
  state           text NOT NULL DEFAULT 'pending'
                  CHECK (state IN ('pending', 'dispatched', 'acknowledged', 'missed', 'cancelled', 'failed')),
  enqueued_at     timestamptz,
  dispatched_at   timestamptz,
  acknowledged_at timestamptz,
  created_at      timestamptz NOT NULL DEFAULT now()
);
-- No duplicate occurrence per activity and instant. Cancelled rows (zone/rule change, pause) do not block a
-- regenerated occurrence at the same instant.
CREATE UNIQUE INDEX occurrences_activity_due_uq ON occurrences (activity_id, due_at) WHERE state <> 'cancelled';
CREATE INDEX occurrences_scan_idx ON occurrences (due_at) WHERE state = 'pending' AND enqueued_at IS NULL;
CREATE INDEX occurrences_subject_idx ON occurrences (subject_ref, due_at, id);
CREATE INDEX occurrences_dispatched_idx ON occurrences (dispatched_at) WHERE state = 'dispatched';

-- HTTP replay protection for POST /v1/activities (API standards section 6): the key, a hash of the body and the
-- created activity (the response is rebuilt from it). Kept 24 hours.
CREATE TABLE idempotency_keys (
  idem_key     uuid PRIMARY KEY,
  request_hash bytea NOT NULL,
  activity_id  uuid NOT NULL REFERENCES activities (id) ON DELETE CASCADE,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);
