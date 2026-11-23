-- Patient step-up by SMS code (S-178, TM-32, TM-33; data-rights-flows.md). A step-up code
-- is bound to the session that asked for it, so it cannot be used from another session.
ALTER TABLE otp_challenge ADD COLUMN session_id uuid;
ALTER TABLE otp_challenge ADD CONSTRAINT otp_challenge_step_up_session
  CHECK (purpose <> 'step_up' OR session_id IS NOT NULL);
