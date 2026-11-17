-- TEAM-305: who belongs to a game. Until now a game had one developer (games.developer_id). A game now has
-- members: its owner (the team lead, who may do everything) and, later, members the owner invites.
-- Phase 1 only moves the question "may this developer use this game" to this table; every game gets its
-- creator as its owner, so nothing changes for anybody. games.developer_id stays for now (the admin views and
-- the rollback path read it) and is dropped once a release has run on game_members.
create table game_members (
  game_id      uuid        not null references games (id) on delete cascade,
  developer_id uuid        not null references developers (id),
  role         text        not null check (role in ('owner', 'member')),
  created_at   timestamptz not null default now(),
  primary key (game_id, developer_id)
);

-- "The games of this developer".
create index game_members_developer_id on game_members (developer_id, created_at desc);

-- Every existing game belongs to its developer, as its owner.
insert into game_members (game_id, developer_id, role, created_at)
select id, developer_id, 'owner', created_at from games;
