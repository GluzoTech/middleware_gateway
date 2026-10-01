# Gluzo Integration Gateway

A production-oriented integration gateway written in Go. It receives order
webhooks from EasyEcom, converts them into Gluzo's domain model, and hands
them to a dropship fulfilment vendor through a resumable, retryable, fully
traced workflow.

The vendor side is a role-based abstraction (`app/vendor`). No vendor adapter
is implemented yet; Vinculum eRetail is the first, see
[docs/vinculum-integration-plan.md](docs/vinculum-integration-plan.md).

## Quick start

Nothing installed? This needs only a Go toolchain — no Docker, no PostgreSQL,
no Redis:

```bash
go run ./cmd/devstack    # or, where make is installed: make devstack
```

It starts an embedded PostgreSQL and an in-process Redis, stands up stub
EasyEcom and Vinculum servers, provisions the credentials, route and SKU
mapping, launches the real `cmd/server`, then posts one webhook and waits for
the order to reach the vendor. It prints the admin log URL and a curl command
for sending another, and tears everything down on Ctrl+C. `-once` exits as
soon as the order lands, which is what a smoke test wants.

The stub vendors are deliberate: every outbound base URL points at a server
the tool owns, and the generated environment is passed through `ENV_FILE` so
a `.env` holding real credentials is not read. Several Vinculum request field
names are still unverified (see [docs/blockers.md](docs/blockers.md)), so a
dev stack that could reach a real warehouse would be a way to ship a parcel
by accident.

With Docker:

```bash
cp .env.example .env
docker compose up --build -d
curl http://localhost:8080/health
```

Or, with PostgreSQL and Redis already running:

```bash
cp .env.example .env    # then edit it: DATABASE_URL and REDIS_URL at least
go run ./cmd/server
```

Both commands read `.env` from the working directory at start-up and set only
the variables the environment has not already set, so an exported value still
wins and a deployment that ships no file behaves as before. `ENV_FILE` points
at a different file. Configuration itself still comes from the environment
alone (`app/config`); the file is just a convenient way to populate it while
developing.

## Provision credentials

`gatewayctl` reads the same `.env`, so `DATABASE_URL` needs no export:

```bash
go run ./cmd/gatewayctl platform create --name easyecom --type source
go run ./cmd/gatewayctl platform create --name vinculum --type destination
go run ./cmd/gatewayctl integration create --name easyecom-vinculum --source easyecom --destination vinculum
go run ./cmd/gatewayctl token issue --integration easyecom-vinculum --name "easyecom webhook"
```

Each secret is printed once. See [docs/authentication.md](docs/authentication.md).

## Verify

```bash
gofmt -l .
go vet ./...
go test ./...          # includes integration and end-to-end tests on an embedded PostgreSQL
go test -short ./...   # unit tests only
```

The end-to-end test in `tests/` runs the full production scenario: a
duplicated EasyEcom webhook is accepted once, the worker routes, fetches,
maps and pushes the order to a fake Uniware, the destination outage exhausts
the action's retries, a simulated restart resumes the run from the failed
action to completion, the trace is served by the admin viewer, and retention
prunes old logs.

## Operate

```bash
# search the execution log and view a timeline
curl -H "Authorization: Bearer $ADMIN_LOG_VIEWER_TOKEN" "http://localhost:8080/admin/logs/search?external_order_id=9876543"
curl -H "Authorization: Bearer $ADMIN_LOG_VIEWER_TOKEN" "http://localhost:8080/admin/logs/INT-…?format=json"

# resume a failed run from its failed action
curl -X POST -H "Authorization: Bearer $ADMIN_LOG_VIEWER_TOKEN" "http://localhost:8080/admin/workflows/INT-…/resume"
```

## Layout

```text
cmd/server        process entry point
cmd/gatewayctl    operator CLI (migrations, platforms, integrations, tokens, routes)
app/admin         protected log viewer and resume endpoint
app/apperror      error categories and retry semantics
app/auth          two-tier authentication (platform key + integration token)
app/config        environment configuration
app/correlation   correlation ID propagation
app/domain        order, inventory and tracking domain models
app/event         platform-neutral event model
app/database      PostgreSQL pool, Redis client, migration runner
app/health        liveness and readiness endpoints
app/httpclient    resilient HTTP client for external APIs
app/httpserver    router, middleware, hardened HTTP server
app/idempotency   duplicate-event protection
app/integrations  platform adapters (EasyEcom origin; vendor adapters)
app/vendor        vendor/origin role contracts and the adapter registry
app/intlog        append-only JSONL execution log, sanitiser, reader, retention
app/logging       structured application logger
app/queue         job queue (in-memory and Redis Streams)
app/routing       database-backed integration routing
app/webhook       webhook intake
app/worker        job consumer, recovery and manual resume
app/workflow      workflow engine and the ORDER_SYNC workflow
app/workflowstate atomic file-based workflow state
tests/            integration tests against a real PostgreSQL
docs/             architecture and operational documentation
storage/          runtime logs and workflow state (not committed)
```

## Documentation

- [Architecture](docs/architecture.md)
- [Authentication](docs/authentication.md)
- [Webhook flow](docs/webhook-flow.md)
- [Idempotency](docs/idempotency.md)
- [Routing](docs/routing.md)
- [Workflow engine](docs/workflow-engine.md)
- [Retry strategy](docs/retry-strategy.md)
- [Logging and the admin viewer](docs/logging.md)
- [Integrations](docs/integrations.md)
- [Deployment](docs/deployment.md)
