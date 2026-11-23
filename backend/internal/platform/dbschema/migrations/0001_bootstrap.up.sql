-- Bootstrap (S-090, B1/B2): one schema and one NOLOGIN group role per module.
-- Login roles (one per module) are created by infrastructure code with passwords from
-- the secrets manager and made members of these group roles. No password lives here.
CREATE EXTENSION IF NOT EXISTS postgis WITH SCHEMA public;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;

DO $$
DECLARE
  m text;
  r text;
BEGIN
  CREATE SCHEMA IF NOT EXISTS audit;
  FOREACH m IN ARRAY ARRAY['admin','audit','booking','clinical','consult','directory','identity',
                           'notifications','payments','platform','records','verification'] LOOP
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
    -- Every module role may reach the audit schema, but only to INSERT into
    -- audit.audit_outbox (granted in the audit module's migration). The single B2 exception.
    EXECUTE format('GRANT USAGE ON SCHEMA audit TO %I', r);
  END LOOP;

  -- Queue (River) role: used only by the event relay and workers (decision D-07).
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mugizh_queue') THEN
    CREATE ROLE mugizh_queue NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
  END IF;
  CREATE SCHEMA IF NOT EXISTS queue;
  REVOKE ALL ON SCHEMA queue FROM PUBLIC;
  GRANT USAGE ON SCHEMA queue TO mugizh_queue;
  ALTER DEFAULT PRIVILEGES IN SCHEMA queue GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO mugizh_queue;
  ALTER DEFAULT PRIVILEGES IN SCHEMA queue GRANT USAGE, SELECT ON SEQUENCES TO mugizh_queue;
  ALTER DEFAULT PRIVILEGES IN SCHEMA queue GRANT EXECUTE ON FUNCTIONS TO mugizh_queue;
END $$;

-- The migrations bookkeeping table is for the migration owner only.
REVOKE ALL ON platform.schema_migrations FROM PUBLIC;
