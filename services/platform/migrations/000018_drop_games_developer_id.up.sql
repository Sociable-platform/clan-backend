-- TEAM-310: games.developer_id is gone. Since TEAM-305 who belongs to a game is game_members (role owner or
-- member), and nothing reads this column any more: not access checks, not the admin views, not suspension.
--
-- DEPLOY THIS ONE RELEASE AFTER 000015 (game_members), not with it. Until it runs, the column is the way back: the
-- release before teams still reads it, so rolling the application back is safe. Once it has run, rolling back past
-- teams needs the down migration (it restores the column from the owners) before the old application starts.
drop index if exists games_developer_id;
alter table games drop column developer_id;
