-- Review F3 (P02): when the name, date of birth or sex last changed. A doctor's identity
-- confirmation made before this time no longer holds, so a prescription never prints details
-- other than those the doctor confirmed. Changes to other profile fields do not move it.
ALTER TABLE profile ADD COLUMN identity_changed_at timestamptz NOT NULL DEFAULT now();
