-- CH-12 / TC-48 (E-FAM-04): a doctor who finds the patient is a minor stops the consult and
-- a support case opens, so staff run the CH-12 runbook (close the account, keep RET-02
-- records, tell the user). The case holds references only.
ALTER TABLE grievance DROP CONSTRAINT grievance_kind_check;
ALTER TABLE grievance ADD CONSTRAINT grievance_kind_check CHECK (kind IN ('complaint','content_takedown','order_takedown','intimate_image',
  'lawful_request','medical_record_copy','data_rights','child_account'));
