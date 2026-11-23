ALTER TABLE ops_ticket DROP CONSTRAINT ops_ticket_kind_check;
ALTER TABLE ops_ticket ADD CONSTRAINT ops_ticket_kind_check CHECK (kind IN ('grievance_refund_shortfall','refund_failed','reconciliation_mismatch',
  'refund_policy_blocked','webhook_rejected','refund_exceeds_remaining','order_failed'));
DROP TABLE patient_document;
DROP TABLE document_series;
ALTER TABLE payment_order DROP COLUMN doc_type, DROP COLUMN doc_legal_name, DROP COLUMN doc_address, DROP COLUMN doc_tax_id;
ALTER TABLE clinic_merchant_link DROP CONSTRAINT clinic_tax_id_only_when_registered,
  DROP COLUMN doc_legal_name, DROP COLUMN doc_address, DROP COLUMN tax_status, DROP COLUMN tax_id;
