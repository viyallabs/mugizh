ALTER TABLE clinic DROP CONSTRAINT clinic_registration_doc_ref_check;
ALTER TABLE clinic ADD CONSTRAINT clinic_registration_doc_ref_check CHECK (registration_doc_ref ~ '^[A-Za-z0-9_./\-]{1,200}$');
ALTER TABLE professional DROP CONSTRAINT professional_photo_ref_check;
ALTER TABLE professional ADD CONSTRAINT professional_photo_ref_check CHECK (photo_ref ~ '^[A-Za-z0-9_./\-]{1,200}$');
DROP TABLE IF EXISTS doctor_invitation;
ALTER TABLE clinic DROP COLUMN lead_accepted;
ALTER TABLE practice DROP COLUMN doctor_accepted;
