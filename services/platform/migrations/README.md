# platform migrations

SQL migrations for the `platform` schema (for example `000001_create_developers.up.sql` / `.down.sql`).

- The schema itself and the `platform_user` database user are created by `deploy/postgres/init/01-schemas.sh`, not here.
  `platform_user` is not allowed to create schemas.
- Migrations run as `platform_user`, so tables are created in the `platform` schema (its `search_path`).
- Never reference another service's schema.
