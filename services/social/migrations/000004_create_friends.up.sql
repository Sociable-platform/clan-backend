-- Friend requests and friendships. A pair of players is either strangers, has one pending request
-- (in either direction), or is friends. "Already friends" cannot be a constraint across the two
-- tables, so the repository checks it in the same transaction, with the pair locked.

CREATE TABLE friend_requests (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id     uuid        NOT NULL,
    sender_id   uuid        NOT NULL,
    receiver_id uuid        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CHECK (sender_id <> receiver_id),
    -- both players must exist and belong to the same game
    FOREIGN KEY (sender_id, game_id)   REFERENCES players (id, game_id),
    FOREIGN KEY (receiver_id, game_id) REFERENCES players (id, game_id)
);

-- at most one pending request per pair, whoever sent it (A -> B and B -> A count as the same pair)
CREATE UNIQUE INDEX friend_requests_pair_key
    ON friend_requests (game_id, LEAST(sender_id, receiver_id), GREATEST(sender_id, receiver_id));
CREATE INDEX friend_requests_receiver_idx ON friend_requests (game_id, receiver_id);
CREATE INDEX friend_requests_sender_idx   ON friend_requests (game_id, sender_id);

-- One row per friendship. The smaller ID is always player_a_id, so a pair can only be stored one way.
CREATE TABLE friendships (
    game_id     uuid        NOT NULL,
    player_a_id uuid        NOT NULL,
    player_b_id uuid        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (game_id, player_a_id, player_b_id),
    CHECK (player_a_id < player_b_id),
    FOREIGN KEY (player_a_id, game_id) REFERENCES players (id, game_id),
    FOREIGN KEY (player_b_id, game_id) REFERENCES players (id, game_id)
);

-- the primary key finds friends by player_a_id; this finds them by player_b_id
CREATE INDEX friendships_player_b_idx ON friendships (game_id, player_b_id);
