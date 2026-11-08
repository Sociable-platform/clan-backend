-- Published widget configs. A row is written when a developer presses
-- Publish (or rolls back) and is never changed afterwards, so the history
-- stays intact. Players get the highest version of their game.
create table widget_versions (
                                 id uuid primary key,
                                 game_id uuid not null references games(id),
                                 version int not null,                   -- 1, 2, 3 ... per game
                                 config jsonb not null,
                                 published_at timestamptz not null default now(),
                                 published_by uuid not null references developers(id),

    -- a game never has two rows with the same version number, which also
    -- makes "latest version of a game" a fast lookup
                                 unique (game_id, version)
);