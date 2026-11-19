# social migrations

SQL migrations for the `social` schema (for example `000001_create_players.up.sql` / `.down.sql`).

- The schema itself and the `social_user` database user are created by `deploy/postgres/init/01-schemas.sh`, not here.
  `social_user` is not allowed to create schemas.
- `docker compose up` applies new files automatically; see docs/running-the-backend.md "Database migrations".
  Never edit a file that has already run; add a new numbered one (the next number after the last file here).
- Migrations run as `social_user`, so tables are created in the `social` schema (its `search_path`).
- Never reference another service's schema.
- They are applied by the `social-migrate` container in `docker-compose.yml`
  (`docker compose up`, or `docker compose run --rm social-migrate`).
