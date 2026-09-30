# Docker guide

How to build, run and operate the Gluzo Integration Gateway with Docker.
Configuration reference lives in [docs/deployment.md](docs/deployment.md);
this guide covers the container mechanics only.

## What the repository ships

| File | Purpose |
| --- | --- |
| `Dockerfile` | Two-stage build: `golang:1.27-alpine` compiles `server` and `gatewayctl`, then a distroless runtime image |
| `docker-compose.yml` | Local stack: `postgres`, `redis`, `gateway` |
| `.dockerignore` | Keeps `.git`, `.env*`, `storage/`, `docs/` and `*.md` out of the build context |

Two properties of the runtime image shape most of the commands below:

- It is **distroless** (`gcr.io/distroless/static-debian12:nonroot`): no shell,
  no package manager, no `curl`. `docker compose exec gateway sh` will fail —
  that is expected, not a broken image.
- It runs as the **non-root** user `nonroot` and its entrypoint is
  `/app/server`. Both binaries are at `/app/server` and `/app/gatewayctl`.

## Prerequisites

- Docker Engine with Compose v2 (`docker compose`, not `docker-compose`)
- Host ports 5432, 6379 and 8080 free — Compose publishes all three. Stop a
  local PostgreSQL or Redis first, or comment out those `ports:` entries.

## Quick start

```bash
cp .env.example .env
docker compose up --build -d
curl http://localhost:8080/health
curl http://localhost:8080/ready
```

`make docker-up` and `make docker-down` wrap the same two commands.

`up` starts PostgreSQL and Redis first and waits for their healthchecks before
the gateway starts. Schema migrations are applied automatically by the server
on startup, so nothing else is needed for a first run.

## Everyday commands

```bash
docker compose up --build -d          # build and start the whole stack
docker compose up --build -d gateway  # rebuild and restart only the gateway
docker compose ps                     # what is running, and each service's health
docker compose logs -f gateway        # follow the gateway's structured logs
docker compose logs --tail=100 postgres
docker compose restart gateway        # restart without rebuilding
docker compose stop                   # stop, keeping containers and volumes
docker compose down                   # remove containers and the network, keep volumes
docker compose down -v                # ALSO delete the database, Redis and storage volumes
```

`docker compose down -v` is destructive: it drops the database, the queue and
every execution log. Use plain `down` unless you want a clean slate.

Code changes do not hot-reload. After editing Go source, run
`docker compose up --build -d gateway` — or, for a faster inner loop, run the
server on the host with `go run ./cmd/server` against the Compose PostgreSQL
and Redis (see [Using only the dependencies](#using-only-the-dependencies)).

## Running gatewayctl

The operator CLI is baked into the image. Which form you need depends on
whether the gateway container is already running.

Container running — `exec` bypasses the entrypoint, so name the binary directly:

```bash
docker compose exec gateway /app/gatewayctl platform list
docker compose exec gateway /app/gatewayctl platform create --name easyecom --type source
docker compose exec gateway /app/gatewayctl token issue --integration easyecom-vinculum --name "easyecom webhook"
```

Container not running — `run` keeps the image's `ENTRYPOINT`, so it must be
overridden, otherwise the arguments are passed to `/app/server`:

```bash
docker compose run --rm --entrypoint /app/gatewayctl gateway migrate
```

`run` starts `postgres` and `redis` first because of `depends_on`, and `--rm`
discards the throwaway container afterwards.

Generated API keys and tokens are printed once. Copy them out of the terminal
immediately; they cannot be recovered.

## Building the image

```bash
docker build -t gluzo/integration-gateway:dev .
docker build --build-arg VERSION=$(git describe --tags --always --dirty) -t gluzo/integration-gateway:$(git describe --tags --always) .
```

`VERSION` is stamped into `main.version` at link time; it defaults to `dev`.
Compose reads it from the environment or `.env` (`VERSION=${VERSION:-dev}`):

```bash
VERSION=$(git describe --tags --always --dirty) docker compose build gateway
```

The build stage runs `go mod download` before copying the source, so
dependency layers are cached across source-only changes. `docker build --no-cache .`
forces a clean build when a cached layer is suspect.

## Configuration

Three things feed the gateway container, in increasing order of precedence:

1. **`.env` → `env_file`** — every variable in `.env` is passed to the gateway
   container. The file is optional (`required: false`), so the stack still
   starts without it.
2. **`environment:` in `docker-compose.yml`** — `DATABASE_URL`, `REDIS_URL`,
   `LOG_DIRECTORY` and `WORKFLOW_DIRECTORY` are set here and **override
   whatever `.env` holds**. This is deliberate: inside the network the
   addresses are `postgres:5432` and `redis:6379`, not `localhost`. Editing
   those four values in `.env` has no effect on the container.
3. **`ENV` in the Dockerfile** — the defaults baked into the image.

`.env` is also the file Compose interpolates from, which is how `VERSION`
reaches the build.

`.dockerignore` excludes `.env` from the build context, so secrets never enter
an image layer; they arrive at runtime only.

The process refuses to start if a required variable is missing or malformed
and lists every problem in one message — check `docker compose logs gateway`
after a container that exits immediately.

## Data and volumes

| Volume | Holds | Lost on `down -v` |
| --- | --- | --- |
| `postgres-data` | Configuration, routing, credentials, idempotency records | yes |
| `redis-data` | Job queue (appendonly) | yes |
| `gateway-storage` | Execution logs and workflow state, mounted at `/app/storage` | yes |

Volume names are prefixed with the Compose project name (the directory name),
e.g. `middleware_integration_gateway-storage`. Confirm with `docker volume ls`.

Because the image has no shell, inspect the storage volume with a throwaway
container or copy files out:

```bash
docker run --rm -v middleware_integration_gateway-storage:/data alpine ls -R /data
docker compose cp gateway:/app/storage/logs ./storage-dump
```

Prefer the admin viewer for reading execution logs — it is what it exists for:

```bash
curl -H "Authorization: Bearer $ADMIN_LOG_VIEWER_TOKEN" \
  "http://localhost:8080/admin/logs/search?external_order_id=9876543"
```

Back up the database with a `pg_dump` run inside the `postgres` container,
which does have a shell:

```bash
docker compose exec postgres pg_dump -U gluzo gluzo_gateway > backup.sql
```

## Using only the dependencies

To debug the gateway on the host with a debugger or fast rebuilds, start the
backing services alone and run the server locally:

```bash
docker compose up -d postgres redis
go run ./cmd/server          # .env points at localhost:5432 and localhost:6379
```

The defaults in `.env.example` already match the published ports and the
`gluzo`/`gluzo` credentials.

## Health and readiness

```bash
curl http://localhost:8080/health   # liveness
curl http://localhost:8080/ready    # dependencies reachable
```

`postgres` and `redis` have Compose healthchecks; the gateway does not,
because a distroless image has no shell or `curl` for the usual
`CMD-SHELL` probe. In production, point the orchestrator's HTTP probes at
`/health` and `/ready` instead — those do not run inside the container.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `docker compose exec gateway sh` → `exec: "sh": executable file not found` | Distroless image, no shell. Name the binary (`/app/gatewayctl`) or use a throwaway `alpine` container against the volume. |
| `gatewayctl` arguments seem ignored under `docker compose run` | The `ENTRYPOINT` is `/app/server`. Add `--entrypoint /app/gatewayctl`. |
| Gateway exits immediately after `up` | Configuration validation failed, or a dependency was unreachable. `docker compose logs gateway` lists every problem at once. |
| `bind: address already in use` on 5432 / 6379 / 8080 | A local PostgreSQL, Redis or another service holds the port. Stop it, or change the host side of the `ports:` mapping. |
| Gateway starts before the database is ready | It should not — `depends_on` waits for both healthchecks. If it does, check `docker compose ps` for an unhealthy dependency. |
| Source change not reflected | `restart` reuses the old image. Use `up --build -d gateway`. |
| Permission denied writing to `/app/storage` | The container runs as `nonroot`. Use the named volume rather than a host bind mount, or `chown` the host directory to UID 65532. |
| Stale schema or data after a rebuild | The image rebuilt, the volumes did not. `docker compose down -v` for a clean slate (destructive). |

## Production notes

- The Compose file is a **local development stack**. It publishes the database
  and Redis to the host and uses throwaway credentials; do not deploy it.
- Build with a version stamp and push the image; mount `/app/storage` on
  persistent storage, since workflow state on local disk is owned by the
  instance that started the run.
- Terminate TLS at the load balancer. `/admin/*` must never be exposed without
  `ADMIN_LOG_VIEWER_TOKEN` set and TLS in front of it.
- Stop with `SIGTERM`; the server drains in-flight requests for
  `APP_SHUTDOWN_TIMEOUT`. `docker compose stop` sends it already.
- Scale by adding instances behind the same Redis stream. Intake-only
  instances run with `WORKER_ENABLED=false`.

## Windows notes

Use Docker Desktop with the WSL 2 backend; the paths and volume commands above
are unchanged. Under PowerShell, `$(...)` command substitution and inline
`VAR=value cmd` prefixes are not available:

```powershell
docker build --build-arg VERSION=$(git describe --tags --always) .   # works
$env:VERSION = (git describe --tags --always); docker compose build  # instead of VERSION=... docker compose build
```

Keep the repository inside the WSL filesystem if bind-mount performance
matters; the named volumes used here are unaffected.
