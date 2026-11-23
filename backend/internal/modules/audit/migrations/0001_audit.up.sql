-- Audit module (S-096; audit-logging-design §2, §4; ADR-28).
-- audit_outbox: every module role may INSERT only. The audit worker moves rows into the
-- hash-chained audit_event table, which no role may UPDATE or DELETE.

CREATE TABLE audit_outbox (
  event_id      uuid        PRIMARY KEY,
  occurred_at   timestamptz NOT NULL,
  actor_ref     text        NOT NULL CHECK (length(actor_ref) BETWEEN 1 AND 80),
  actor_role    text        NOT NULL DEFAULT '' CHECK (length(actor_role) <= 40),
  action        text        NOT NULL CHECK (action ~ '^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$'),
  subject_ref   text        NOT NULL DEFAULT '' CHECK (length(subject_ref) <= 80),
  resource_type text        NOT NULL DEFAULT '' CHECK (length(resource_type) <= 80),
  resource_ref  text        NOT NULL DEFAULT '' CHECK (length(resource_ref) <= 80),
  purpose       text        NOT NULL DEFAULT '' CHECK (length(purpose) <= 80),
  grant_id      text        NOT NULL DEFAULT '' CHECK (length(grant_id) <= 80),
  reason_code   text        NOT NULL DEFAULT '' CHECK (length(reason_code) <= 80),
  ticket_ref    text        NOT NULL DEFAULT '' CHECK (length(ticket_ref) <= 80),
  result        text        NOT NULL CHECK (result IN ('allowed','denied','failed')),
  client        text        NOT NULL DEFAULT '' CHECK (length(client) <= 80),
  ip_hash       text        NOT NULL DEFAULT '' CHECK (length(ip_hash) <= 80),
  request_id    text        NOT NULL DEFAULT '' CHECK (length(request_id) <= 80),
  inserted_at   timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX audit_outbox_inserted_idx ON audit_outbox (inserted_at, event_id);

-- Module roles: INSERT only. They cannot read, change or delete outbox rows.
REVOKE ALL ON audit_outbox FROM PUBLIC;
DO $$
DECLARE m text;
BEGIN
  FOREACH m IN ARRAY ARRAY['admin','booking','clinical','consult','directory','identity',
                           'notifications','payments','platform','records','verification'] LOOP
    EXECUTE format('REVOKE ALL ON audit.audit_outbox FROM %I', 'mugizh_mod_' || m);
    EXECUTE format('GRANT INSERT ON audit.audit_outbox TO %I', 'mugizh_mod_' || m);
  END LOOP;
END $$;
-- The audit role moves rows out of the outbox.
GRANT SELECT, INSERT, DELETE ON audit_outbox TO mugizh_mod_audit;
REVOKE UPDATE ON audit_outbox FROM mugizh_mod_audit;

-- Chained store. One chain per day of appending ("partition_day"); the first event of a
-- day links to the last hash of the previous day.
CREATE TABLE audit_event (
  partition_day date        NOT NULL,
  seq           bigint      NOT NULL CHECK (seq > 0),
  event_id      uuid        NOT NULL,
  occurred_at   timestamptz NOT NULL,
  chained_at    timestamptz NOT NULL,
  actor_ref     text        NOT NULL,
  actor_role    text        NOT NULL,
  action        text        NOT NULL,
  subject_ref   text        NOT NULL,
  resource_type text        NOT NULL,
  resource_ref  text        NOT NULL,
  purpose       text        NOT NULL,
  grant_id      text        NOT NULL,
  reason_code   text        NOT NULL,
  ticket_ref    text        NOT NULL,
  result        text        NOT NULL,
  client        text        NOT NULL,
  ip_hash       text        NOT NULL,
  request_id    text        NOT NULL,
  prev_hash     bytea       NOT NULL CHECK (length(prev_hash) = 32),
  hash          bytea       NOT NULL CHECK (length(hash) = 32),
  PRIMARY KEY (partition_day, seq)
);
CREATE UNIQUE INDEX audit_event_event_id_idx ON audit_event (event_id, partition_day);
CREATE INDEX audit_event_subject_idx ON audit_event (subject_ref, occurred_at) WHERE subject_ref <> '';

-- Append-only: no role may UPDATE or DELETE (E-SEC-12). Deleting whole expired days is a
-- separate retention role (RET-06), created when the retention job is built.
REVOKE ALL ON audit_event FROM PUBLIC;
REVOKE UPDATE, DELETE, TRUNCATE ON audit_event FROM mugizh_mod_audit;
GRANT SELECT, INSERT ON audit_event TO mugizh_mod_audit;

-- Daily anchors: the final hash of each day, also written to write-once object storage.
CREATE TABLE chain_anchor (
  partition_day date        PRIMARY KEY,
  last_seq      bigint      NOT NULL,
  last_hash     bytea       NOT NULL CHECK (length(last_hash) = 32),
  blob_key      text        NOT NULL,
  anchored_at   timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON chain_anchor FROM PUBLIC;
REVOKE UPDATE, DELETE, TRUNCATE ON chain_anchor FROM mugizh_mod_audit;
GRANT SELECT, INSERT ON chain_anchor TO mugizh_mod_audit;
