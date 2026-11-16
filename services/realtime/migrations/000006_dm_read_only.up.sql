-- A DM whose friendship ended through a block stays, read-only, for both players (TEAM-283):
-- they keep the history, nobody can send, and it does not count as a friendship (no presence).
-- NULL while the two are friends; becoming friends again sets it back to NULL.
ALTER TABLE conversations ADD COLUMN read_only_since timestamptz;
