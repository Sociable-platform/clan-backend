CREATE TABLE conversations (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id    uuid        NOT NULL,
    kind       text        NOT NULL CHECK (kind IN ('dm', 'clan')),
    clan_id    uuid,                     -- the clan's ID in the social service (no foreign key across schemas)
    dm_key     text,                     -- '<smaller player uuid>:<larger player uuid>', one DM per pair
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (game_id, dm_key),
    UNIQUE (game_id, clan_id),
    UNIQUE (id, game_id),                -- lets other tables check the game matches
    CHECK ((kind = 'dm')   = (dm_key  IS NOT NULL)),
    CHECK ((kind = 'clan') = (clan_id IS NOT NULL))
);
