-- Revocation journal (backup-and-restore.md section 2 item 4, K-DP-02). A consent withdrawal, a grant
-- revocation and a caregiver link revocation are also written to write-once storage, outside this database,
-- and replayed (removals only) before the service opens after a restore. This table is the outbox for that
-- write: a row is inserted in the same transaction as the stop action and marked journaled once the entry is
-- in write-once storage, so the worker can catch up after an object-store outage.
-- References, codes and times only: no contact details, no health data.
CREATE TABLE revocation_outbox (
  id            uuid        PRIMARY KEY,
  kind          text        NOT NULL CHECK (kind IN ('consent','grant','link')),
  subject_ref   uuid        NOT NULL,
  purpose_id    text        CHECK (purpose_id ~ '^[PD]-[A-Z]+$'),
  grant_id      uuid,
  link_id       uuid,
  caregiver_ref uuid,
  at            timestamptz NOT NULL,
  journaled_at  timestamptz,
  CHECK ((kind = 'consent') = (purpose_id IS NOT NULL)),
  CHECK ((kind = 'grant') = (grant_id IS NOT NULL)),
  CHECK ((kind = 'link') = (link_id IS NOT NULL AND caregiver_ref IS NOT NULL))
);
CREATE INDEX revocation_outbox_due_idx ON revocation_outbox (at) WHERE journaled_at IS NULL;
