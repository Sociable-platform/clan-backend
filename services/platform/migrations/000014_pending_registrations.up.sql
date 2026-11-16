-- TEAM-285: verify a developer's email address before the account exists.
--
-- With email verification on, registering does not create a developer. It stores the registration
-- here and emails a 6-digit code to the address. Entering the code creates the developer from this
-- row and deletes the row. A registration nobody confirms simply expires, so there are no
-- unverified accounts, and a mistyped or foreign address is free again after a few minutes.
--
-- One row per email address: registering again with the same address replaces the row (and its
-- code). The row holds no plain secret: the password is already hashed the way developers.password_hash
-- is, and of the code only a SHA-256 hash is kept.
create table pending_registrations (
  email text primary key,                 -- stored lowercase by the service, like developers.email
  name text not null,
  password_hash text not null,            -- bcrypt; copied to developers when the code is confirmed
  code_hash text not null,                -- hex SHA-256 of the email and the 6-digit code
  attempts integer not null default 0,    -- wrong codes so far; too many cancel the registration
  expires_at timestamptz not null,        -- the code stops working here
  last_sent_at timestamptz not null,      -- when the code was last emailed; limits "send again"
  created_at timestamptz not null default now()
);

-- Expired rows are removed when a new registration is stored.
create index pending_registrations_expires_at_idx on pending_registrations (expires_at);
