-- payments module (S-124, S-125, S-132, S-133, S-146, S-163; K-PAY-01..05).
-- References, amounts in minor units and statuses only. NO card, UPI or bank data
-- exists in this schema (K-PAY-01). Financial records: kept RET-04, never deleted.

-- Per-clinic merchant link (mode B). Holds only CREDENTIAL REFERENCES: the secrets live
-- in the secrets manager (adapter-interfaces A8, K-SEC-05).
CREATE TABLE clinic_merchant_link (
  link_ref           uuid        PRIMARY KEY,
  clinic_ref         uuid        NOT NULL UNIQUE,
  aggregator         text        NOT NULL CHECK (aggregator ~ '^[a-z0-9_]{1,40}$'),
  -- 'split' is v1.2 and is refused (S-163): it is not even an allowed value.
  settlement_mode    text        NOT NULL CHECK (settlement_mode IN ('clinic_collected','pay_at_clinic')),
  credential_ref     text        NOT NULL DEFAULT '' CHECK (credential_ref = '' OR credential_ref ~ '^secretref:[A-Za-z0-9_./\-]{1,200}$'),
  webhook_secret_ref text        NOT NULL DEFAULT '' CHECK (webhook_secret_ref = '' OR webhook_secret_ref ~ '^secretref:[A-Za-z0-9_./\-]{1,200}$'),
  status             text        NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended')),
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  CHECK (settlement_mode = 'pay_at_clinic' OR (credential_ref <> '' AND webhook_secret_ref <> ''))
);

-- Durable operation record for orders (API standards section 6): unique operation key and
-- our merchant reference, independent of the 24-hour HTTP replay cache.
CREATE TABLE payment_order (
  order_ref         uuid        PRIMARY KEY,
  operation_key     text        NOT NULL UNIQUE,
  merchant_ref      text        NOT NULL UNIQUE,
  appointment_ref   uuid        NOT NULL,
  clinic_ref        uuid        NOT NULL,
  link_ref          uuid        NOT NULL REFERENCES clinic_merchant_link (link_ref),
  amount_minor      bigint      NOT NULL CHECK (amount_minor > 0),
  currency          text        NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  -- requested -> processing (vendor call in flight) -> created | unknown | failed;
  -- created -> captured | expired.
  status            text        NOT NULL CHECK (status IN ('requested','processing','unknown','created','captured','expired','failed')),
  appointment_state text        NOT NULL DEFAULT 'open' CHECK (appointment_state IN ('open','cancelled','expired','no_show')),
  provider_order_id text        UNIQUE,
  checkout_url      text        NOT NULL DEFAULT '',
  payment_id        text,
  captured_at       timestamptz,
  hold_expires_at   timestamptz NOT NULL,
  attempts          int         NOT NULL DEFAULT 0,
  last_call_at      timestamptz,
  next_attempt_at   timestamptz NOT NULL,
  failure_code      text        NOT NULL DEFAULT '',
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  CHECK (operation_key = 'order:' || appointment_ref::text),
  CHECK (merchant_ref = order_ref::text)
);
CREATE INDEX payment_order_clinic_idx ON payment_order (clinic_ref, order_ref DESC);
CREATE INDEX payment_order_pending_idx ON payment_order (next_attempt_at) WHERE status IN ('requested','processing','unknown','created');

CREATE TABLE payment_attempt (
  order_ref    uuid        NOT NULL REFERENCES payment_order (order_ref),
  payment_id   text        NOT NULL CHECK (payment_id ~ '^[A-Za-z0-9_]{1,60}$'),
  status       text        NOT NULL CHECK (status IN ('captured','failed')),
  amount_minor bigint      NOT NULL CHECK (amount_minor > 0),
  currency     text        NOT NULL,
  event_at     timestamptz NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (order_ref, payment_id)
);
CREATE INDEX payment_attempt_captured_idx ON payment_attempt (event_at) WHERE status = 'captured';

-- Raw verified webhook events, de-duplicated by the vendor's event ID (A7, TC-30).
-- Only events whose signature verified are stored.
CREATE TABLE webhook_event (
  id                  uuid        PRIMARY KEY,
  link_ref            uuid        NOT NULL,
  vendor_event_id     text        NOT NULL CHECK (length(vendor_event_id) BETWEEN 1 AND 100),
  event_type          text        NOT NULL CHECK (length(event_type) <= 60),
  merchant_order_ref  text        NOT NULL DEFAULT '',
  provider_order_id   text        NOT NULL DEFAULT '',
  payment_id          text        NOT NULL DEFAULT '',
  merchant_refund_ref text        NOT NULL DEFAULT '',
  provider_refund_id  text        NOT NULL DEFAULT '',
  amount_minor        bigint      NOT NULL,
  currency            text        NOT NULL,
  occurred_at         timestamptz NOT NULL,
  raw_body            bytea       NOT NULL,
  status              text        NOT NULL DEFAULT 'received' CHECK (status IN ('received','processed','rejected')),
  reject_reason       text        NOT NULL DEFAULT '',
  received_at         timestamptz NOT NULL DEFAULT now(),
  processed_at        timestamptz,
  UNIQUE (link_ref, vendor_event_id)
);
CREATE INDEX webhook_event_pending_idx ON webhook_event (received_at, id) WHERE status = 'received';

-- Durable operation record for refunds: one refund per booking and rule, whatever key the
-- client sent (TM-12).
CREATE TABLE refund (
  refund_ref         uuid        PRIMARY KEY,
  operation_key      text        NOT NULL UNIQUE CHECK (operation_key LIKE 'refund:%'),
  merchant_ref       text        NOT NULL UNIQUE,
  order_ref          uuid        NOT NULL REFERENCES payment_order (order_ref),
  appointment_ref    uuid        NOT NULL,
  clinic_ref         uuid        NOT NULL,
  link_ref           uuid        NOT NULL,
  payment_id         text        NOT NULL,
  rule_id            text        NOT NULL CHECK (rule_id ~ '^RF[0-9]{2}$'),
  policy_version     text        NOT NULL CHECK (policy_version ~ '^[A-Za-z0-9.\-]{1,40}$'),
  amount_minor       bigint      NOT NULL CHECK (amount_minor > 0),
  currency           text        NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  -- requested -> processing -> unknown -> succeeded | failed -> escalated
  status             text        NOT NULL CHECK (status IN ('requested','processing','unknown','succeeded','failed','escalated')),
  provider_refund_id text        UNIQUE,
  failure_code       text        NOT NULL DEFAULT '',
  attempts           int         NOT NULL DEFAULT 0,
  last_call_at       timestamptz,
  next_attempt_at    timestamptz NOT NULL,
  shortfall_since    timestamptz,
  escalated_at       timestamptz,
  succeeded_at       timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  CHECK (merchant_ref = refund_ref::text)
);
CREATE INDEX refund_appointment_idx ON refund (appointment_ref);
CREATE INDEX refund_clinic_idx ON refund (clinic_ref, refund_ref DESC);
CREATE INDEX refund_pending_idx ON refund (next_attempt_at) WHERE status IN ('requested','processing','unknown','escalated');

-- Ledger (K-PAY-04, S-132): append-only, double-entry. Signed integer minor units:
-- debits positive, credits negative; every transaction sums to zero per currency.
-- Mode B money is not ours, so every entry is a MEMO entry.
CREATE TABLE ledger_txn (
  txn_id      uuid        PRIMARY KEY,
  txn_key     text        NOT NULL UNIQUE,
  kind        text        NOT NULL CHECK (kind IN ('payment_captured','refund_succeeded')),
  order_ref   uuid        NOT NULL,
  refund_ref  uuid,
  occurred_at timestamptz NOT NULL
);
CREATE TABLE ledger_entry (
  entry_id     uuid        PRIMARY KEY,
  txn_id       uuid        NOT NULL REFERENCES ledger_txn (txn_id),
  account      text        NOT NULL CHECK (account ~ '^[a-z_]+:[a-z_]+:[0-9a-f\-]{36}$'),
  amount_minor bigint      NOT NULL CHECK (amount_minor <> 0),
  currency     text        NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  memo         boolean     NOT NULL DEFAULT true,
  occurred_at  timestamptz NOT NULL
);
CREATE INDEX ledger_entry_txn_idx ON ledger_entry (txn_id);
CREATE INDEX ledger_entry_account_idx ON ledger_entry (account, currency);

CREATE FUNCTION ledger_assert_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  n   int;
  bad int;
BEGIN
  SELECT count(*) INTO n FROM payments.ledger_entry WHERE txn_id = NEW.txn_id;
  SELECT count(*) INTO bad FROM (
    SELECT currency FROM payments.ledger_entry WHERE txn_id = NEW.txn_id GROUP BY currency HAVING sum(amount_minor) <> 0
  ) s;
  IF n < 2 OR bad > 0 THEN
    RAISE EXCEPTION 'ledger transaction % is not balanced', NEW.txn_id USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER ledger_entry_balanced AFTER INSERT ON ledger_entry
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION ledger_assert_balanced();
CREATE CONSTRAINT TRIGGER ledger_txn_balanced AFTER INSERT ON ledger_txn
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION ledger_assert_balanced();

-- Defence in depth: even a role with UPDATE or DELETE rights cannot change the ledger.
CREATE FUNCTION ledger_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'the ledger is append-only' USING ERRCODE = '42501';
END $$;
CREATE TRIGGER ledger_entry_append_only BEFORE UPDATE OR DELETE ON ledger_entry FOR EACH ROW EXECUTE FUNCTION ledger_append_only();
CREATE TRIGGER ledger_txn_append_only BEFORE UPDATE OR DELETE ON ledger_txn FOR EACH ROW EXECUTE FUNCTION ledger_append_only();
CREATE TRIGGER ledger_entry_no_truncate BEFORE TRUNCATE ON ledger_entry FOR EACH STATEMENT EXECUTE FUNCTION ledger_append_only();
CREATE TRIGGER ledger_txn_no_truncate BEFORE TRUNCATE ON ledger_txn FOR EACH STATEMENT EXECUTE FUNCTION ledger_append_only();
REVOKE UPDATE, DELETE, TRUNCATE ON ledger_entry FROM mugizh_mod_payments;
REVOKE UPDATE, DELETE, TRUNCATE ON ledger_txn FROM mugizh_mod_payments;

-- Balances are derived, never stored or edited.
CREATE VIEW ledger_balance AS
  SELECT account, currency, sum(amount_minor)::bigint AS balance_minor FROM ledger_entry GROUP BY account, currency;

-- Tickets (grievance, reconciliation, blocked policy rules) and alerts. A ticket key makes
-- every ticket once-only, so retries and re-runs never duplicate them.
CREATE TABLE ops_ticket (
  ticket_ref  uuid        PRIMARY KEY,
  ticket_key  text        NOT NULL UNIQUE,
  kind        text        NOT NULL CHECK (kind IN ('grievance_refund_shortfall','refund_failed','reconciliation_mismatch',
                                                  'refund_policy_blocked','webhook_rejected','refund_exceeds_remaining','order_failed')),
  clinic_ref  uuid,
  subject_ref text        NOT NULL DEFAULT '' CHECK (subject_ref ~ '^[A-Za-z0-9_:.\-]{0,160}$'),
  status      text        NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved')),
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE payment_alert (
  alert_id    uuid        PRIMARY KEY,
  code        text        NOT NULL CHECK (code ~ '^[a-z][a-z0-9_.]{0,60}$'),
  clinic_ref  uuid,
  subject_ref text        NOT NULL DEFAULT '' CHECK (subject_ref ~ '^[A-Za-z0-9_:.\-]{0,160}$'),
  created_at  timestamptz NOT NULL DEFAULT now()
);

-- Daily reconciliation per clinic account (S-146, C17). One row per link and UTC day.
CREATE TABLE reconciliation_run (
  link_ref        uuid        NOT NULL,
  run_date        date        NOT NULL,
  line_count      int         NOT NULL,
  mismatch_count  int         NOT NULL,
  ran_at          timestamptz NOT NULL,
  PRIMARY KEY (link_ref, run_date)
);

-- Events (module-boundaries section 4): outbox of this module and processed-event guard.
CREATE TABLE event_outbox (
  event_id    uuid        PRIMARY KEY,
  type        text        NOT NULL,
  version     int         NOT NULL DEFAULT 1,
  occurred_at timestamptz NOT NULL,
  subject_ref text        NOT NULL DEFAULT '',
  payload     jsonb       NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX event_outbox_occurred_idx ON event_outbox (occurred_at, event_id);
CREATE TABLE processed_event (
  handler      text        NOT NULL,
  event_id     uuid        NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (handler, event_id)
);
