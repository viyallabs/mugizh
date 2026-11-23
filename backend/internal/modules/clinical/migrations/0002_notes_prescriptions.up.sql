-- clinical module: consult notes and prescriptions with versions (S-139, S-149, S-150, S-164).
-- Health data (SH-C): note text, items, rule results, the document and the signature are
-- sealed with the care key of the patient and doctor (K-DP-03) and stored as bytea.
-- Signed versions are immutable (K-CS-05): a trigger refuses any UPDATE or DELETE of a
-- signed row, even for a superuser, and the module role has no DELETE or TRUNCATE at all.

CREATE FUNCTION clinical_signed_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'clinical records are never deleted' USING ERRCODE = '23001';
  END IF;
  IF OLD.state = 'signed' THEN
    RAISE EXCEPTION 'signed clinical records are immutable' USING ERRCODE = '23001';
  END IF;
  IF NEW.id <> OLD.id OR NEW.version <> OLD.version OR NEW.created_at <> OLD.created_at THEN
    RAISE EXCEPTION 'clinical record identity cannot change' USING ERRCODE = '23001';
  END IF;
  RETURN NEW;
END $$;

CREATE TABLE note (
  id              uuid        PRIMARY KEY,
  appointment_ref uuid        NOT NULL,
  patient_ref     uuid        NOT NULL,
  doctor_ref      uuid        NOT NULL,
  clinic_ref      uuid,
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (appointment_ref, doctor_ref)
);

CREATE TABLE note_version (
  id            uuid        PRIMARY KEY,
  note_id       uuid        NOT NULL REFERENCES note (id),
  version       int         NOT NULL CHECK (version >= 1),
  state         text        NOT NULL CHECK (state IN ('draft','signed')),
  content       bytea       NOT NULL,           -- sealed (care key): complaint, findings, diagnosis, advice
  has_diagnosis boolean     NOT NULL,           -- flag for rule P05; the text stays sealed
  rev           int         NOT NULL DEFAULT 1, -- bumped on every draft save
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  signed_at     timestamptz,
  UNIQUE (note_id, version),
  CHECK ((state = 'signed') = (signed_at IS NOT NULL))
);
CREATE UNIQUE INDEX note_one_draft_idx ON note_version (note_id) WHERE state = 'draft';
CREATE TRIGGER note_version_immutable BEFORE UPDATE OR DELETE ON note_version
  FOR EACH ROW EXECUTE FUNCTION clinical_signed_immutable();

CREATE TABLE prescription (
  id              uuid        PRIMARY KEY,
  appointment_ref uuid        NOT NULL,
  patient_ref     uuid        NOT NULL,
  doctor_ref      uuid        NOT NULL,
  clinic_ref      uuid,
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (appointment_ref, doctor_ref)
);

CREATE TABLE prescription_version (
  id                        uuid        PRIMARY KEY,
  prescription_id           uuid        NOT NULL REFERENCES prescription (id),
  version                   int         NOT NULL CHECK (version >= 1),
  state                     text        NOT NULL CHECK (state IN ('draft','signed')),
  content                   bytea       NOT NULL,  -- sealed: items (exactly as entered), tests, advice
  rev                       int         NOT NULL DEFAULT 1,
  created_at                timestamptz NOT NULL DEFAULT now(),
  updated_at                timestamptz NOT NULL DEFAULT now(),
  -- set once, at signing
  note_version_id           uuid        REFERENCES note_version (id),
  signed_at                 timestamptz,
  signed_by                 uuid,
  consult_mode              text,
  ruleset_version           text,
  ruleset_checksum          text,
  classification_version    text,
  rule_result               bytea,                 -- sealed: the full evaluation (RET-02)
  document                  bytea,                 -- sealed: canonical document bytes
  document_sha256           bytea,
  signature                 bytea,                 -- sealed: signature value and chain
  signing_provider          text,
  verification_hash         bytea       UNIQUE,    -- SHA-256 of the random reference; the token is only in the sealed document
  -- One draft becomes one signed version: the row itself, never copied, and versions are unique.
  UNIQUE (prescription_id, version),
  CHECK ((state = 'draft') OR (signed_at IS NOT NULL AND signed_by IS NOT NULL AND note_version_id IS NOT NULL
         AND rule_result IS NOT NULL AND document IS NOT NULL AND length(document_sha256) = 32 AND signature IS NOT NULL
         AND length(verification_hash) = 32 AND ruleset_version IS NOT NULL AND ruleset_checksum IS NOT NULL
         AND classification_version IS NOT NULL AND consult_mode IS NOT NULL))
);
CREATE UNIQUE INDEX prescription_one_draft_idx ON prescription_version (prescription_id) WHERE state = 'draft';
CREATE TRIGGER prescription_version_immutable BEFORE UPDATE OR DELETE ON prescription_version
  FOR EACH ROW EXECUTE FUNCTION clinical_signed_immutable();

-- Opaque links between a scheduler-core activity and a prescription item (no health text).
CREATE TABLE reminder_link (
  external_ref            uuid        PRIMARY KEY,   -- sent to scheduler-core as the activity external ref
  prescription_version_id uuid        NOT NULL REFERENCES prescription_version (id),
  patient_ref             uuid        NOT NULL,
  item_ref                text        NOT NULL,
  step                    int         NOT NULL DEFAULT 0,
  kind                    text        NOT NULL CHECK (kind IN ('med','test')),
  activity_id             uuid        NOT NULL,
  state                   text        NOT NULL DEFAULT 'active' CHECK (state IN ('active','cancelled')),
  created_at              timestamptz NOT NULL DEFAULT now(),
  cancelled_at            timestamptz
);
CREATE INDEX reminder_link_version_idx ON reminder_link (prescription_version_id);

-- What the follow-up of a signed version did about reminders (offer once, S-155).
CREATE TABLE reminder_run (
  prescription_version_id uuid        PRIMARY KEY REFERENCES prescription_version (id),
  outcome                 text        NOT NULL CHECK (outcome IN ('scheduled','consent_missing','none')),
  created_at              timestamptz NOT NULL DEFAULT now()
);

REVOKE DELETE, TRUNCATE ON note, note_version, prescription, prescription_version, reminder_link, reminder_run FROM mugizh_mod_clinical;
REVOKE UPDATE ON note, prescription, reminder_run FROM mugizh_mod_clinical;
