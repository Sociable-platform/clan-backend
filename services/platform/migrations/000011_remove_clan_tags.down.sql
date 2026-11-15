-- Nothing to undo. Before 000011 clan.tags was already optional (TEAM-276 step 1), so configs
-- without it are valid for the older code too, and the removed values (always ignored) are
-- not needed back.
SELECT 1;
