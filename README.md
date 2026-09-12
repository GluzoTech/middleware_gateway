# Gluzo Integration Gateway

A production-oriented integration gateway written in Go. It receives order
webhooks from EasyEcom, converts them into Gluzo's domain model, and
synchronises them with Dabur's Uniware APIs through a resumable, retryable,
fully traced workflow.

## Quick start

```bash
cp .env.example .env
docker compose up --build -d
curl http://localhost:8080/health
```

Or, with PostgreSQL and Redis already running:

```bash
cp .env.example .env
go run ./cmd/server
```

## Provision credentials

```bash
export DATABASE_URL=postgres://gluzo:gluzo@localhost:5432/gluzo_gateway?sslmode=disable
go run ./cmd/gatewayctl platform create --name easyecom --type source
go run ./cmd/gatewayctl platform create --name dabur --type destination
go run ./cmd/gatewayctl integration create --name easyecom-dabur --source easyecom --destination dabur
go run ./cmd/gatewayctl token issue --integration easyecom-dabur --name "easyecom webhook"
```

Each secret is printed once. See [docs/authentication.md](docs/authentication.md).

## Verify

```bash
gofmt -l .
go vet ./...
go test ./...          # includes integration tests on an embedded PostgreSQL
go test -short ./...   # unit tests only
```

## Layout

```text
cmd/server        process entry point
cmd/gatewayctl    operator CLI (migrations, platforms, integrations, tokens)
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
app/integrations  platform adapters (EasyEcom source, Dabur/Uniware destination)
app/intlog        integration execution log contract
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
- [Integrations](docs/integrations.md)
- [Deployment](docs/deployment.md)
