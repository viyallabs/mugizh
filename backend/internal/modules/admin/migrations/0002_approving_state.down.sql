UPDATE access_request SET status='pending' WHERE status='approving';
ALTER TABLE access_request DROP CONSTRAINT access_request_status_check;
ALTER TABLE access_request ADD CONSTRAINT access_request_status_check CHECK (status IN ('pending','approved','rejected','expired'));
