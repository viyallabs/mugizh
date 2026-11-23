-- records module: purpose consent and access permissions (S-097, consent-design §2).

-- Append-only consent log (RET-05). The module role may not change or delete it.
CREATE TABLE consent_record (
  id               uuid        PRIMARY KEY,
  subject_ref      uuid        NOT NULL,
  given_by_ref     uuid        NOT NULL,
  on_behalf_of_ref uuid,
  purpose_id       text        NOT NULL CHECK (purpose_id ~ '^[PD]-[A-Z]+$'),
  text_version     int         NOT NULL CHECK (text_version > 0),
  notice_version   text        NOT NULL CHECK (notice_version ~ '^[A-Za-z0-9.\-]{1,40}$'),
  action           text        NOT NULL CHECK (action IN ('grant','withdraw')),
  channel          text        NOT NULL CHECK (channel IN ('app','web','paper','consent_manager')),
  evidence_ref     text,
  occurred_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX consent_record_subject_idx ON consent_record (subject_ref, purpose_id, occurred_at);
REVOKE UPDATE, DELETE, TRUNCATE ON consent_record FROM mugizh_mod_records;

-- Current value per (subject, purpose), derived from the log in the same transaction.
CREATE TABLE consent_state (
  subject_ref  uuid        NOT NULL,
  purpose_id   text        NOT NULL,
  granted      boolean     NOT NULL,
  text_version int         NOT NULL,
  updated_at   timestamptz NOT NULL,
  PRIMARY KEY (subject_ref, purpose_id)
);

-- Access permissions: tiers T1-T4 and staff just-in-time / break-glass grants.
CREATE TABLE permission_grant (
  id           uuid        PRIMARY KEY,
  patient_ref  uuid        NOT NULL,
  grantee_ref  uuid        NOT NULL,
  clinic_ref   uuid,
  tier         text        NOT NULL CHECK (tier IN ('T1_current_care','T2_own_records','T3_history_share','T4_extension','jit_staff','break_glass')),
  scope_ref    text        NOT NULL DEFAULT '' CHECK (length(scope_ref) <= 80),
  kinds        text[]      NOT NULL DEFAULT '{}',
  basis        text        NOT NULL CHECK (basis ~ '^[A-Za-z_\-]{1,40}$'),
  starts_at    timestamptz NOT NULL,
  expires_at   timestamptz,
  revoked_at   timestamptz,
  revoked_by   uuid,
  approved_by  uuid,
  reason_code  text        NOT NULL DEFAULT '',
  created_from text        NOT NULL DEFAULT '',
  created_at   timestamptz NOT NULL DEFAULT now(),
  CHECK (expires_at IS NULL OR expires_at > starts_at),
  -- Staff grants always expire and are approved by someone else (K-SEC-08).
  CHECK (tier NOT IN ('jit_staff','break_glass') OR expires_at IS NOT NULL),
  CHECK (tier <> 'jit_staff' OR (approved_by IS NOT NULL AND approved_by <> grantee_ref)),
  -- Only T2 may be open-ended (legal record duty, RET-02).
  CHECK (tier = 'T2_own_records' OR expires_at IS NOT NULL)
);
CREATE INDEX permission_grant_patient_idx ON permission_grant (patient_ref) WHERE revoked_at IS NULL;
CREATE UNIQUE INDEX permission_grant_t1_once ON permission_grant (patient_ref, grantee_ref, scope_ref) WHERE tier = 'T1_current_care';
CREATE UNIQUE INDEX permission_grant_t2_once ON permission_grant (patient_ref, grantee_ref, coalesce(clinic_ref, '00000000-0000-0000-0000-000000000000'::uuid)) WHERE tier = 'T2_own_records' AND revoked_at IS NULL;
-- Grants are revoked, never deleted (evidence for the patient's access log).
REVOKE DELETE, TRUNCATE ON permission_grant FROM mugizh_mod_records;

CREATE TABLE event_outbox (
  event_id    uuid        PRIMARY KEY,
  type        text        NOT NULL,
  version     int         NOT NULL DEFAULT 1,
  occurred_at timestamptz NOT NULL,
  subject_ref text        NOT NULL DEFAULT '',
  payload     jsonb       NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX event_outbox_occurred_idx ON event_outbox (occurred_at, event_id);
CREATE TABLE processed_event (
  handler      text        NOT NULL,
  event_id     uuid        NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (handler, event_id)
);
