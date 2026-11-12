# The migrations of one service, as an image: migrate/migrate plus that service's migrations/
# folder in /migrations. Kubernetes runs it as a Job before the service starts, the same way
# docker-compose runs the *-migrate containers (there the folder is mounted instead).
#
# Build from the repository root, once per service:
#   docker build -f deploy/migrate/Dockerfile --build-arg SERVICE=social -t social-migrate .
#
# Run it with the same arguments as in docker-compose.yml:
#   social-migrate -path=/migrations -database=postgres://... up
#
# The image only copies files, so building it for another CPU needs no emulation.
ARG MIGRATE_VERSION=v4.18.1
FROM migrate/migrate:${MIGRATE_VERSION}

# platform, social or realtime
ARG SERVICE
COPY services/${SERVICE}/migrations /migrations
