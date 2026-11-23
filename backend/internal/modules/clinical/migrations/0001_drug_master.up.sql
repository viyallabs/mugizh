-- clinical module: drug master and drug classification versions (S-126, S-138).
-- Internal data (INT): rule and drug tables, no patient data (E-DP-19). Every version is
-- immutable: the module role can only INSERT and SELECT.

CREATE TABLE drug_classification_version (
  id              uuid        PRIMARY KEY,
  version         text        NOT NULL UNIQUE CHECK (version ~ '^[A-Za-z0-9][A-Za-z0-9._\-]{0,39}$'),
  status          text        NOT NULL CHECK (status IN ('draft','doctor_reviewed','legal_reviewed','approved')),
  checksum        text        NOT NULL CHECK (checksum ~ '^[0-9a-f]{64}$'),
  ruleset_version text        NOT NULL,
  entry_count     int         NOT NULL CHECK (entry_count >= 0),
  imported_by     text        NOT NULL CHECK (imported_by ~ '^[A-Za-z0-9_:\-]{1,80}$'),
  imported_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE drug_master_entry (
  classification_id   uuid    NOT NULL REFERENCES drug_classification_version (id),
  drug_ref            text    NOT NULL CHECK (drug_ref ~ '^[a-z0-9][a-z0-9_\-]{1,79}$'),
  generic_name        text    NOT NULL CHECK (length(generic_name) BETWEEN 1 AND 100),
  brand_names         text[]  NOT NULL DEFAULT '{}',
  strength            text    NOT NULL,
  form                text    NOT NULL,
  route               text    NOT NULL,
  in_prohibited       boolean NOT NULL DEFAULT false,
  in_list_o           boolean NOT NULL DEFAULT false,
  in_list_a           boolean NOT NULL DEFAULT false,
  in_list_b           boolean NOT NULL DEFAULT false,
  amendment_exception boolean NOT NULL DEFAULT false,
  condition_tags      text[]  NOT NULL DEFAULT '{}',
  source_ref          text    NOT NULL CHECK (length(source_ref) BETWEEN 1 AND 300),
  PRIMARY KEY (classification_id, drug_ref),
  -- A drug is never both prohibited and permitted, unless an amendment exception names it.
  CONSTRAINT drug_not_prohibited_and_permitted
    CHECK (NOT (in_prohibited AND (in_list_o OR in_list_a OR in_list_b) AND NOT amendment_exception)),
  CONSTRAINT list_b_needs_condition_tag CHECK (NOT in_list_b OR cardinality(condition_tags) > 0)
);

-- Lower-case generic and brand names for prefix search.
CREATE TABLE drug_search_key (
  classification_id uuid NOT NULL,
  search_key        text NOT NULL,
  drug_ref          text NOT NULL,
  PRIMARY KEY (classification_id, search_key, drug_ref),
  FOREIGN KEY (classification_id, drug_ref) REFERENCES drug_master_entry (classification_id, drug_ref)
);
CREATE INDEX drug_search_key_prefix_idx ON drug_search_key (classification_id, search_key text_pattern_ops);

REVOKE UPDATE, DELETE, TRUNCATE ON drug_classification_version FROM mugizh_mod_clinical;
REVOKE UPDATE, DELETE, TRUNCATE ON drug_master_entry FROM mugizh_mod_clinical;
REVOKE UPDATE, DELETE, TRUNCATE ON drug_search_key FROM mugizh_mod_clinical;

-- Events (prescription.signed and prescription.amended come with the prescription stories).
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
