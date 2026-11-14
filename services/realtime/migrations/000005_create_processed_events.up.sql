-- Events realtime has handled. Delivery is at least once: an event can arrive again, for example when a
-- publisher's outbox relay stops after RabbitMQ confirmed the event but before it marked it sent. The
-- consumer writes the event's ID here in the same transaction as the change the event causes, and skips
-- (and acknowledges) an event whose ID is already here. Rows older than REALTIME_PROCESSED_EVENTS_RETENTION
-- are deleted.
CREATE TABLE processed_events (
    game_id    uuid        NOT NULL,
    event_id   uuid        NOT NULL,
    handled_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, event_id)
);

-- the cleanup: rows handled before a point in time
CREATE INDEX processed_events_handled_at_idx ON processed_events (handled_at);
