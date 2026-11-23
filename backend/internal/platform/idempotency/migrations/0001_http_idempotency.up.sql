-- HTTP replay cache (API standards §6, decision D-10). 24 hours, scoped per user.
-- Stored responses are encrypted with a service key; money is protected separately by
-- durable operation records in the payments module.
CREATE TABLE http_idempotency (
  scope        text        NOT NULL CHECK (length(scope) BETWEEN 1 AND 120),
  key          uuid        NOT NULL,
  request_hash bytea       NOT NULL CHECK (length(request_hash) = 32),
  state        text        NOT NULL CHECK (state IN ('in_progress','done')),
  status       int,
  content_type text,
  body_ct      bytea,
  created_at   timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL,
  PRIMARY KEY (scope, key)
);
CREATE INDEX http_idempotency_expiry_idx ON http_idempotency (expires_at);
