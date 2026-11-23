DROP INDEX IF EXISTS caregiver_redeem_attempt_ip_idx;
ALTER TABLE caregiver_redeem_attempt DROP COLUMN IF EXISTS ip_hash;
ALTER TABLE caregiver_link DROP COLUMN IF EXISTS label_ref, DROP COLUMN IF EXISTS label_sealed;
ALTER TABLE caregiver_invitation DROP COLUMN IF EXISTS label_sealed;
