-- records module, part 2: data rights (S-143), uploads (S-159), secure file links (S-171).
-- Data-rights design: docs/security/data-rights-flows.md. Retention: RET-09 (requests),
-- RET-03 (uploads), RET-18 (legal-minimum hold).

-- One row per request. References, codes and times only; free text (correction details,
-- nominee) is sealed with the patient key (K-DP-03).
CREATE TABLE rights_request (
  id              uuid        PRIMARY KEY,
  subject_ref     uuid        NOT NULL,
  type            text        NOT NULL CHECK (type IN ('access','export','correction','erasure','nomination')),
  status          text        NOT NULL CHECK (status IN ('received','in_progress','restricted','finalising','completed','rejected','cancelled')),
  received_at     timestamptz NOT NULL,
  ack_due_at      timestamptz NOT NULL,
  due_at          timestamptz NOT NULL,
  acknowledged_at timestamptz,
  completed_at    timestamptz,
  cancelled_at    timestamptz,
  outcome         text        NOT NULL DEFAULT '' CHECK (outcome ~ '^[a-z_]{0,40}$'),
  details_sealed  bytea,
  -- erasure (two steps; undo window; RET-18 hold)
  step            int         NOT NULL DEFAULT 1 CHECK (step IN (1,2)),
  undo_until      timestamptz,
  restricted_at   timestamptz,
  hold_ends_at    timestamptz,
  deleted_at      timestamptz,
  -- export
  export_blob_key  text,
  export_ready_at  timestamptz,
  export_expires_at timestamptz,
  report          jsonb       NOT NULL DEFAULT '[]'::jsonb,
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX rights_request_subject_idx ON rights_request (subject_ref, received_at);
CREATE INDEX rights_request_open_idx ON rights_request (status) WHERE status NOT IN ('completed','rejected','cancelled');
-- One live erasure per person.
CREATE UNIQUE INDEX rights_request_one_erasure ON rights_request (subject_ref)
  WHERE type = 'erasure' AND status IN ('received','in_progress','restricted','finalising');
-- Evidence (RET-09): requests are never deleted by the module role.
REVOKE DELETE, TRUNCATE ON rights_request FROM mugizh_mod_records;

-- One task per module and step; each module reports rights.task_done.
CREATE TABLE rights_task (
  request_id     uuid        NOT NULL REFERENCES rights_request (id),
  step           int         NOT NULL CHECK (step IN (1,2)),
  module         text        NOT NULL CHECK (module ~ '^[a-z_]{1,40}$'),
  outcome        text        NOT NULL DEFAULT '' CHECK (outcome ~ '^[a-z_]{0,40}$'),
  retention_rule text        NOT NULL DEFAULT '' CHECK (retention_rule ~ '^[A-Z0-9\-]{0,20}$'),
  done_at        timestamptz,
  PRIMARY KEY (request_id, step, module)
);
REVOKE DELETE, TRUNCATE ON rights_task FROM mugizh_mod_records;

-- An account under erasure: its data is hidden from the app and every staff role.
-- Released only by an undo inside the window (no row is deleted).
CREATE TABLE account_restriction (
  subject_ref uuid        PRIMARY KEY,
  request_id  uuid        NOT NULL,
  restricted_at timestamptz NOT NULL,
  released_at timestamptz
);
REVOKE DELETE, TRUNCATE ON account_restriction FROM mugizh_mod_records;

-- Patient uploads. The file is sealed with the patient key before it reaches object
-- storage; the title is sealed too (SH-C).
CREATE TABLE document (
  id           uuid        PRIMARY KEY,
  patient_ref  uuid        NOT NULL,
  title_sealed bytea       NOT NULL,
  content_type text        NOT NULL CHECK (content_type IN ('application/pdf','image/jpeg','image/png')),
  size_bytes   int         NOT NULL CHECK (size_bytes > 0),
  scan_result  text        NOT NULL CHECK (scan_result = 'clean'),
  blob_key     text        NOT NULL,
  created_at   timestamptz NOT NULL
);
CREATE INDEX document_patient_idx ON document (patient_ref, created_at);

-- Short-lived, single-use, user-bound download links. Only the keyed hash of the token
-- is stored (S-171, encryption-design §8).
CREATE TABLE file_link (
  token_hash  bytea       PRIMARY KEY CHECK (length(token_hash) = 32),
  user_ref    uuid        NOT NULL,
  patient_ref uuid        NOT NULL,
  kind        text        NOT NULL CHECK (kind IN ('document','export')),
  target_ref  uuid        NOT NULL,
  expires_at  timestamptz NOT NULL,
  used_at     timestamptz,
  created_at  timestamptz NOT NULL
);
CREATE INDEX file_link_expiry_idx ON file_link (expires_at);
