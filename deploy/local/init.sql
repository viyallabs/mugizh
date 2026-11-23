-- Separate database for the key store (encryption-design §4): wrapped keys never sit in
-- the Core database or its backups.
CREATE DATABASE mugizh_keystore;
