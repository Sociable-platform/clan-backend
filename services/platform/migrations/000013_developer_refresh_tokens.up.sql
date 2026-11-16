-- TEAM-281: refresh tokens for developer sessions.
--
-- A login creates a session and its first refresh token. The token is a random value kept in
-- an HttpOnly cookie of the dashboard; only its SHA-256 hash is stored here, so reading this
-- table is not enough to continue anyone's session.
--
-- Each refresh replaces the token (rotation): the old row gets replaced_at and a new row is
-- inserted with the same session_id. A session is therefore a chain of rows, of which exactly
-- one has replaced_at NULL: the token that works now.
--
-- A row with replaced_at set is kept until it expires, on purpose. If that token is ever sent
-- again, someone is using a copy of it, and the service ends the whole session.
create table developer_refresh_tokens (
  token_hash text primary key,            -- hex SHA-256 of the token in the cookie
  developer_id uuid not null references developers (id) on delete cascade,
  session_id uuid not null,               -- one per login; shared by the tokens that replaced each other
  expires_at timestamptz not null,
  created_at timestamptz not null default now(),
  replaced_at timestamptz                 -- when a refresh replaced this token; NULL: still usable
);

-- "end every session of this developer" (password reset) and "end this session" (logout, reuse).
create index developer_refresh_tokens_developer_id_idx on developer_refresh_tokens (developer_id);
create index developer_refresh_tokens_session_id_idx on developer_refresh_tokens (session_id);
