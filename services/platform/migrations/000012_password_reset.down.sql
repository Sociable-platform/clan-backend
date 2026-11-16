drop table password_reset_tokens;

alter table developers
  drop column password_changed_at;
