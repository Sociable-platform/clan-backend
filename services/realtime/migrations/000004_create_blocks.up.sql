-- Realtime's copy of Social's blocks, so they survive a restart.
CREATE TABLE blocks (
    game_id    uuid NOT NULL,
    blocker_id uuid NOT NULL,
    blocked_id uuid NOT NULL,
    PRIMARY KEY (game_id, blocker_id, blocked_id)
);
