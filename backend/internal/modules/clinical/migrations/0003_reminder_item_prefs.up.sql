-- S-155: the patient's reminder switch for one prescription item. It survives amendments (a
-- new signed version schedules only the items that are on). generation makes a re-enabled
-- item's scheduler activities new ones (their external refs and idempotency keys change).
CREATE TABLE reminder_item_pref (
  prescription_id uuid        NOT NULL REFERENCES prescription (id),
  item_ref        text        NOT NULL CHECK (item_ref ~ '^[A-Za-z0-9_-]{1,40}$'),
  enabled         boolean     NOT NULL,
  generation      integer     NOT NULL DEFAULT 0 CHECK (generation >= 0),
  updated_at      timestamptz NOT NULL,
  PRIMARY KEY (prescription_id, item_ref)
);
REVOKE DELETE, TRUNCATE ON reminder_item_pref FROM mugizh_mod_clinical;
