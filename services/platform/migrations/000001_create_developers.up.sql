-- Developers who sign up in the dashboard. Created in the platform schema
-- (platform_user's search_path), so the table name is not qualified.
create table developers (
  id uuid primary key,
  email text unique not null,             -- stored lowercase by the service
  password_hash text not null,            -- bcrypt
  name text not null,
  created_at timestamptz not null default now()
);
