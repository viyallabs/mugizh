-- Review H2: an approval claims the request ('approving') before the grant is created, so two
-- concurrent approvals cannot create two grants.
ALTER TABLE access_request DROP CONSTRAINT access_request_status_check;
ALTER TABLE access_request ADD CONSTRAINT access_request_status_check CHECK (status IN ('pending','approving','approved','rejected','expired'));
