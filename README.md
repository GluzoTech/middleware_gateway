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
app/auth          two-tier authentication (platform key + integration token)
app/config        environment configuration
app/logging       structured application logger
app/correlation   correlation ID propagation
app/httpserver    router, middleware, hardened HTTP server
app/health        liveness and readiness endpoints
app/database      PostgreSQL pool, Redis client, migration runner
tests/            integration tests against a real PostgreSQL
docs/             architecture and operational documentation
storage/          runtime logs and workflow state (not committed)
```

## Documentation

- [Architecture](docs/architecture.md)
- [Authentication](docs/authentication.md)
- [Deployment](docs/deployment.md)
