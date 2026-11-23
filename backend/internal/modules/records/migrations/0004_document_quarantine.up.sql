-- Hidden documents, hard-deleted by the purge job. AAD-05 (TM-43): a document reported to show a prohibited identifier
-- (an Aadhaar number in the India pack) is quarantined at once. RET-03: the patient deletes their own upload. Data
-- inventory 4.8: the instant, the reason and the role of whoever hid it; never the number.
ALTER TABLE document ADD COLUMN hidden_at timestamptz;
ALTER TABLE document ADD COLUMN hidden_reason text CHECK (hidden_reason IN ('prohibited_identifier','patient_deleted'));
ALTER TABLE document ADD COLUMN hidden_by_role text CHECK (hidden_by_role IN ('patient','doctor'));
ALTER TABLE document ADD CONSTRAINT document_hidden_complete CHECK ((hidden_at IS NULL) = (hidden_reason IS NULL) AND (hidden_at IS NULL) = (hidden_by_role IS NULL));
CREATE INDEX document_hidden_idx ON document (hidden_at) WHERE hidden_at IS NOT NULL;
