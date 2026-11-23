-- S-134 (money-flow T1, T5): the patient's payment document, issued in the clinic's name on its
-- behalf. Its type follows the clinic's tax registration status recorded at onboarding
-- (registered -> the type the tax configuration names for registered clinics, for example a
-- bill of supply with the registration ID; unregistered -> a plain receipt with no tax fields).
-- The profile is copied onto each order when it is created, so a later change applies only to
-- new bookings. Numbers run without gaps per clinic, document type and financial year.
-- [VALIDATE-CA] formats, numbering and the clinic's written authorisation (money-flow T5).
ALTER TABLE clinic_merchant_link
  ADD COLUMN doc_legal_name text NOT NULL DEFAULT '' CHECK (length(doc_legal_name) <= 120),
  ADD COLUMN doc_address    text NOT NULL DEFAULT '' CHECK (length(doc_address) <= 300),
  ADD COLUMN tax_status     text NOT NULL DEFAULT '' CHECK (tax_status IN ('','registered','unregistered')),
  ADD COLUMN tax_id         text NOT NULL DEFAULT '' CHECK (tax_id ~ '^[0-9A-Z]{0,20}$'),
  ADD CONSTRAINT clinic_tax_id_only_when_registered CHECK ((tax_status = 'registered') = (tax_id <> ''));

ALTER TABLE payment_order
  ADD COLUMN doc_type       text NOT NULL DEFAULT '' CHECK (doc_type ~ '^[a-z_]{0,30}$'),
  ADD COLUMN doc_legal_name text NOT NULL DEFAULT '' CHECK (length(doc_legal_name) <= 120),
  ADD COLUMN doc_address    text NOT NULL DEFAULT '' CHECK (length(doc_address) <= 300),
  ADD COLUMN doc_tax_id     text NOT NULL DEFAULT '' CHECK (doc_tax_id ~ '^[0-9A-Z]{0,20}$');

CREATE TABLE document_series (
  clinic_ref uuid    NOT NULL,
  doc_type   text    NOT NULL CHECK (doc_type ~ '^[a-z_]{1,30}$'),
  fy_label   text    NOT NULL CHECK (fy_label ~ '^[0-9]{4}-([0-9]{2}|CY)$'),
  next_no    integer NOT NULL DEFAULT 1 CHECK (next_no > 0),
  PRIMARY KEY (clinic_ref, doc_type, fy_label)
);

CREATE TABLE patient_document (
  doc_ref           uuid        PRIMARY KEY,
  order_ref         uuid        NOT NULL UNIQUE REFERENCES payment_order (order_ref),
  appointment_ref   uuid        NOT NULL,
  clinic_ref        uuid        NOT NULL,
  doc_type          text        NOT NULL CHECK (doc_type ~ '^[a-z_]{1,30}$'),
  number            text        NOT NULL CHECK (number ~ '^[0-9]{4}-([0-9]{2}|CY)/[0-9]{6,}$'),
  issued_at         timestamptz NOT NULL,
  issued_on         date        NOT NULL,
  clinic_legal_name text        NOT NULL CHECK (length(clinic_legal_name) BETWEEN 1 AND 120),
  clinic_address    text        NOT NULL CHECK (length(clinic_address) BETWEEN 1 AND 300),
  clinic_tax_id     text        NOT NULL DEFAULT '' CHECK (clinic_tax_id ~ '^[0-9A-Z]{0,20}$'),
  amount_minor      bigint      NOT NULL CHECK (amount_minor > 0),
  currency          text        NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  payment_ref       text        NOT NULL,
  UNIQUE (clinic_ref, doc_type, number)
);
CREATE INDEX patient_document_appointment_idx ON patient_document (appointment_ref);
-- Issued documents are financial records (RET-04): never changed or deleted.
REVOKE UPDATE, DELETE, TRUNCATE ON patient_document FROM mugizh_mod_payments;
REVOKE DELETE, TRUNCATE ON document_series FROM mugizh_mod_payments;

-- A capture for a clinic without a complete document profile opens a ticket (no guessed document).
ALTER TABLE ops_ticket DROP CONSTRAINT ops_ticket_kind_check;
ALTER TABLE ops_ticket ADD CONSTRAINT ops_ticket_kind_check CHECK (kind IN ('grievance_refund_shortfall','refund_failed','reconciliation_mismatch',
  'refund_policy_blocked','webhook_rejected','refund_exceeds_remaining','order_failed','document_profile_missing'));
