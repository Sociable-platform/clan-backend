-- Who changed a developer's or game's access, when and why. Append-only by convention: the service
-- only inserts and never updates or deletes a row.
-- actor is text, not a foreign key to admins, because it can also be "system:billing" later.
-- target_id has no foreign key either, so the history survives whatever happens to the target.
create table admin_audit_log (
  id uuid primary key,
  actor text not null,                    -- admin id, or "system:billing" later
  action text not null,                   -- developer.suspended | developer.resumed | game.suspended | game.resumed
  target_type text not null,              -- developer | game
  target_id uuid not null,
  reason text not null,
  note text,                              -- internal free text, never shown to the developer
  created_at timestamptz not null default now()
);

-- "history of one developer or game, newest first"
create index admin_audit_log_target on admin_audit_log (target_id, created_at desc);
