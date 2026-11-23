-- reviews module (S-209 backend, S-214; data inventory 4.13; K-DP-03, K-DP-07).
-- References, times, a 1-5 rating and the sealed text only. The review text is sealed with the
-- patient's key and never stored in plaintext. Nothing here comes from payments (CD-05).

-- Eligibility read model (rule B5): one row per completed appointment, built from booking's
-- appointment.completed event. Only references and the completion time.
CREATE TABLE visit (
  appointment_ref uuid        PRIMARY KEY,
  patient_ref     uuid        NOT NULL,
  doctor_ref      uuid        NOT NULL,
  practice_ref    uuid        NOT NULL,
  completed_at    timestamptz NOT NULL
);
CREATE INDEX visit_patient_idx ON visit (patient_ref);

-- One review per visit. The text is sealed with the patient key (AAD: table, column, row ID).
-- Text state: none (a rating only) or pending_moderation (S-213 moderation is human and does
-- not exist yet, so no text is published).
CREATE TABLE review (
  id              uuid        PRIMARY KEY,
  appointment_ref uuid        NOT NULL UNIQUE REFERENCES visit (appointment_ref),
  patient_ref     uuid        NOT NULL,
  doctor_ref      uuid        NOT NULL,
  rating          smallint    NOT NULL CHECK (rating BETWEEN 1 AND 5),
  text_sealed     bytea,
  text_state      text        NOT NULL DEFAULT 'none' CHECK (text_state IN ('none','pending_moderation')),
  created_at      timestamptz NOT NULL,
  CHECK ((text_state = 'none') = (text_sealed IS NULL))
);
CREATE INDEX review_patient_idx ON review (patient_ref, created_at DESC, id DESC);

-- The doctor's rating in counts: kept in the same transaction as create and withdraw.
CREATE TABLE doctor_rating (
  doctor_ref uuid   PRIMARY KEY,
  n          int    NOT NULL DEFAULT 0 CHECK (n >= 0),
  sum        bigint NOT NULL DEFAULT 0 CHECK (sum >= 0)
);

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
