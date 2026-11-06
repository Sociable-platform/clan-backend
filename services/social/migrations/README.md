# social migrations

SQL migrations for the `social` schema (for example `000001_create_players.up.sql` / `.down.sql`).

- The schema itself and the `social_user` database user are created by `deploy/postgres/init/01-schemas.sh`, not here.
  `social_user` is not allowed to create schemas.
- **Adding the first file here? Uncomment `social-migrate` and the `social-migrate` line in social's `depends_on` in
  `docker-compose.yml`.** It is off only because golang-migrate fails on an empty folder. After that,
  `docker compose up` applies new files automatically; see the main README, "Database migrations".
  Never edit a file that has already run; add a new numbered one.
- Migrations run as `social_user`, so tables are created in the `social` schema (its `search_path`).
- Never reference another service's schema.
