-- Push token per device (data inventory §4.1 "Device ID, push token, app version").
-- Encrypted with the person's key; replaced on change; deleted with the device.
ALTER TABLE device ADD COLUMN push_token_ct bytea;
ALTER TABLE device ADD COLUMN push_token_updated_at timestamptz;
