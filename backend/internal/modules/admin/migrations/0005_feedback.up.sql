-- S-189: in-app feedback and bug reports (data inventory 4.11). The message is sealed with the
-- submitter's own key (K-DP-03), so erasure crypto-shreds it (K-DP-07); the rest is codes only.
-- No health data, screenshots or logs are attached. Retention: 1 year from receipt [CONFIRM].
CREATE TABLE feedback (
  id                uuid        PRIMARY KEY,
  reference         text        NOT NULL UNIQUE CHECK (reference ~ '^FB-[0-9A-F]{10}$'),
  user_ref          uuid        NOT NULL,
  kind              text        NOT NULL CHECK (kind IN ('feedback','bug')),
  message_sealed    bytea       NOT NULL,
  screen            text        CHECK (screen ~ '^[a-z][a-z0-9_.]{0,60}$'),
  app_version       text        CHECK (app_version ~ '^[0-9A-Za-z.+\-]{1,40}$'),
  platform          text        CHECK (platform IN ('android','ios','web')),
  -- closed set of codes, present only when the user ticked "attach diagnostics"
  diagnostics       jsonb       CHECK (jsonb_typeof(diagnostics) = 'object'),
  status            text        NOT NULL DEFAULT 'new' CHECK (status IN ('new','triaged','closed')),
  received_at       timestamptz NOT NULL,
  status_changed_at timestamptz
);
CREATE INDEX feedback_user_idx ON feedback (user_ref, received_at DESC, id DESC);
CREATE INDEX feedback_received_idx ON feedback (received_at DESC, id DESC);
