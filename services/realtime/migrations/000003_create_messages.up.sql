CREATE TABLE messages (
    id              uuid        PRIMARY KEY,     -- UUIDv7 made in Go, so ids sort in time order
    conversation_id uuid        NOT NULL,
    game_id         uuid        NOT NULL,
    sender_id       uuid        NOT NULL,
    sender_name     text        NOT NULL,        -- the name at send time; history needs no join and survives leaving
    body            text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (conversation_id, game_id) REFERENCES conversations (id, game_id) ON DELETE CASCADE
);

-- History pages, the last message of each conversation and unread counts all read newest first.
CREATE INDEX messages_conversation_id_idx ON messages (conversation_id, id DESC);
