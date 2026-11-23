-- Review M5: a clinic's lead-doctor link and a practice created for a doctor other than the
-- creator count for listing only after that doctor accepts. Rows that exist before this
-- migration were created under the old rule and stay accepted.
ALTER TABLE practice ADD COLUMN doctor_accepted boolean NOT NULL DEFAULT true;
ALTER TABLE clinic   ADD COLUMN lead_accepted   boolean NOT NULL DEFAULT true;

CREATE TABLE doctor_invitation (
  id              uuid        PRIMARY KEY,
  kind            text        NOT NULL CHECK (kind IN ('practice','clinic_lead')),
  professional_id uuid        NOT NULL REFERENCES professional (id),
  clinic_id       uuid        NOT NULL REFERENCES clinic (id),
  practice_id     uuid        REFERENCES practice (id),
  status          text        NOT NULL CHECK (status IN ('pending_doctor','accepted','declined')),
  created_by      uuid        NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  decided_at      timestamptz,
  CHECK ((kind = 'practice') = (practice_id IS NOT NULL))
);
CREATE INDEX doctor_invitation_doctor_idx ON doctor_invitation (professional_id, status);
CREATE UNIQUE INDEX doctor_invitation_practice_once ON doctor_invitation (practice_id) WHERE practice_id IS NOT NULL;
CREATE UNIQUE INDEX doctor_invitation_lead_once ON doctor_invitation (clinic_id) WHERE kind = 'clinic_lead';
REVOKE DELETE, TRUNCATE ON doctor_invitation FROM mugizh_mod_directory;

-- photo_ref and registration_doc_ref are opaque upload IDs (UUIDs), never paths or URLs.
-- NOT VALID: older rows are left alone, every new or changed row must comply.
ALTER TABLE professional DROP CONSTRAINT professional_photo_ref_check;
ALTER TABLE professional ADD CONSTRAINT professional_photo_ref_check
  CHECK (photo_ref ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') NOT VALID;
ALTER TABLE clinic DROP CONSTRAINT clinic_registration_doc_ref_check;
ALTER TABLE clinic ADD CONSTRAINT clinic_registration_doc_ref_check
  CHECK (registration_doc_ref ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') NOT VALID;
