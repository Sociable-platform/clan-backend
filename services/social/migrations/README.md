# social migrations

SQL migrations for the `social` schema (for example `000001_create_players.up.sql` / `.down.sql`).

- The schema itself and the `social_user` database user are created by `deploy/postgres/init/01-schemas.sh`, not here.
  `social_user` is not allowed to create schemas.
- Migrations run as `social_user`, so tables are created in the `social` schema (its `search_path`).
- Never reference another service's schema.
