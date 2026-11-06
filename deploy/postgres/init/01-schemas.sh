#!/bin/bash
# Runs once, the first time the Postgres container starts with an empty data volume
# (files in /docker-entrypoint-initdb.d are run by the official postgres image).
#
# Creates one schema per service, each owned by its own database user:
#   platform_user -> schema platform
#   social_user   -> schema social
# Neither user can see the other's schema.
set -euo pipefail

: "${PLATFORM_DB_PASSWORD:?PLATFORM_DB_PASSWORD must be set}"
: "${SOCIAL_DB_PASSWORD:?SOCIAL_DB_PASSWORD must be set}"

psql -v ON_ERROR_STOP=1 \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  -v db="$POSTGRES_DB" \
  -v platform_password="$PLATFORM_DB_PASSWORD" \
  -v social_password="$SOCIAL_DB_PASSWORD" <<'SQL'
-- Start from nothing: ordinary users get no rights unless granted below.
REVOKE ALL ON DATABASE :"db" FROM PUBLIC;
REVOKE ALL ON SCHEMA public FROM PUBLIC;

CREATE ROLE platform_user LOGIN PASSWORD :'platform_password';
CREATE ROLE social_user   LOGIN PASSWORD :'social_password';

GRANT CONNECT ON DATABASE :"db" TO platform_user, social_user;

-- Each user owns its own schema and nothing else.
CREATE SCHEMA platform AUTHORIZATION platform_user;
CREATE SCHEMA social   AUTHORIZATION social_user;

-- Unqualified table names resolve to the user's own schema.
ALTER ROLE platform_user SET search_path = platform;
ALTER ROLE social_user   SET search_path = social;
SQL

echo "init: created schemas platform and social with their own users"
