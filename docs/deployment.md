# Deployment

## Requirements

- Go 1.27 or newer (local builds)
- PostgreSQL 14 or newer
- Redis 6 or newer
- Docker with Compose v2 (optional, for the local stack)

## Configuration

All settings come from environment variables. Copy `.env.example` to `.env`
for local development; production deployments inject variables through their
secret manager. Never commit `.env`.

| Variable | Default | Purpose |
| --- | --- | --- |
| `APP_ENV` | `development` | `development`, `staging` or `production` |
| `APP_PORT` | `8080` | HTTP listen port |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `APP_SHUTDOWN_TIMEOUT` | `15s` | Grace period for in-flight requests on shutdown |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | Header read deadline |
| `HTTP_READ_TIMEOUT` | `15s` | Full request read deadline |
| `HTTP_WRITE_TIMEOUT` | `30s` | Response write deadline |
| `HTTP_IDLE_TIMEOUT` | `60s` | Keep-alive idle deadline |
| `HTTP_MAX_BODY_BYTES` | `1048576` | Cap on inbound request bodies |
| `DATABASE_URL` | required | PostgreSQL connection URL |
| `DATABASE_MAX_CONNS` | `10` | Pool size |
| `DATABASE_CONNECT_TIMEOUT` | `5s` | Connect and startup ping deadline |
| `REDIS_URL` | required | `redis://` or `rediss://` URL |
| `REDIS_CONNECT_TIMEOUT` | `5s` | Dial and startup ping deadline |
| `LOG_DIRECTORY` | `./storage/logs` | Root of date-partitioned JSONL execution logs |
| `WORKFLOW_DIRECTORY` | `./storage/workflows` | Root of workflow state files |
| `LOG_RETENTION_DAYS` | `30` | Days of execution logs to keep |

The process refuses to start if a required variable is missing or any value is
malformed, and lists every problem in one message.

## Running locally

```bash
cp .env.example .env
go run ./cmd/server
```

The server needs reachable PostgreSQL and Redis instances and exits with a
clear error if either is unavailable at startup. Pending schema migrations
are applied automatically before the server starts listening; the same
migrations can be applied ahead of a deploy with:

```bash
go run ./cmd/gatewayctl migrate
```

Platforms, integrations and credentials are provisioned with `gatewayctl`;
see [authentication.md](authentication.md).

## Docker Compose

```bash
docker compose up --build -d
curl http://localhost:8080/health
curl http://localhost:8080/ready
```

Compose starts PostgreSQL, Redis and the gateway. Service addresses are set in
`docker-compose.yml` and override anything in `.env`. Execution logs and
workflow state live in the `gateway-storage` named volume so they survive
container recreation.

## Production notes

- Build with a version stamp: `docker build --build-arg VERSION=$(git describe --tags --always) .`
- The image is distroless and runs as a non-root user; mount `/app/storage` on
  persistent storage.
- Point the orchestrator's liveness probe at `GET /health` and its readiness
  probe at `GET /ready`.
- Terminate TLS at the load balancer and configure trusted proxies before
  relying on client IPs in logs.
- Send `SIGTERM` to stop; the server drains requests for `APP_SHUTDOWN_TIMEOUT`.

## Verification

```bash
gofmt -l .
go vet ./...
go test ./...
```

The `tests` package runs against a real PostgreSQL: it uses `TEST_DATABASE_URL`
when set and otherwise starts an embedded PostgreSQL 16, downloading its
binaries once into `~/.embedded-postgres-go`. Use `go test -short ./...` to
skip it. Per-package tests that need live infrastructure are skipped unless
`TEST_DATABASE_URL` and `TEST_REDIS_URL` are set.
