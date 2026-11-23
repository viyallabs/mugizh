ALTER TABLE sign_in_lock DROP CONSTRAINT sign_in_lock_pkey;
DELETE FROM sign_in_lock a USING sign_in_lock b WHERE a.phone_bidx = b.phone_bidx AND a.ip_key > b.ip_key;
ALTER TABLE sign_in_lock ADD PRIMARY KEY (phone_bidx);
ALTER TABLE sign_in_lock DROP COLUMN ip_key;
DROP INDEX IF EXISTS device_user_key_app;
CREATE UNIQUE INDEX device_user_key ON device (user_id, key_hash) WHERE status <> 'revoked';
