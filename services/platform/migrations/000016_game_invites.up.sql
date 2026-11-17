-- TEAM-307: an owner invites a developer to a game by email.
--
-- An invite is a pending row and carries no secret: it belongs to an email address, and the person it
-- is for sees it by logging in with an account that has that address (a person with no account
-- registers first, which confirms the address when email verification is on). Accepting deletes the
-- row and creates the game_members row in one statement; declining, cancelling or letting it expire
-- deletes it and creates nothing.
--
-- One pending invite per game and email: inviting again renews the row.
create table game_invites (
  id uuid primary key default gen_random_uuid(),
  game_id uuid not null references games (id) on delete cascade,
  email text not null check (email = lower(email)),
  invited_by uuid not null references developers (id),
  created_at timestamptz not null default now(),
  expires_at timestamptz not null,
  unique (game_id, email)
);

-- "My invites" looks up by email; expired rows are removed by expiry time.
create index game_invites_email_idx on game_invites (email);
create index game_invites_expires_at_idx on game_invites (expires_at);

-- Every invite that was sent, kept for a day so creating invites can be rate limited per game and per
-- email: game_invites cannot do it, because its rows are deleted when an invite is answered.
create table game_invite_log (
  game_id uuid not null references games (id) on delete cascade,
  email text not null,
  created_at timestamptz not null default now()
);

create index game_invite_log_game_idx on game_invite_log (game_id, created_at);
create index game_invite_log_email_idx on game_invite_log (email, created_at);
