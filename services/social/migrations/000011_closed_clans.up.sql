-- Open and closed clans. An open clan takes anyone who joins; a closed clan takes only the players its
-- leaders accept: a player asks (a row in clan_join_requests) and the clan's owner, or a role that may
-- remove members, accepts or declines. Every clan that exists stays open.
ALTER TABLE clans
    ADD COLUMN join_policy text NOT NULL DEFAULT 'open'
        CONSTRAINT clans_join_policy_check CHECK (join_policy IN ('open', 'closed'));

-- A pending request. Accepting, declining or taking it back deletes the row (the events say what
-- happened); joining any clan deletes the player's other requests. One request per player and clan.
CREATE TABLE clan_join_requests (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    clan_id    uuid        NOT NULL,
    player_id  uuid        NOT NULL,
    game_id    uuid        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT clan_join_requests_clan_player_key UNIQUE (clan_id, player_id),
    FOREIGN KEY (clan_id, game_id)   REFERENCES clans (id, game_id) ON DELETE CASCADE,
    FOREIGN KEY (player_id, game_id) REFERENCES players (id, game_id)
);

-- "How many open requests does this player have" and "drop the player's requests".
CREATE INDEX clan_join_requests_player_idx ON clan_join_requests (game_id, player_id);
