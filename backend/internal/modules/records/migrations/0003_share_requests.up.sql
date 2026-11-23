-- records module, part 3: doctor requests for more time on a history share (S-168, E-DP-02).
-- References, codes and times only: no health data and no free text. A request becomes a T4
-- grant only when the patient approves (consent-design §2); nothing here grants access.
CREATE TABLE share_request (
  id            uuid        PRIMARY KEY,
  patient_ref   uuid        NOT NULL,
  doctor_ref    uuid        NOT NULL,
  clinic_ref    uuid,
  status        text        NOT NULL CHECK (status IN ('pending','approved','declined','expired')),
  requested_at  timestamptz NOT NULL,
  expires_at    timestamptz NOT NULL CHECK (expires_at > requested_at),
  decided_at    timestamptz,
  grant_id      uuid,
  created_at    timestamptz NOT NULL DEFAULT now(),
  CHECK ((status = 'pending') = (decided_at IS NULL) OR status = 'expired')
);
-- At most one open request per patient and doctor (a repeat tap returns the same request).
CREATE UNIQUE INDEX share_request_one_pending ON share_request (patient_ref, doctor_ref) WHERE status = 'pending';
CREATE INDEX share_request_patient_idx ON share_request (patient_ref, requested_at);
-- Evidence of what was asked and decided: never deleted by the module role.
REVOKE DELETE, TRUNCATE ON share_request FROM mugizh_mod_records;
