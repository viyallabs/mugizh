-- verification module: professional verification cases, evidence, checks and decisions
-- (S-109, S-110, S-129, S-160; data inventory §4.4). Subject is a doctor now, a clinic later.
-- No prohibited identity numbers are stored anywhere (K-DP-05).

CREATE TABLE verification_case (
  id                  uuid        PRIMARY KEY,
  subject_kind        text        NOT NULL CHECK (subject_kind IN ('doctor','clinic')),
  subject_ref         uuid        NOT NULL,
  status              text        NOT NULL CHECK (status IN ('draft','submitted','checking','needs_info','verified','rejected','suspended','expired','re_verify_due')),
  -- Workflow of the current submission round. For a first application the status mirrors it;
  -- for a re-verification round the standing (status) stays until the decision.
  round               int         NOT NULL DEFAULT 1 CHECK (round > 0),
  round_state         text        NOT NULL DEFAULT 'draft' CHECK (round_state IN ('draft','submitted','checking','needs_info','decided')),
  council_code        text        NOT NULL DEFAULT '' CHECK (length(council_code) <= 60),
  registration_number text        NOT NULL DEFAULT '' CHECK (length(registration_number) <= 40),
  registration_key    text        NOT NULL DEFAULT '' CHECK (length(registration_key) <= 40),
  registration_year   int         CHECK (registration_year IS NULL OR registration_year BETWEEN 1900 AND 2200),
  registered_name     text        NOT NULL DEFAULT '' CHECK (length(registered_name) <= 120),
  qualifications      text[]      NOT NULL DEFAULT '{}',
  specialty_claim     text        NOT NULL DEFAULT '' CHECK (length(specialty_claim) <= 80),
  specialty_confirmed boolean     NOT NULL DEFAULT false,
  id_type             text        NOT NULL DEFAULT '' CHECK (length(id_type) <= 40),
  auto_check          text        NOT NULL DEFAULT '' CHECK (auto_check IN ('','passed','failed')),
  manual_flags        text[]      NOT NULL DEFAULT '{}',
  info_requested      text        NOT NULL DEFAULT '' CHECK (length(info_requested) <= 60),
  pending_outcome     text        NOT NULL DEFAULT '' CHECK (pending_outcome IN ('','verified','rejected')),
  pending_by          uuid,
  pending_reason      text        NOT NULL DEFAULT '',
  licence_expires_on  date,
  verified_at         timestamptz,
  re_verify_due_at    timestamptz,
  decided_at          timestamptz,
  submitted_at        timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (subject_kind, subject_ref),
  CHECK ((pending_outcome = '') = (pending_by IS NULL))
);
CREATE INDEX verification_case_queue_idx ON verification_case (submitted_at, id) WHERE round_state IN ('submitted','checking');
CREATE INDEX verification_case_regkey_idx ON verification_case (registration_key) WHERE registration_key <> '';
CREATE INDEX verification_case_due_idx ON verification_case (re_verify_due_at) WHERE status IN ('verified','re_verify_due');

-- ID and certificate images. The bytes live encrypted in object storage under a key that
-- belongs to this one evidence item, so it can be destroyed alone (RET-08, hard delete).
CREATE TABLE evidence (
  id           uuid        PRIMARY KEY,
  case_id      uuid        NOT NULL REFERENCES verification_case (id),
  subject_ref  uuid        NOT NULL,
  round        int         NOT NULL,
  kind         text        NOT NULL CHECK (kind ~ '^[a-z_]{1,40}$'),
  content_type text        NOT NULL CHECK (content_type IN ('image/jpeg','image/png','application/pdf')),
  size_bytes   int         NOT NULL CHECK (size_bytes > 0),
  blob_key     text        NOT NULL,
  key_owner    text        NOT NULL,
  state        text        NOT NULL DEFAULT 'stored' CHECK (state IN ('stored','deleted','purged')),
  delete_reason text       NOT NULL DEFAULT '',
  purge_after  timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  deleted_at   timestamptz
);
CREATE INDEX evidence_case_idx ON evidence (case_id, round);
CREATE INDEX evidence_purge_idx ON evidence (purge_after) WHERE state = 'stored';

-- Register lookup results. The registered name is sealed (RG-08); codes are plain.
CREATE TABLE check_result (
  id          uuid        PRIMARY KEY,
  case_id     uuid        NOT NULL REFERENCES verification_case (id),
  round       int         NOT NULL,
  source      text        NOT NULL CHECK (source IN ('registry','duplicate','name')),
  outcome     text        NOT NULL CHECK (outcome ~ '^[a-z_]{1,40}$'),
  detail_ct   bytea,
  checked_at  timestamptz NOT NULL
);
CREATE INDEX check_result_case_idx ON check_result (case_id, round);
REVOKE UPDATE, DELETE, TRUNCATE ON check_result FROM mugizh_mod_verification;

-- Append-only decision log: reviewer, outcome, reason code, four-eyes link (RET-08).
CREATE TABLE decision (
  id            uuid        PRIMARY KEY,
  case_id       uuid        NOT NULL REFERENCES verification_case (id),
  subject_ref   uuid        NOT NULL,
  round         int         NOT NULL,
  reviewer_ref  uuid        NOT NULL,
  outcome       text        NOT NULL CHECK (outcome IN ('verified','rejected','needs_info','suspended','prohibited_id_received')),
  stage         text        NOT NULL CHECK (stage IN ('proposed','final')),
  reason_code   text        NOT NULL CHECK (reason_code ~ '^[a-z_]{1,60}$'),
  override      boolean     NOT NULL DEFAULT false,
  confirms_id   uuid        REFERENCES decision (id),
  created_at    timestamptz NOT NULL DEFAULT now(),
  -- A reviewer never decides their own case, in the database as well as in code.
  CHECK (reviewer_ref <> subject_ref)
);
CREATE INDEX decision_case_idx ON decision (case_id, created_at);
REVOKE UPDATE, DELETE, TRUNCATE ON decision FROM mugizh_mod_verification;

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
