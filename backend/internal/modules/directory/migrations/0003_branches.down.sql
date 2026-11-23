DROP INDEX IF EXISTS practice_branch_idx;
DROP INDEX IF EXISTS practice_professional_branch_uq;
ALTER TABLE practice DROP CONSTRAINT IF EXISTS practice_branch_fk;
ALTER TABLE practice DROP COLUMN IF EXISTS branch_id;
DROP TABLE IF EXISTS branch;
ALTER TABLE practice ADD CONSTRAINT practice_professional_id_clinic_id_key UNIQUE (professional_id, clinic_id);
