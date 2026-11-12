-- Secret keys a game's own server uses to ask for player tokens.
-- Only a hash is stored; the full key is shown once, when it is created.
create table api_keys (
  id uuid primary key,
  game_id uuid not null references games(id),
  key_prefix text not null,               -- first characters, to recognise a key in the dashboard
  key_hash text not null,                 -- sha256 (hex) of the full key
  created_at timestamptz not null default now(),
  revoked_at timestamptz                  -- set when revoked; a revoked key never works again
);

-- "list a game's keys"
create index api_keys_game_id on api_keys (game_id, created_at desc);
-- checking a key looks it up by hash, and a hash must belong to one key only
create unique index api_keys_key_hash on api_keys (key_hash);
