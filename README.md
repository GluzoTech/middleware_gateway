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

## Verify

```bash
gofmt -l .
go vet ./...
go test ./...
```

## Layout

```text
cmd/server        process entry point
app/config        environment configuration
app/logging       structured application logger
app/correlation   correlation ID propagation
app/httpserver    router, middleware, hardened HTTP server
app/health        liveness and readiness endpoints
app/database      PostgreSQL pool, Redis client, migration runner
docs/             architecture and operational documentation
storage/          runtime logs and workflow state (not committed)
```

## Documentation

- [Architecture](docs/architecture.md)
- [Deployment](docs/deployment.md)
