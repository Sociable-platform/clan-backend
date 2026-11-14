-- Transactional outbox (pkg/events). The service writes an event here in the same transaction as the
-- change it describes, so both commit or neither does. The service's relay then publishes the unsent
-- rows to RabbitMQ, oldest first, and marks each one sent once the broker has confirmed it. Sent rows
-- are deleted after a retention period. Platform and social have the same table: keep the two files identical.
CREATE TABLE outbox (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY, -- write order: the relay sends by id
    event_id   uuid        NOT NULL UNIQUE,
    event_type text        NOT NULL,                                 -- the routing key, for example friend.accepted
    payload    jsonb       NOT NULL,                                 -- the whole event, as it is published
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at    timestamptz,                                          -- null until the broker has confirmed it
    attempts   integer     NOT NULL DEFAULT 0,                       -- failed sends so far
    last_error text                                                  -- why the last send failed
);

-- the relay's lookup: unsent rows, oldest first
CREATE INDEX outbox_unsent_idx ON outbox (id) WHERE sent_at IS NULL;
-- the cleanup: sent rows older than the retention period
CREATE INDEX outbox_sent_at_idx ON outbox (sent_at) WHERE sent_at IS NOT NULL;
