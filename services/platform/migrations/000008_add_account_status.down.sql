alter table games
  drop column status_changed_at,
  drop column status_reason,
  drop column status;

alter table developers
  drop column status_changed_at,
  drop column status_reason,
  drop column status;
