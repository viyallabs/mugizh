-- notifications module: preferences, dispatch queue, delivery log (S-141; module-boundaries §2).

-- Per-user preferences (data inventory 4.10). No phone number, email or push token here:
-- those belong to identity (encrypted) and are fetched at send time.
CREATE TABLE preference (
  user_ref            uuid        PRIMARY KEY,
  channels            jsonb       NOT NULL DEFAULT '{}'::jsonb, -- message type -> allowed channels
  discreet            boolean     NOT NULL DEFAULT false,
  quiet_hours_enabled boolean     NOT NULL DEFAULT true,
  time_zone           text        NOT NULL CHECK (length(time_zone) BETWEEN 1 AND 64),
  locale              text        NOT NULL CHECK (length(locale) BETWEEN 1 AND 16),
  updated_at          timestamptz NOT NULL DEFAULT now()
);

-- One row per notification to send. Reminder rows hold opaque references only; their wording
-- is resolved at send time. `vars` holds the typed variables of a message only while it is in
-- flight and is cleared when the dispatch reaches a final state (never kept in logs).
CREATE TABLE dispatch (
  id                uuid        PRIMARY KEY,
  dedupe_key        text        NOT NULL UNIQUE,
  kind              text        NOT NULL CHECK (kind IN ('message','reminder')),
  template_key      text        NOT NULL,
  recipient_ref     uuid        NOT NULL,
  priority          text        NOT NULL CHECK (priority IN ('critical','normal')),
  category          text        NOT NULL CHECK (category IN ('service','promotional')),
  reason            text        NOT NULL DEFAULT '',
  vars              jsonb,
  occurrence_ref    text        NOT NULL DEFAULT '',
  activity_ref      text        NOT NULL DEFAULT '',
  type_key          text        NOT NULL DEFAULT '',
  due_at            timestamptz,
  state             text        NOT NULL CHECK (state IN ('queued','sent','delivered','failed','fallback','dead_letter','blocked')),
  tried             text[]      NOT NULL DEFAULT '{}',
  channel           text        NOT NULL DEFAULT '',
  attempt           int         NOT NULL DEFAULT 0,
  channel_started_at timestamptz,
  sent_at           timestamptz,
  receipt_checked   boolean     NOT NULL DEFAULT false,
  next_attempt_at   timestamptz NOT NULL,
  lease_until       timestamptz,
  fail_reason       text        NOT NULL DEFAULT '',
  created_at        timestamptz NOT NULL,
  updated_at        timestamptz NOT NULL
);
CREATE INDEX dispatch_due_idx ON dispatch (next_attempt_at) WHERE state IN ('queued','fallback');
CREATE INDEX dispatch_sent_idx ON dispatch (sent_at) WHERE state = 'sent';
CREATE INDEX dispatch_recipient_idx ON dispatch (recipient_ref);

-- Delivery log (RET-10, 90 days): ids, channel, outcome, provider id and times. Never the
-- variables or the rendered text (K-DP-04).
CREATE TABLE delivery_log (
  id                  uuid        PRIMARY KEY,
  dispatch_id         uuid        NOT NULL,
  recipient_ref       uuid        NOT NULL,
  template_key        text        NOT NULL,
  channel             text        NOT NULL DEFAULT '',
  outcome             text        NOT NULL,
  reason              text        NOT NULL DEFAULT '',
  provider_message_id text        NOT NULL DEFAULT '',
  attempt             int         NOT NULL DEFAULT 0,
  at                  timestamptz NOT NULL
);
CREATE INDEX delivery_log_at_idx ON delivery_log (at);
CREATE INDEX delivery_log_dispatch_idx ON delivery_log (dispatch_id);

-- Events (platform/outbox TableDDL).
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
