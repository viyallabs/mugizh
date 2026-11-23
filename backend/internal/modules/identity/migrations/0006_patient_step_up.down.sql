ALTER TABLE otp_challenge DROP CONSTRAINT IF EXISTS otp_challenge_step_up_session;
ALTER TABLE otp_challenge DROP COLUMN IF EXISTS session_id;
