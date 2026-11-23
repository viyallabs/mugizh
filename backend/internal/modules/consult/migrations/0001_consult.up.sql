-- consult module (S-135, S-136, S-137, S-147, S-148; data inventory 4.7; K-ARC-04).
-- References, times (UTC), codes and booleans only. No names, no clinical content, no
-- tokens (join tokens are never stored) and never a recording (ADR-13).

CREATE TABLE session (
  id                    uuid        PRIMARY KEY,
  appointment_ref       uuid        NOT NULL UNIQUE,
  patient_ref           uuid        NOT NULL,
  doctor_ref            uuid        NOT NULL,
  clinic_ref            uuid        NOT NULL,
  booked_mode           text        NOT NULL CHECK (booked_mode IN ('video','audio')),
  -- The mode right now; its value at signing is the "mode at signing" (E-RX-06).
  current_mode          text        NOT NULL CHECK (current_mode IN ('video','audio','in_person')),
  -- scheduled -> open -> both_connected -> ended | failed_technical | no_show; cancelled when
  -- the appointment is cancelled before the call ended.
  state                 text        NOT NULL DEFAULT 'scheduled' CHECK (state IN ('scheduled','open','both_connected','ended','failed_technical','no_show','cancelled')),
  outcome               text        NOT NULL DEFAULT '' CHECK (outcome IN ('','completed','converted_in_person','patient_no_show','doctor_no_show','technical_failure','minor_stopped')),
  slot_start            timestamptz NOT NULL,
  slot_end              timestamptz NOT NULL,
  provider_room_ref     text        NOT NULL DEFAULT '',
  opened_at             timestamptz,
  both_connected_at     timestamptz,
  ended_at              timestamptz,
  identity_confirmed    boolean     NOT NULL DEFAULT false,
  identity_confirmed_at timestamptz,
  identity_confirmed_by uuid,
  short_call            boolean     NOT NULL DEFAULT false,
  minor_flagged         boolean     NOT NULL DEFAULT false,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),
  CHECK (slot_end > slot_start)
);
CREATE INDEX session_open_idx ON session (slot_start) WHERE state IN ('scheduled','open','both_connected');

-- The two participants. participant_ref is the opaque reference the vendor sees (never the user ID).
CREATE TABLE participant (
  session_id      uuid        NOT NULL REFERENCES session (id),
  role            text        NOT NULL CHECK (role IN ('patient','doctor')),
  user_ref        uuid        NOT NULL,
  participant_ref uuid        NOT NULL UNIQUE,
  tokens_issued   int         NOT NULL DEFAULT 0,
  last_token_at   timestamptz,
  PRIMARY KEY (session_id, role)
);

-- Join and leave events from the vendor (webhook or poll). One row per vendor event ID.
CREATE TABLE participant_event (
  session_id uuid        NOT NULL REFERENCES session (id),
  event_id   text        NOT NULL CHECK (length(event_id) BETWEEN 1 AND 100),
  role       text        NOT NULL CHECK (role IN ('patient','doctor')),
  kind       text        NOT NULL CHECK (kind IN ('joined','left')),
  at         timestamptz NOT NULL,
  PRIMARY KEY (session_id, event_id)
);

-- Pre-join: the device check and the teleconsultation consent for this consult (C09).
CREATE TABLE pre_join (
  session_id           uuid        PRIMARY KEY REFERENCES session (id),
  consent_text_id      text        NOT NULL,
  consent_text_version int         NOT NULL,
  in_country_declared  boolean     NOT NULL,
  camera_ok            boolean     NOT NULL,
  microphone_ok        boolean     NOT NULL,
  network_ok           boolean     NOT NULL,
  accepted_at          timestamptz NOT NULL
);

CREATE TABLE mode_change (
  id         uuid        PRIMARY KEY,
  session_id uuid        NOT NULL REFERENCES session (id),
  from_mode  text        NOT NULL CHECK (from_mode IN ('video','audio','in_person')),
  to_mode    text        NOT NULL CHECK (to_mode IN ('video','audio','in_person')),
  reason     text        NOT NULL CHECK (reason IN ('network','device','user_choice','room_fallback','doctor_conversion')),
  changed_by uuid,
  changed_at timestamptz NOT NULL
);
CREATE INDEX mode_change_session_idx ON mode_change (session_id, changed_at, id);

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
