-- Review open item "payments tables deletable": every payments record is a financial
-- record kept under RET-04 (data inventory section 4.5), and erasure restricts them rather
-- than deleting (D-32). Nothing in the module deletes rows, so the module role loses
-- DELETE and TRUNCATE on every table except the event outbox, which the relay drains.
REVOKE DELETE, TRUNCATE ON clinic_merchant_link, payment_order, payment_attempt, webhook_event, refund,
  ops_ticket, payment_alert, reconciliation_run, processed_event FROM mugizh_mod_payments;
