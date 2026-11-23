-- directory module: doctor and clinic profiles, practices, locality table and the
-- verification read model (S-112, S-117, S-119, S-120; module-boundaries B5). Public
-- business data only (class PUB) except registration document references (PER).

-- A doctor's public profile. id IS the professional reference used in verification events
-- and equals the doctor's user ID, so no mapping table is needed.
CREATE TABLE professional (
  id           uuid        PRIMARY KEY,
  display_name text        NOT NULL CHECK (length(display_name) BETWEEN 2 AND 80),
  -- RG-04: set only while a recognised postgraduate qualification for it is recorded
  -- (checked in the same transaction by the module).
  specialty    text        CHECK (specialty ~ '^[a-z][a-z0-9_]{1,39}$'),
  languages    text[]      NOT NULL DEFAULT '{}' CHECK (cardinality(languages) <= 10),
  photo_ref    text        CHECK (photo_ref ~ '^[A-Za-z0-9_./\-]{1,200}$'),
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Recognised qualifications only. Rows are added by the verification wiring or the admin
-- console through the module's RecordQualification; a doctor cannot add one.
CREATE TABLE qualification (
  id              uuid        PRIMARY KEY,
  professional_id uuid        NOT NULL REFERENCES professional (id),
  degree          text        NOT NULL CHECK (length(degree) BETWEEN 2 AND 80),
  level           text        NOT NULL CHECK (level IN ('ug','pg','other')),
  specialty       text        CHECK (specialty ~ '^[a-z][a-z0-9_]{1,39}$'),
  recorded_at     timestamptz NOT NULL DEFAULT now(),
  CHECK (specialty IS NULL OR level = 'pg')
);
CREATE INDEX qualification_professional_idx ON qualification (professional_id);

CREATE TABLE clinic (
  id                   uuid        PRIMARY KEY,
  name                 text        NOT NULL CHECK (length(name) BETWEEN 2 AND 120),
  address_text         text        NOT NULL CHECK (length(address_text) BETWEEN 5 AND 300),
  geo                  public.geography(Point, 4326) NOT NULL,
  geo_source           text        NOT NULL CHECK (geo_source IN ('geocoded','manual_pin')),
  geo_accuracy         text        NOT NULL DEFAULT '',
  time_zone            text        NOT NULL CHECK (length(time_zone) BETWEEN 1 AND 64),
  lead_professional_id uuid        NOT NULL REFERENCES professional (id),
  registration_doc_ref text        CHECK (registration_doc_ref ~ '^[A-Za-z0-9_./\-]{1,200}$'),
  hfr_id               text        CHECK (hfr_id ~ '^[A-Za-z0-9\-]{1,30}$'),
  created_by           uuid        NOT NULL,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX clinic_geo_idx ON clinic USING gist (geo);
CREATE INDEX clinic_lead_idx ON clinic (lead_professional_id);

CREATE TABLE practice (
  id              uuid        PRIMARY KEY,
  professional_id uuid        NOT NULL REFERENCES professional (id),
  clinic_id       uuid        NOT NULL REFERENCES clinic (id),
  modes           text[]      NOT NULL CHECK (cardinality(modes) BETWEEN 1 AND 3 AND modes <@ ARRAY['in_person','video','audio']),
  fee_amount      bigint      NOT NULL CHECK (fee_amount >= 0),
  currency        text        NOT NULL CHECK (length(currency) BETWEEN 1 AND 8),
  -- Bumped only when fee_amount changes. A booking copies (amount, currency, version) at
  -- hold time, so a later change never touches an existing booking.
  fee_version     int         NOT NULL DEFAULT 1,
  fee_updated_at  timestamptz NOT NULL DEFAULT now(),
  timings         text        NOT NULL DEFAULT '' CHECK (length(timings) <= 200),
  active          boolean     NOT NULL DEFAULT true,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (professional_id, clinic_id)
);
CREATE INDEX practice_clinic_idx ON practice (clinic_id) WHERE active;
CREATE INDEX practice_professional_idx ON practice (professional_id);

-- Read model fed by verification.status_changed (B5, K-CS-01). professional_ref is a
-- doctor's ID or a clinic's ID. A missing row means "not verified" (fail closed).
CREATE TABLE verification_status_view (
  professional_ref uuid        PRIMARY KEY,
  status           text        NOT NULL CHECK (status ~ '^[a-z_]{1,30}$'),
  updated_at       timestamptz NOT NULL
);

-- Our own locality and PIN-code table for the search fallback (adapter-interfaces §10:
-- no reverse geocoding; patient location never leaves us). Loaded from a dataset by an
-- operator task; the repository holds synthetic test data only.
CREATE TABLE locality (
  postal_code text PRIMARY KEY CHECK (postal_code ~ '^[A-Z0-9\-]{3,10}$'),
  centroid    public.geography(Point, 4326) NOT NULL
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
