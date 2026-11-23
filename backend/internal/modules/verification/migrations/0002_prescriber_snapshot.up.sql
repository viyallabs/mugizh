-- The registration details a reviewer approved, copied when the final "verified" decision
-- lands. Prescriptions print these (prescription format: name, qualification, registration
-- number and council), never a re-verification draft that no reviewer has seen yet.
-- Same data class and retention as the case columns they copy (data inventory section 4.4).
ALTER TABLE verification_case
  ADD COLUMN verified_registered_name     text   NOT NULL DEFAULT '' CHECK (length(verified_registered_name) <= 120),
  ADD COLUMN verified_council_code        text   NOT NULL DEFAULT '' CHECK (length(verified_council_code) <= 60),
  ADD COLUMN verified_registration_number text   NOT NULL DEFAULT '' CHECK (length(verified_registration_number) <= 40),
  ADD COLUMN verified_qualifications      text[] NOT NULL DEFAULT '{}';
