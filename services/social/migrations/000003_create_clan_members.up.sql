CREATE TABLE clan_members (
    clan_id   uuid        NOT NULL,
    player_id uuid        NOT NULL,
    game_id   uuid        NOT NULL,
    role      text        NOT NULL CHECK (role IN ('owner', 'member')),
    joined_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (clan_id, player_id),
    UNIQUE (game_id, player_id),         -- one clan per player per game
    FOREIGN KEY (clan_id, game_id)   REFERENCES clans (id, game_id) ON DELETE CASCADE,
    FOREIGN KEY (player_id, game_id) REFERENCES players (id, game_id)
);
