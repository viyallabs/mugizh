-- Reviews module (S-209, S-214): its own schema and NOLOGIN group role, with the same grants
-- as 0001_bootstrap for every module. Idempotent. The INSERT right on audit.audit_outbox is
-- granted by the audit module's migration 0002, which runs after this one.
DO $$
DECLARE
  m text := 'reviews';
  r text;
BEGIN
  r := 'mugizh_mod_' || m;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
    EXECUTE format('CREATE ROLE %I NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT', r);
  END IF;
  EXECUTE format('CREATE SCHEMA IF NOT EXISTS %I', m);
  EXECUTE format('REVOKE ALL ON SCHEMA %I FROM PUBLIC', m);
  EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', m, r);
  EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %I GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %I', m, r);
  EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %I GRANT USAGE, SELECT ON SEQUENCES TO %I', m, r);
  EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %I GRANT EXECUTE ON FUNCTIONS TO %I', m, r);
  EXECUTE format('GRANT USAGE ON SCHEMA audit TO %I', r);
END $$;
