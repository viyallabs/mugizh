-- admin module: just-in-time staff access (S-128) and the grievance queue (S-142).
-- Retention: access requests RET-06, grievances and evidence RET-09 (data inventory §4.11).

-- A staff member asks for access to one patient's data; a security owner who is not the
-- requester approves; records creates the grant.
CREATE TABLE access_request (
  id                 uuid        PRIMARY KEY,
  staff_ref          uuid        NOT NULL,
  staff_role         text        NOT NULL CHECK (staff_role IN ('support','ops','grievance_officer','security_owner')),
  patient_ref        uuid        NOT NULL,
  kinds              text[]      NOT NULL CHECK (cardinality(kinds) > 0),
  reason_code        text        NOT NULL CHECK (reason_code IN ('complaint','rights_request','payment_dispute','security_incident','legal_order')),
  ticket_ref         text        NOT NULL CHECK (ticket_ref ~ '^[A-Za-z0-9_\-]{1,40}$'),
  duration_seconds   int         NOT NULL CHECK (duration_seconds > 0),
  break_glass        boolean     NOT NULL DEFAULT false,
  status             text        NOT NULL CHECK (status IN ('pending','approved','rejected','expired')),
  requested_at       timestamptz NOT NULL,
  decided_at         timestamptz,
  approver_ref       uuid,
  decision_reason    text        NOT NULL DEFAULT '' CHECK (decision_reason ~ '^[a-z_]{0,40}$'),
  grant_id           uuid,
  grant_expires_at   timestamptz,
  -- break-glass: a written justification is due within 24 h (access-control-design §5)
  justification_due_at timestamptz,
  justification_sealed bytea,
  justified_at       timestamptz,
  CHECK (status <> 'approved' OR approver_ref IS NOT NULL OR break_glass),
  CHECK (approver_ref IS NULL OR approver_ref <> staff_ref)
);
CREATE INDEX access_request_requested_idx ON access_request (requested_at);
CREATE INDEX access_request_status_idx ON access_request (status) WHERE status = 'pending';
REVOKE DELETE, TRUNCATE ON access_request FROM mugizh_mod_admin;

-- Evidence that the security owner reviewed a month of staff access.
CREATE TABLE access_review (
  month        date        PRIMARY KEY,
  reviewer_ref uuid        NOT NULL,
  reviewed_at  timestamptz NOT NULL,
  row_count    int         NOT NULL
);
REVOKE DELETE, TRUNCATE ON access_review FROM mugizh_mod_admin;

-- The grievance and lawful-request queue with legal timers (grievance-redressal.md).
CREATE TABLE grievance (
  id             uuid        PRIMARY KEY,
  reference      text        NOT NULL UNIQUE CHECK (reference ~ '^[A-Z]{2}-[0-9A-F]{10}$'),
  kind           text        NOT NULL CHECK (kind IN ('complaint','content_takedown','order_takedown','intimate_image','lawful_request','medical_record_copy','data_rights')),
  status         text        NOT NULL CHECK (status IN ('open','acknowledged','resolved')),
  submitter_ref  uuid,
  source_ref     text        NOT NULL DEFAULT '' CHECK (source_ref ~ '^[A-Za-z0-9_:\-]{0,80}$'),
  text_sealed    bytea,
  received_at    timestamptz NOT NULL,
  ack_due_at     timestamptz NOT NULL,
  due_at         timestamptz NOT NULL,
  acknowledged_at timestamptz,
  resolved_at    timestamptz,
  outcome        text        NOT NULL DEFAULT '' CHECK (outcome ~ '^[a-z_]{0,40}$'),
  created_at     timestamptz NOT NULL DEFAULT now(),
  CHECK (due_at >= received_at)
);
CREATE INDEX grievance_open_idx ON grievance (due_at) WHERE status <> 'resolved';
CREATE UNIQUE INDEX grievance_source_once ON grievance (kind, source_ref) WHERE source_ref <> '';
REVOKE DELETE, TRUNCATE ON grievance FROM mugizh_mod_admin;

-- Append-only evidence trail (RET-09): who did what, when.
CREATE TABLE grievance_event (
  id           uuid        PRIMARY KEY,
  grievance_id uuid        NOT NULL REFERENCES grievance (id),
  action       text        NOT NULL CHECK (action ~ '^[a-z_]{1,40}$'),
  actor_ref    text        NOT NULL CHECK (length(actor_ref) BETWEEN 1 AND 80),
  outcome      text        NOT NULL DEFAULT '' CHECK (outcome ~ '^[a-z_]{0,40}$'),
  occurred_at  timestamptz NOT NULL
);
CREATE INDEX grievance_event_idx ON grievance_event (grievance_id, occurred_at);
REVOKE UPDATE, DELETE, TRUNCATE ON grievance_event FROM mugizh_mod_admin;

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
