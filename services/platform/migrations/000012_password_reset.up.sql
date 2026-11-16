-- TEAM-259: reset a forgotten developer password.
--
-- password_changed_at is set when a password is reset. A developer token issued before it is
-- refused, so a reset also logs out every session that used the old password. NULL: never reset.
alter table developers
  add column password_changed_at timestamptz;

-- One row per reset link that can still be used. The link carries a random token; only its
-- SHA-256 hash is stored, so reading this table is not enough to reset anyone's password.
-- A developer has at most one row: asking again replaces it, and a reset deletes it.
create table password_reset_tokens (
  token_hash text primary key,            -- hex SHA-256 of the token in the link
  developer_id uuid not null references developers (id) on delete cascade,
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);

create index password_reset_tokens_developer_id_idx on password_reset_tokens (developer_id);
