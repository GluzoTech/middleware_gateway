# Architecture

## Purpose

The Gluzo Integration Gateway receives order events from a source platform
(today: EasyEcom), converts them into Gluzo's own domain model, and pushes the
result to a destination platform (today: Dabur's Uniware instance).

It is an integration *engine* built for one production use case. The
boundaries are chosen so that new sources and destinations can be added later
by writing a client, DTOs, mappers and a workflow, without touching the
webhook, queue, state, retry, logging or authentication infrastructure.

## Conceptual flow

```text
EasyEcom ──webhook──▶ Webhook Handler
                          │  authenticate (platform key + integration token)
                          │  validate payload
                          │  assign correlation ID
                          │  idempotency check
                          ▼
                        Queue (Redis)
                          │
                          ▼
                    Workflow Executor
                          │  load / create workflow state
                          ▼
              Action 1 → Action 2 → … → Action N
                          │
                          ▼
              Integration clients (EasyEcom, Dabur)
                          │
              ┌───────────┴───────────┐
              ▼                       ▼
      Workflow state (mutable)   JSONL execution log (append-only)
```

Every stage carries the same correlation ID, so one identifier retrieves the
full history of an event from the admin log viewer.

## Data-flow boundary

```text
External API DTO ──mapper──▶ Gluzo domain model ──mapper──▶ External API DTO
```

External DTOs never cross an integration boundary. EasyEcom types are only
visible inside `app/integrations/easyecom`; Dabur types only inside
`app/integrations/dabur`. The workflow engine and its actions speak the domain
model only.

## Package layout

| Package | Responsibility | Phase |
| --- | --- | --- |
| `cmd/server` | Process entry point: configuration, logging, dependency wiring, graceful shutdown | 1 |
| `app/config` | Environment-driven configuration and validation | 1 |
| `app/logging` | Process-wide structured JSON logger | 1 |
| `app/correlation` | Correlation ID generation and context propagation | 1 |
| `app/httpserver` | Gin router, shared middleware, hardened `net/http` server | 1 |
| `app/health` | Liveness and readiness endpoints | 1 |
| `app/database/postgres` | PostgreSQL connection pool | 1 |
| `app/database/redisconn` | Redis client | 1 |
| `app/database/migrations` | Versioned SQL migration runner | 1 |
| `app/auth` | Platform API keys and integration access tokens: verifiers, middleware, PostgreSQL and in-memory stores | 2 |
| `cmd/gatewayctl` | Operator CLI: migrations, platforms, integrations, tokens | 2 |
| `tests` | Integration tests against a real PostgreSQL (embedded, or `TEST_DATABASE_URL`) | 2 |
| `app/integrations/easyecom` | EasyEcom client, DTOs, endpoints, mappers | 3 |
| `app/domain` | Gluzo domain models (order, inventory, tracking) | 4 |
| `app/routing` | Database-backed integration routing | 5 |
| `app/workflow` | Workflow engine, state, actions, registry | 6 |
| `app/queue` | Queue abstraction and Redis implementation | 7 |
| `app/integrations/dabur` | Dabur/Uniware client, DTOs, endpoints, mappers | 8 |
| `app/workflow_state` | Atomic file-based workflow state repository | 9 |
| `app/logging` (integration logger) | Append-only JSONL execution logs, sanitiser, retention | 10 |
| `app/admin` | Protected log viewer | 11 |

## Boundaries that must hold

- Webhook handlers never call a destination platform synchronously.
- Mappers are pure: no HTTP, no database, no authentication.
- Integration clients never orchestrate workflows.
- Workflow actions never see HTTP routing or queue details.
- Queue implementations never leak into workflow logic.
- Workflow state is mutable; execution logs are append-only.
- Nothing logs a credential, at any level.

## Decision records

### ADR-001: Go with Gin; standard library everywhere else

Gin gives routing and middleware ergonomics without dictating structure.
Everything else (logging via `log/slog`, HTTP clients via `net/http`, JSON via
`encoding/json`, tests via `testing`) uses the standard library so the
dependency surface stays small and auditable.

### ADR-002: Configuration comes only from the environment

Secrets must never be committed, and one binary must run unchanged across
environments. `app/config` reads environment variables, treats blanks as
unset, applies defaults, validates every value and reports all problems at
once. Errors never echo connection strings.

### ADR-003: Correlation IDs are generated server-side

An inbound `X-Correlation-ID` is ignored. Accepting one would let a caller
collide with, or replay, another event's identifier, and the ID is later used
as a file name for workflow state. The generated ID is returned in the response
header so the caller can still quote it.

### ADR-004: Readiness is separate from liveness

`/health` proves the process is up and never touches dependencies, so an
orchestrator does not restart a healthy process because PostgreSQL is briefly
unreachable. `/ready` probes each dependency concurrently under one timeout and
reports only `ok`/`failed` per check; error detail goes to the server log, not
the response.

### ADR-005: Migrations are embedded SQL applied under an advisory lock

Plain SQL files keep schema changes reviewable. A small runner in
`app/database/migrations` records applied versions in `schema_migrations`,
applies each file in its own transaction, and takes a PostgreSQL advisory lock
so that two replicas starting together cannot race. This avoids a third-party
migration framework while keeping the behaviour explicit.

### ADR-006: PostgreSQL for relational configuration, files for logs and workflow state

Platforms, integrations, routes, credentials and idempotency records need
uniqueness constraints and transactions, so they live in PostgreSQL. Execution
logs are high-volume, append-only and time-partitioned, which date-partitioned
JSONL files serve well with 30-day retention by directory deletion. Workflow
state is small and per-correlation-ID, so atomic file writes are sufficient
initially; the repository sits behind an interface so it can move to a database
without touching workflow logic.

### ADR-007: Credentials are stored as SHA-256 digests and bound to a platform

Platform API keys and integration access tokens are 256-bit random values.
Only their SHA-256 digest is stored, so a database leak yields nothing usable,
and a fast hash is correct for high-entropy secrets (slow password hashes
protect low-entropy passwords). Tokens belong to exactly one integration,
carry an optional expiry and can be revoked; after both tiers verify, the
token's integration must belong to the platform that presented the key. This
keeps the tiers independently checkable while preventing a token from being
replayed through another platform's key. See [authentication.md](authentication.md).

### ADR-008: Integration tests run against a real, embedded PostgreSQL

SQL that is only exercised by mocks is unverified SQL. The `tests` package
starts an embedded PostgreSQL 16 (binaries cached after the first download)
or uses `TEST_DATABASE_URL`, applies the real migrations, and drives the
stores through their public APIs. Unit tests elsewhere use in-memory
verifiers and stay hermetic; `go test -short ./...` skips the embedded suite.
