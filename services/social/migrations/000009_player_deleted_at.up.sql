-- TEAM-249: a game can delete a player's account (DELETE /api/v1/players/me).
--
-- Social keeps the row and marks it deleted instead of removing it: the deletion time is what
-- refuses the player tokens issued before it, and Realtime's chat history keeps a player to
-- point at. A deleted player is renamed, has no clan, friends, requests or blocks, and is never
-- shown to other players.
ALTER TABLE players ADD COLUMN deleted_at timestamptz;

-- One live record per player per game. A deleted record keeps its external_id, so a game that
-- gives the same ID to a new account gets a new, empty record next to it.
ALTER TABLE players DROP CONSTRAINT players_game_id_external_id_key;
CREATE UNIQUE INDEX players_game_external_live_key
    ON players (game_id, external_id) WHERE deleted_at IS NULL;

-- Every player request checks its token against the player's deletions, so finding them is fast.
CREATE INDEX players_deleted_idx
    ON players (game_id, external_id, deleted_at) WHERE deleted_at IS NOT NULL;
