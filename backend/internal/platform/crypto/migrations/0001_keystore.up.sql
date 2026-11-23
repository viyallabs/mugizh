-- Key store (S-098; encryption-design §3-§4). Runs in its OWN database, not the Core
-- database, so wrapped data keys never appear in Core database backups. Key-store
-- backups are kept at most 35 days (RET-17) under a separate key.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mugizh_keystore') THEN
    CREATE ROLE mugizh_keystore NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
  END IF;
END $$;
REVOKE ALL ON SCHEMA keystore FROM PUBLIC;
GRANT USAGE ON SCHEMA keystore TO mugizh_keystore;

-- One row per (kind, owner): which key ID serves it and its current version.
CREATE TABLE key_ref (
  kind            text        NOT NULL CHECK (kind IN ('patient','care','service','verification','pilot')),
  owner_ref       text        NOT NULL CHECK (length(owner_ref) BETWEEN 1 AND 200),
  key_id          uuid        NOT NULL UNIQUE,
  current_version int         NOT NULL DEFAULT 1 CHECK (current_version > 0),
  status          text        NOT NULL DEFAULT 'active' CHECK (status IN ('active','destroyed')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  destroyed_at    timestamptz,
  PRIMARY KEY (kind, owner_ref)
);

-- Wrapped data keys (encrypted by the KMS root key). Deleted on destruction.
CREATE TABLE data_key (
  key_id      uuid        NOT NULL REFERENCES key_ref (key_id),
  version     int         NOT NULL CHECK (version > 0),
  wrapped     bytea       NOT NULL,
  root_key    text        NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (key_id, version)
);

-- Destruction log (tombstones). Never holds key material. Replicated to DR and replayed
-- after every restore before the service opens (encryption-design §4 step 4).
CREATE TABLE destruction_log (
  key_id       uuid        NOT NULL,
  version      int,                       -- NULL = every version of the key
  kind         text        NOT NULL,
  reason       text        NOT NULL CHECK (reason ~ '^[a-z][a-z0-9_.]*$'),
  destroyed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (key_id, destroyed_at)
);

GRANT SELECT, INSERT, UPDATE ON key_ref TO mugizh_keystore;
GRANT SELECT, INSERT, DELETE ON data_key TO mugizh_keystore;
-- Root-key rotation re-wraps data keys in place (encryption-design §6); nothing else changes.
GRANT UPDATE (wrapped, root_key) ON data_key TO mugizh_keystore;
GRANT SELECT, INSERT ON destruction_log TO mugizh_keystore;
REVOKE UPDATE, DELETE, TRUNCATE ON destruction_log FROM mugizh_keystore;
