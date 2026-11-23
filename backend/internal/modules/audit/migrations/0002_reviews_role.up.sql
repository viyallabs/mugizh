-- The reviews module's role may INSERT into the audit outbox, like every module role (see 0001).
-- Its role and schema are created by the platform bootstrap migration 0002, which runs first.
REVOKE ALL ON audit.audit_outbox FROM mugizh_mod_reviews;
GRANT INSERT ON audit.audit_outbox TO mugizh_mod_reviews;
