-- Distributor staff who run the admin panel. Kept apart from developers on purpose: developers
-- register themselves, admins never do (the first one is created with tools/createadmin).
create table admins (
  id uuid primary key,
  email text unique not null,             -- stored lowercase by the service
  password_hash text not null,            -- bcrypt
  name text not null,
  created_at timestamptz not null default now(),
  disabled_at timestamptz                 -- an admin who left; never deleted, the audit log points here
);
