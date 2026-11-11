-- A player can block another player of the same game. Blocking is one-way: a row (A, B) means
-- A blocked B, nothing about B blocking A. The social service keeps the pair's friendship and
-- friend requests in step with it (blocking removes them), under the same pair lock as friends.
CREATE TABLE blocks (
    game_id    uuid        NOT NULL,
    blocker_id uuid        NOT NULL,
    blocked_id uuid        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, blocker_id, blocked_id),
    CHECK (blocker_id <> blocked_id),
    -- both players must exist and belong to the same game
    FOREIGN KEY (blocker_id, game_id) REFERENCES players (id, game_id),
    FOREIGN KEY (blocked_id, game_id) REFERENCES players (id, game_id)
);

-- the primary key finds the players someone blocked; this finds who blocked a player
CREATE INDEX blocks_blocked_idx ON blocks (game_id, blocked_id);
