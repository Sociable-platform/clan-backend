CREATE TABLE clans (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id      uuid        NOT NULL,
    name         text        NOT NULL,
    owner_id     uuid        NOT NULL,
    member_count int         NOT NULL DEFAULT 1 CHECK (member_count >= 0),
    max_members  int         NOT NULL DEFAULT 10 CHECK (max_members BETWEEN 2 AND 100),
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (id, game_id),
    -- the owner must be a player of the same game
    FOREIGN KEY (owner_id, game_id) REFERENCES players (id, game_id)
);

-- clan names are unique per game, ignoring upper/lower case ("Wolves" = "wolves")
CREATE UNIQUE INDEX clans_game_name_key ON clans (game_id, lower(name));
