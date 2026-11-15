DROP INDEX IF EXISTS players_deleted_idx;
DROP INDEX IF EXISTS players_game_external_live_key;

-- The old rule allows one record per (game_id, external_id), deleted or not, so the deleted
-- records have to go first. They have no clan, friends, requests or blocks left.
DELETE FROM players WHERE deleted_at IS NOT NULL;
ALTER TABLE players ADD CONSTRAINT players_game_id_external_id_key UNIQUE (game_id, external_id);

ALTER TABLE players DROP COLUMN IF EXISTS deleted_at;
