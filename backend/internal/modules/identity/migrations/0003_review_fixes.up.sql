-- Security review fixes (2026-11-17).
-- H2: a device is bound per app; a pro-app device row can never satisfy a patient sign-in.
DROP INDEX IF EXISTS device_user_key;
CREATE UNIQUE INDEX device_user_key_app ON device (user_id, key_hash, app) WHERE status <> 'revoked';
-- M3: wrong-code lockouts apply to the (number, client network) pair, so a stranger cannot
-- lock the real owner out from elsewhere; per-number request limits still bound guessing.
ALTER TABLE sign_in_lock ADD COLUMN ip_key bytea NOT NULL DEFAULT '\x'::bytea;
ALTER TABLE sign_in_lock DROP CONSTRAINT sign_in_lock_pkey;
ALTER TABLE sign_in_lock ADD PRIMARY KEY (phone_bidx, ip_key);
