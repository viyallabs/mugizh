-- S-130 (branches) and S-145 / E-BK-06 (the clinic closes for a day).

-- A practice belongs to a branch of its clinic (directory convention: a clinic's main branch
-- has the clinic's own ID, so rows that exist before branches get that value). The copy is
-- refreshed from the directory on practice.updated, like the zone.
ALTER TABLE availability ADD COLUMN branch_ref uuid;
UPDATE availability SET branch_ref = clinic_ref;
ALTER TABLE availability ALTER COLUMN branch_ref SET NOT NULL;
CREATE INDEX availability_branch_idx ON availability (clinic_ref, branch_ref);

-- A closure: a clinic (or one branch, branch_ref set) is closed on one local date. The date is
-- read in each practice's own zone (K-ARC-04), so a clinic with branches in two zones closes
-- each branch's local day. The record is the unit of progress: cancelling or flagging is done
-- appointment by appointment, each in its own transaction, and a closure in state
-- 'processing' can be run again until it is 'done' (crash safe, idempotent).
CREATE TABLE clinic_closure (
  id              uuid        PRIMARY KEY,
  clinic_ref      uuid        NOT NULL,
  branch_ref      uuid,
  closure_date    date        NOT NULL,
  mode            text        NOT NULL CHECK (mode IN ('cancel','reschedule')),
  state           text        NOT NULL DEFAULT 'processing' CHECK (state IN ('processing','done')),
  created_by      uuid        NOT NULL,
  created_at      timestamptz NOT NULL,
  completed_at    timestamptz,
  cancelled_count int         NOT NULL DEFAULT 0,
  flagged_count   int         NOT NULL DEFAULT 0,
  CHECK ((state = 'done') = (completed_at IS NOT NULL))
);
-- The same closure twice is one closure.
CREATE UNIQUE INDEX clinic_closure_once ON clinic_closure (clinic_ref, COALESCE(branch_ref, '00000000-0000-0000-0000-000000000000'::uuid), closure_date);
CREATE INDEX clinic_closure_open_idx ON clinic_closure (created_at) WHERE state = 'processing';
CREATE INDEX clinic_closure_clinic_idx ON clinic_closure (clinic_ref, closure_date DESC);
REVOKE DELETE, TRUNCATE ON clinic_closure FROM mugizh_mod_booking;
