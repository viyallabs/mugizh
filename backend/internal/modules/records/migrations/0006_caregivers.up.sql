-- records module, part 6: caregiver access (v1.1 phase 1; P-CAREGIVER, E-FAM-02, TC-47).
-- References, scopes and times only: no health data, no names, no phone numbers.
-- The one-time code is never stored, only a keyed hash of it.
CREATE TABLE caregiver_invitation (
  id           uuid        PRIMARY KEY,
  patient_ref  uuid        NOT NULL,
  code_hash    bytea       NOT NULL UNIQUE,
  scopes       text[]      NOT NULL CHECK (cardinality(scopes) BETWEEN 1 AND 4 AND scopes <@ ARRAY['reminders','appointments','prescriptions','documents']::text[]),
  created_at   timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL CHECK (expires_at > created_at),
  redeemed_at  timestamptz,
  revoked_at   timestamptz
);
CREATE INDEX caregiver_invitation_patient_idx ON caregiver_invitation (patient_ref);

CREATE TABLE caregiver_link (
  id            uuid        PRIMARY KEY,
  patient_ref   uuid        NOT NULL,
  caregiver_ref uuid        NOT NULL,
  scopes        text[]      NOT NULL CHECK (cardinality(scopes) BETWEEN 1 AND 4 AND scopes <@ ARRAY['reminders','appointments','prescriptions','documents']::text[]),
  status        text        NOT NULL CHECK (status IN ('active','revoked')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  revoked_at    timestamptz,
  CHECK ((status = 'revoked') = (revoked_at IS NOT NULL)),
  CHECK (patient_ref <> caregiver_ref)
);
CREATE UNIQUE INDEX caregiver_link_one_active ON caregiver_link (patient_ref, caregiver_ref) WHERE status = 'active';
CREATE INDEX caregiver_link_caregiver_idx ON caregiver_link (caregiver_ref) WHERE status = 'active';
CREATE INDEX caregiver_link_patient_idx ON caregiver_link (patient_ref);
-- Links are revoked, never deleted (evidence for the patient's access log; RET-06).
REVOKE DELETE, TRUNCATE ON caregiver_link FROM mugizh_mod_records;

-- Wrong codes, for the guessing limit (5 per caregiver per hour). Purged after an hour.
CREATE TABLE caregiver_redeem_attempt (
  caregiver_ref uuid        NOT NULL,
  attempted_at  timestamptz NOT NULL
);
CREATE INDEX caregiver_redeem_attempt_idx ON caregiver_redeem_attempt (caregiver_ref, attempted_at);
