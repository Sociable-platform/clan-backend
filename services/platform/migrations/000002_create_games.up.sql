-- A game is an integration record for a game the developer already has, never the game itself.
create table games (
  id uuid primary key,
  developer_id uuid not null references developers(id),
  name text not null,
  description text,
  created_at timestamptz not null default now()
);

-- "list my games" filters by developer
create index games_developer_id on games (developer_id, created_at desc);
