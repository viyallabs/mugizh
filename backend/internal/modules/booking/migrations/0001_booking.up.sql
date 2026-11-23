-- booking module (S-113, S-121, S-122, S-123, S-131, S-144; K-PAY-02, K-PAY-05, K-ARC-04, K-DP-10).
-- References, times (UTC) and zones only. No health data, no names, no contact details.
-- The fee is copied from the practice at hold time; the refund policy version is stored
-- with every appointment (K-PAY-05).

-- One row per practice that has availability. Refs and the practice zone are copied from
-- the directory at set-up time and refreshed on practice.updated.
CREATE TABLE availability (
  practice_id      uuid        PRIMARY KEY,
  clinic_ref       uuid        NOT NULL,
  professional_ref uuid        NOT NULL,
  time_zone        text        NOT NULL CHECK (length(time_zone) BETWEEN 1 AND 64),
  updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX availability_clinic_idx ON availability (clinic_ref);
CREATE INDEX availability_professional_idx ON availability (professional_ref);

-- Weekly template: local wall times in the practice zone. weekday 0 = Sunday .. 6 = Saturday.
CREATE TABLE availability_rule (
  id            uuid     PRIMARY KEY,
  practice_id   uuid     NOT NULL REFERENCES availability (practice_id) ON DELETE CASCADE,
  weekday       smallint NOT NULL CHECK (weekday BETWEEN 0 AND 6),
  start_local   time     NOT NULL,
  end_local     time     NOT NULL,
  slot_minutes  int      NOT NULL CHECK (slot_minutes BETWEEN 5 AND 240),
  mode          text     NOT NULL CHECK (mode IN ('in_person','video','audio')),
  CHECK (end_local > start_local)
);
CREATE INDEX availability_rule_practice_idx ON availability_rule (practice_id);

-- Leave and closure windows (absolute instants, computed from local dates in the practice zone).
CREATE TABLE availability_exception (
  id          uuid        PRIMARY KEY,
  practice_id uuid        NOT NULL REFERENCES availability (practice_id) ON DELETE CASCADE,
  kind        text        NOT NULL CHECK (kind IN ('leave','closure')),
  starts_at   timestamptz NOT NULL,
  ends_at     timestamptz NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  CHECK (ends_at > starts_at)
);
CREATE INDEX availability_exception_practice_idx ON availability_exception (practice_id, starts_at);

-- Materialised slots (30 days ahead). Unique (practice, start). A slot is taken by exactly
-- one appointment: the take is a single UPDATE ... WHERE state = 'open' (TC-22).
CREATE TABLE slot (
  id             uuid        PRIMARY KEY,
  practice_id    uuid        NOT NULL,
  start_at       timestamptz NOT NULL,
  end_at         timestamptz NOT NULL,
  mode           text        NOT NULL CHECK (mode IN ('in_person','video','audio')),
  state          text        NOT NULL DEFAULT 'open' CHECK (state IN ('open','taken')),
  appointment_id uuid,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (practice_id, start_at),
  CHECK (end_at > start_at),
  CHECK ((state = 'taken') = (appointment_id IS NOT NULL))
);
CREATE INDEX slot_open_idx ON slot (practice_id, start_at) WHERE state = 'open';

CREATE TABLE appointment (
  id                           uuid        PRIMARY KEY,
  patient_ref                  uuid        NOT NULL,
  practice_id                  uuid        NOT NULL,
  professional_ref             uuid        NOT NULL,
  clinic_ref                   uuid        NOT NULL,
  slot_id                      uuid        NOT NULL,
  start_at                     timestamptz NOT NULL,
  end_at                       timestamptz NOT NULL,
  mode                         text        NOT NULL CHECK (mode IN ('in_person','video','audio')),
  practice_time_zone           text        NOT NULL,
  patient_time_zone            text        NOT NULL,
  -- P10: set for video and audio only; in-person bookings from abroad are allowed.
  patient_in_india_confirmed   boolean,
  fee_minor                    bigint      NOT NULL CHECK (fee_minor >= 0),
  currency                     text        NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  fee_version                  text        NOT NULL,
  policy_version               text        NOT NULL CHECK (length(policy_version) BETWEEN 1 AND 40),
  settlement_mode              text        NOT NULL CHECK (settlement_mode IN ('clinic_collected','pay_at_clinic')),
  state                        text        NOT NULL CHECK (state IN ('held','pending_payment','confirmed','in_progress','completed','cancelled','no_show','expired')),
  cancelled_by                 text        CHECK (cancelled_by IN ('patient','doctor','clinic','system')),
  cancel_rule                  text        NOT NULL DEFAULT '',
  no_show_by                   text        CHECK (no_show_by IN ('patient','doctor')),
  order_ref                    text,
  reschedule_count             int         NOT NULL DEFAULT 0,
  needs_reschedule             boolean     NOT NULL DEFAULT false,
  payment_failed_at            timestamptz,
  created_at                   timestamptz NOT NULL,
  updated_at                   timestamptz NOT NULL,
  confirmed_at                 timestamptz,
  arrived_at                   timestamptz,
  completed_at                 timestamptz,
  cancelled_at                 timestamptz,
  CHECK (end_at > start_at),
  CHECK (mode = 'in_person' OR patient_in_india_confirmed IS TRUE),
  CHECK (state <> 'cancelled' OR cancelled_by IS NOT NULL),
  CHECK (state <> 'no_show' OR no_show_by IS NOT NULL)
);
CREATE INDEX appointment_patient_idx ON appointment (patient_ref, start_at DESC, id DESC);
CREATE INDEX appointment_practice_idx ON appointment (practice_id, start_at, id);
CREATE INDEX appointment_professional_idx ON appointment (professional_ref, start_at) WHERE state IN ('held','pending_payment','confirmed');
CREATE INDEX appointment_clinic_idx ON appointment (clinic_ref, start_at) WHERE state IN ('held','pending_payment','confirmed');

-- Active hold: exists only while the hold is live (RET-12, DEL-TTL). One per slot.
CREATE TABLE hold (
  hold_id        uuid        PRIMARY KEY,
  slot_id        uuid        NOT NULL UNIQUE,
  appointment_id uuid        NOT NULL UNIQUE REFERENCES appointment (id),
  patient_ref    uuid        NOT NULL,
  expires_at     timestamptz NOT NULL,
  created_at     timestamptz NOT NULL
);
CREATE INDEX hold_patient_idx ON hold (patient_ref);
CREATE INDEX hold_expiry_idx ON hold (expires_at);

-- E-BK-07: the clinic confirms the doctor will be there, the day before.
CREATE TABLE reconfirmation_task (
  id             uuid        PRIMARY KEY,
  appointment_id uuid        NOT NULL UNIQUE REFERENCES appointment (id),
  clinic_ref     uuid        NOT NULL,
  due_at         timestamptz NOT NULL,
  state          text        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','overdue','reconfirmed','void')),
  reconfirmed_by uuid,
  reconfirmed_at timestamptz,
  created_at     timestamptz NOT NULL
);
CREATE INDEX reconfirmation_due_idx ON reconfirmation_task (due_at) WHERE state IN ('pending','overdue');
CREATE INDEX reconfirmation_clinic_idx ON reconfirmation_task (clinic_ref, due_at) WHERE state IN ('pending','overdue');

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
