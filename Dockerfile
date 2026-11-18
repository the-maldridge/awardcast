# Stage 1: build.
#
# CGO is on because the database driver is SQLite, which is C. The static
# link flags are what let stage 2 be scratch: without them the binary keeps a
# dependency on Alpine's libc and the final image will not start.
FROM golang:1.27-alpine AS build
RUN --mount=type=cache,target=/var/lib/apk \
    apk add tini-static sqlite-dev build-base

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -o /awardcast -ldflags '-extldflags "-static"' .

# Stage 2: run.
#
# No shell and no utilities, which is why there is no HEALTHCHECK instruction
# here: nothing in this image could run one. Point the orchestrator's probe at
# GET /-/alive, which the framework serves on its own without touching the
# database.
FROM scratch
COPY --from=build /sbin/tini-static /tini
COPY --from=build /awardcast /awardcast

# Where the SQLite file lives. /data is the volume; the directory has to be
# writable by whatever user the container lands on.
ENV AWARDCAST_DB=/data/award.db

# AWARDCAST_PRODUCTION=1 turns on the framework's production rules: template
# debugging refused, and the session cookie marked Secure. Leave it unset for a
# rehearsal on http://localhost, and set it anywhere there is a real audience.
# The remaining variables are documented in AGENTS.md. The two that will stop
# startup if wrong are AUTHWARE_SESSION_KEY, which must be a long random string
# that stays the same across restarts, and the htpasswd pair, which is read from
# .htpasswd and .htgroup in the working directory unless AUTHWARE_HTPASSWD_FILE
# and AUTHWARE_HTGROUP_FILE say otherwise.
ENTRYPOINT ["/tini", "/awardcast"]
