CREATE TABLE conversation_members (
    conversation_id      uuid        NOT NULL,
    player_id            uuid        NOT NULL,   -- the social service's player ID
    game_id              uuid        NOT NULL,   -- lets startup load members per game
    display_name         text        NOT NULL,
    last_read_message_id uuid,                   -- unread counts; null = nothing read yet
    joined_at            timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (conversation_id, player_id),
    FOREIGN KEY (conversation_id, game_id) REFERENCES conversations (id, game_id) ON DELETE CASCADE
);

CREATE INDEX conversation_members_game_player_idx ON conversation_members (game_id, player_id);
