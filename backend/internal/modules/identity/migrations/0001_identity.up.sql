-- identity module (S-104, S-105, S-106, S-115; data inventory §4.1).
-- Phone numbers are field-encrypted with the person's key plus a blind index (K-DP-03).
-- OTPs and tokens are stored only as keyed or plain SHA-256 hashes (PER-K).
-- No Aadhaar field anywhere (K-DP-05).

CREATE TABLE user_account (
  id                uuid        PRIMARY KEY,
  status            text        NOT NULL DEFAULT 'active' CHECK (status IN ('active','closed')),
  phone_ct          bytea       NOT NULL,
  phone_bidx        bytea       NOT NULL CHECK (length(phone_bidx) = 32),
  preferred_lang    text        NOT NULL DEFAULT '' CHECK (length(preferred_lang) <= 20),
  created_at        timestamptz NOT NULL DEFAULT now(),
  last_seen_at      timestamptz NOT NULL DEFAULT now(),
  closed_at         timestamptz
);
-- One active account per phone. Closed accounts keep their row (restricted, RET-18).
CREATE UNIQUE INDEX user_account_phone_active ON user_account (phone_bidx) WHERE status = 'active';

CREATE TABLE role_assignment (
  id          uuid        PRIMARY KEY,
  user_id     uuid        NOT NULL REFERENCES user_account (id),
  role        text        NOT NULL CHECK (role IN ('patient','doctor','clinic_admin','front_desk','nurse','reviewer','support','grievance_officer','security_owner','ops')),
  clinic_ref  uuid,
  granted_by  uuid,
  granted_at  timestamptz NOT NULL DEFAULT now(),
  revoked_at  timestamptz
);
CREATE UNIQUE INDEX role_assignment_active ON role_assignment (user_id, role, coalesce(clinic_ref, '00000000-0000-0000-0000-000000000000'::uuid)) WHERE revoked_at IS NULL;

-- Device binding (K-SEC-03): the device holds the private key; we keep the public key.
CREATE TABLE device (
  id           uuid        PRIMARY KEY,
  user_id      uuid        NOT NULL REFERENCES user_account (id),
  public_key   bytea       NOT NULL CHECK (length(public_key) BETWEEN 32 AND 200),
  key_hash     bytea       NOT NULL CHECK (length(key_hash) = 32),
  platform     text        NOT NULL CHECK (platform IN ('android','ios','web')),
  app          text        NOT NULL CHECK (app IN ('patient','pro')),
  status       text        NOT NULL CHECK (status IN ('pending_reverification','active','revoked')),
  bound_at     timestamptz NOT NULL DEFAULT now(),
  approved_at  timestamptz,
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  revoked_at   timestamptz
);
CREATE UNIQUE INDEX device_user_key ON device (user_id, key_hash) WHERE status <> 'revoked';

CREATE TABLE session (
  id                  uuid        PRIMARY KEY,
  user_id             uuid        NOT NULL REFERENCES user_account (id),
  device_id           uuid        NOT NULL REFERENCES device (id),
  app                 text        NOT NULL CHECK (app IN ('patient','pro')),
  access_hash         bytea       NOT NULL UNIQUE CHECK (length(access_hash) = 32),
  access_expires_at   timestamptz NOT NULL,
  refresh_hash        bytea       NOT NULL UNIQUE CHECK (length(refresh_hash) = 32),
  prev_refresh_hash   bytea,
  refresh_expires_at  timestamptz NOT NULL,
  last_proof_ts       bigint      NOT NULL DEFAULT 0,
  absolute_expires_at timestamptz NOT NULL,
  mfa_at              timestamptz,
  step_up_at          timestamptz,
  restricted          boolean     NOT NULL DEFAULT false,
  mfa_failures        int         NOT NULL DEFAULT 0,
  created_at          timestamptz NOT NULL DEFAULT now(),
  last_used_at        timestamptz NOT NULL DEFAULT now(),
  revoked_at          timestamptz,
  revoke_reason       text
);
CREATE INDEX session_user_idx ON session (user_id) WHERE revoked_at IS NULL;
CREATE INDEX session_device_idx ON session (device_id) WHERE revoked_at IS NULL;

CREATE TABLE otp_challenge (
  id           uuid        PRIMARY KEY,
  phone_bidx   bytea       NOT NULL,
  purpose      text        NOT NULL CHECK (purpose IN ('sign_in','step_up')),
  app          text        NOT NULL CHECK (app IN ('patient','pro')),
  code_hash    bytea       NOT NULL CHECK (length(code_hash) = 32),
  attempts     int         NOT NULL DEFAULT 0,
  expires_at   timestamptz NOT NULL,
  consumed_at  timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX otp_challenge_expiry_idx ON otp_challenge (expires_at);

-- Fixed-window counters for OTP limits per phone, per IP and globally (TM-02).
CREATE TABLE otp_counter (
  scope        text        NOT NULL CHECK (scope IN ('phone_10m','phone_day','ip_10m','ip_day','global_day')),
  key          bytea       NOT NULL,
  window_start timestamptz NOT NULL,
  count        int         NOT NULL,
  PRIMARY KEY (scope, key, window_start)
);

-- Lock after repeated wrong codes, with doubling backoff (TM-01, E-SEC-01).
CREATE TABLE sign_in_lock (
  phone_bidx   bytea       PRIMARY KEY,
  lockouts     int         NOT NULL DEFAULT 0,
  locked_until timestamptz NOT NULL
);

-- TOTP factor for professionals (K-SEC-03). The secret is field-encrypted.
CREATE TABLE mfa_factor (
  id            uuid        PRIMARY KEY,
  user_id       uuid        NOT NULL REFERENCES user_account (id),
  type          text        NOT NULL CHECK (type IN ('totp')),
  secret_ct     bytea       NOT NULL,
  confirmed_at  timestamptz,
  last_step     bigint      NOT NULL DEFAULT 0,
  created_at    timestamptz NOT NULL DEFAULT now(),
  disabled_at   timestamptz
);
CREATE UNIQUE INDEX mfa_factor_one_active ON mfa_factor (user_id, type) WHERE disabled_at IS NULL AND confirmed_at IS NOT NULL;

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
