-- TEAM-310: a history of who changed a game's team, for the admin panel and for finding out what happened.
--
-- One row per change, written in the transaction of the change itself, so a change and its row are both kept or
-- both lost. It says who did it (actor_id) and to whom: subject_id is the developer acted on, subject_email the
-- address of an invitation (an invitation is for an address, and the person may have no account yet).
--
--   invite_sent / invite_cancelled   an owner invites or takes an invitation back: subject_email
--   invite_accepted / invite_declined the invited developer answers: actor_id is them, subject_email the address
--   member_removed                    an owner removes somebody: subject_id
--   member_left                       somebody leaves: actor_id = subject_id
--   role_changed                      an owner promotes or demotes somebody: subject_id and the new role
--   ownership_transferred             an owner hands the game over: actor_id gives it to subject_id
--
-- Refused attempts are not rows: nothing changed. The log is not an event stream and nothing reads it but the admin
-- panel. Its rows belong to the game: they go with it.
create table game_team_log (
  id uuid primary key default gen_random_uuid(),
  game_id uuid not null references games (id) on delete cascade,
  action text not null check (action in (
    'invite_sent', 'invite_accepted', 'invite_declined', 'invite_cancelled',
    'member_removed', 'member_left', 'role_changed', 'ownership_transferred')),
  actor_id uuid not null references developers (id),
  subject_id uuid references developers (id),
  subject_email text,
  role text check (role in ('owner', 'member')),
  created_at timestamptz not null default now(),
  check (subject_id is not null or subject_email is not null)
);

-- A game's history, newest first, read a page at a time by (created_at, id).
create index game_team_log_game_idx on game_team_log (game_id, created_at desc, id desc);
