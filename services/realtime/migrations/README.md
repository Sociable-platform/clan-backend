# realtime migrations

SQL migrations for the `realtime` schema (for example `000001_create_conversations.up.sql` / `.down.sql`).

- The schema itself and the `realtime_user` database user are created by `deploy/postgres/init/01-schemas.sh`, not here.
  `realtime_user` is not allowed to create schemas.
- They are applied by the `realtime-migrate` container in `docker-compose.yml` (`docker compose up`, or
  `docker compose run --rm realtime-migrate`). The `realtime` service starts only after it succeeded.
  See docs/running-the-backend.md "Database migrations".
- Never edit a file that has already run; add the next numbered pair.
- Migrations run as `realtime_user`, so tables are created in the `realtime` schema (its `search_path`).
- Never reference another service's schema. IDs from Social (players, clans) are plain `uuid` columns without foreign keys.
- Every table has `game_id`, and Realtime's queries always filter by it.
