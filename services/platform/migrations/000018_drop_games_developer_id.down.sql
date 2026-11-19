-- TEAM-310: bring games.developer_id back, as it was in 000002 (not null, references developers, with its index).
--
-- A game with several owners gets the one who joined first (ties by id); every game has at least one owner, which
-- the application keeps true (a game cannot be left without one), so the column can be not null again. It is a
-- best effort for a rollback: the old application knows one developer per game, so the others lose access to it.
alter table games add column developer_id uuid references developers (id);

update games g
set developer_id = (
  select m.developer_id from game_members m
  where m.game_id = g.id and m.role = 'owner'
  order by m.created_at, m.developer_id
  limit 1);

alter table games alter column developer_id set not null;
create index games_developer_id on games (developer_id, created_at desc);
