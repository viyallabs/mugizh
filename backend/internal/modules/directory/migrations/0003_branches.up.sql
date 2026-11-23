-- S-130 / E-GEO-02: a clinic (the verified legal entity) can have several branch locations,
-- each with its own address, geolocation and IANA time zone. A practice belongs to a branch.
-- Branch addresses are business addresses (class PUB), not personal data.
--
-- Every existing clinic gets a "main" branch whose id equals the clinic id. Other modules
-- rely on that convention to give old rows a branch without reading this schema.
CREATE TABLE branch (
  id           uuid        PRIMARY KEY,
  clinic_id    uuid        NOT NULL REFERENCES clinic (id),
  name         text        NOT NULL CHECK (length(name) BETWEEN 2 AND 120),
  address_text text        NOT NULL CHECK (length(address_text) BETWEEN 5 AND 300),
  geo          public.geography(Point, 4326) NOT NULL,
  geo_source   text        NOT NULL CHECK (geo_source IN ('geocoded','manual_pin')),
  geo_accuracy text        NOT NULL DEFAULT '',
  time_zone    text        NOT NULL CHECK (length(time_zone) BETWEEN 1 AND 64),
  is_main      boolean     NOT NULL DEFAULT false,
  active       boolean     NOT NULL DEFAULT true,
  created_by   uuid        NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (id, clinic_id)
);
CREATE INDEX branch_geo_idx ON branch USING gist (geo);
CREATE INDEX branch_clinic_idx ON branch (clinic_id);
CREATE UNIQUE INDEX branch_main_once ON branch (clinic_id) WHERE is_main;
CREATE UNIQUE INDEX branch_name_once ON branch (clinic_id, lower(name));
REVOKE DELETE, TRUNCATE ON branch FROM mugizh_mod_directory;

INSERT INTO branch (id, clinic_id, name, address_text, geo, geo_source, geo_accuracy, time_zone, is_main, active, created_by, created_at, updated_at)
SELECT id, id, left(name, 100) || ' (main)', address_text, geo, geo_source, geo_accuracy, time_zone, true, true, created_by, created_at, updated_at FROM clinic;

ALTER TABLE practice ADD COLUMN branch_id uuid;
UPDATE practice SET branch_id = clinic_id;
ALTER TABLE practice ALTER COLUMN branch_id SET NOT NULL;
ALTER TABLE practice ADD CONSTRAINT practice_branch_fk FOREIGN KEY (branch_id, clinic_id) REFERENCES branch (id, clinic_id);

-- One doctor can practise at several clinics and at several branches; once per branch.
ALTER TABLE practice DROP CONSTRAINT practice_professional_id_clinic_id_key;
CREATE UNIQUE INDEX practice_professional_branch_uq ON practice (professional_id, branch_id);
CREATE INDEX practice_branch_idx ON practice (branch_id) WHERE active;
