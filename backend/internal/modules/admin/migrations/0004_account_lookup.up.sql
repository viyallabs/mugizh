-- S-172 support lookups: who looked up an account, for which ticket, and whether one was
-- found (evidence for the monthly access review and the per-staff limit, E-DP-16). The phone
-- number searched is never stored. Append-only.
CREATE TABLE account_lookup (
  id           uuid        PRIMARY KEY,
  staff_ref    uuid        NOT NULL,
  ticket_ref   text        NOT NULL CHECK (ticket_ref ~ '^[A-Za-z0-9_-]{1,40}$'),
  found        boolean     NOT NULL,
  looked_up_at timestamptz NOT NULL
);
CREATE INDEX account_lookup_staff_idx ON account_lookup (staff_ref, looked_up_at DESC);
REVOKE UPDATE, DELETE, TRUNCATE ON account_lookup FROM mugizh_mod_admin;
