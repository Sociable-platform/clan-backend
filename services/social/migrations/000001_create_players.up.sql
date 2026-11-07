CREATE TABLE players (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id      uuid        NOT NULL,
    external_id  text        NOT NULL,   -- the game's own player ID, e.g. "u_42"
    display_name text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (game_id, external_id),       -- one record per player per game
    UNIQUE (id, game_id)                 -- lets other tables check the game matches
);
