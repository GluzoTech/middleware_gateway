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
| `QUEUE_STREAM` | `gluzo:jobs` | Redis stream holding queued jobs |
| `QUEUE_GROUP` | `gateway-workers` | Consumer group name |
| `QUEUE_BATCH_SIZE` | `10` | Jobs fetched per read |
| `QUEUE_BLOCK_TIMEOUT` | `5s` | How long a read waits for new jobs |
| `QUEUE_CLAIM_MIN_IDLE` | `60s` | Unacknowledged jobs older than this are reclaimed from dead workers |
| `QUEUE_MAX_DELIVERIES` | `5` | Deliveries before a job is dead-lettered to `<stream>:dead` |
| `QUEUE_MAX_LEN` | `100000` | Approximate cap on stream length |
| `EASYECOM_BASE_URL` | `https://api.easyecom.io` | EasyEcom API host |
| `EASYECOM_API_KEY` | | Account API key sent as `X-API-Key` |
| `EASYECOM_JWT_TOKEN` | | Pre-issued JWT (alternative to login) |
| `EASYECOM_EMAIL`, `EASYECOM_PASSWORD`, `EASYECOM_LOCATION_KEY` | | Login credentials used to obtain a JWT |
| `EASYECOM_TIMEOUT` | `15s` | Per-attempt timeout for EasyEcom calls |
| `DABUR_BASE_URL` | | Uniware tenant host, e.g. `https://<tenant>.unicommerce.com` |
| `DABUR_USERNAME`, `DABUR_PASSWORD` | | Uniware API user (OAuth password grant) |
| `DABUR_CLIENT_ID` | `my-trusted-client` | OAuth client id documented by Uniware |
| `DABUR_DEFAULT_FACILITY` | | Facility used when a route has no destination reference |
| `DABUR_CHANNEL` | | Channel code stamped on created orders |
| `DABUR_SHELF_CODE` | `DEFAULT` | Shelf receiving inventory adjustments |
| `DABUR_VERIFICATION_REQUIRED` | `false` | Hold created orders for manual verification |
| `DABUR_TIMEOUT` | `20s` | Per-attempt timeout for Uniware calls |
| `WORKER_ENABLED` | `true` | Run the job worker in this process; `false` gives an intake-only instance |
| `WORKER_CONCURRENCY` | `4` | Jobs processed concurrently |
| `WORKER_MAX_AUTO_RESUMES` | `3` | Automatic resumes of a failed run with a transient error |
| `WORKER_RECOVERY_INTERVAL` | `5m` | How often active runs are scanned |
| `WORKER_STALE_RUNNING_AFTER` | `10m` | Age after which a running run is treated as abandoned by a periodic scan |
| `WORKER_RETRY_FAILED_AFTER` | `1m` | Cooling period before a transient failure is resumed |

When the worker is enabled, EasyEcom and Dabur credentials are required and
validated at startup. Run intake-only instances (`WORKER_ENABLED=false`)
without them. Workflow state is stored on local disk under
`WORKFLOW_DIRECTORY`, so each worker instance owns the runs it started;
scale by adding instances behind the same Redis stream.
| `LOG_DIRECTORY` | `./storage/logs` | Root of date-partitioned JSONL execution logs |
| `WORKFLOW_DIRECTORY` | `./storage/workflows` | Root of workflow state files |
| `LOG_RETENTION_DAYS` | `30` | Days of execution logs, idempotency records and completed state to keep |
| `LOG_RETENTION_INTERVAL` | `1h` | How often the retention job runs |
| `ADMIN_LOG_VIEWER_TOKEN` | | Bearer token for `/admin/*`; empty disables the viewer |

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

## Admin log viewer

Set `ADMIN_LOG_VIEWER_TOKEN` to a long random value and open
`https://<host>/admin/logs` with `Authorization: Bearer <token>`. The viewer
must sit behind TLS; never expose it without the token. See
[logging.md](logging.md) for the routes and filters.

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
