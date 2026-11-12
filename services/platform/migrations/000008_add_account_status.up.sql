-- Access switch for the admin panel. A game is served only when the game and its developer are
-- both active. Existing rows become active through the column default.
-- status_reason is a code (non_payment | abuse | other); it is checked by the service, not here,
-- so a new reason does not need a migration.
alter table developers
  add column status text not null default 'active' check (status in ('active', 'suspended')),
  add column status_reason text,
  add column status_changed_at timestamptz;

alter table games
  add column status text not null default 'active' check (status in ('active', 'suspended')),
  add column status_reason text,
  add column status_changed_at timestamptz;
