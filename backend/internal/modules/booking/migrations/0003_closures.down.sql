DROP TABLE IF EXISTS clinic_closure;
DROP INDEX IF EXISTS availability_branch_idx;
ALTER TABLE availability DROP COLUMN IF EXISTS branch_ref;
