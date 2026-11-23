ALTER TABLE grievance DROP CONSTRAINT grievance_kind_check;
ALTER TABLE grievance ADD CONSTRAINT grievance_kind_check CHECK (kind IN ('complaint','content_takedown','order_takedown','intimate_image',
  'lawful_request','medical_record_copy','data_rights'));
