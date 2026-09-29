# Architecture

## Purpose

The Gluzo Integration Gateway receives order events from an origin platform
(today: EasyEcom), converts them into Gluzo's own domain model, and hands the
result to a dropship fulfilment vendor. No vendor adapter is implemented yet;
Vinculum eRetail is the first, see
[vinculum-integration-plan.md](vinculum-integration-plan.md).

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
              Integration clients (EasyEcom, vendor adapters)
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
visible inside `app/integrations/easyecom`, and a vendor's types only inside
that vendor's package. The workflow engine and its actions speak the domain
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
| `app/apperror` | Error categories, retry semantics, HTTP status classification, serialisable snapshots | 3 |
| `app/event` | Platform-neutral event model shared by intake, queue and workflows | 3 |
| `app/intlog` | Integration execution log: schema, recorder contract, JSONL writer, sanitiser, reader, retention | 3, 10 |
| `app/admin` | Protected log viewer: search, timeline, workflow state, resume | 11 |
| `tests` (e2e) | The definition-of-done scenario against embedded PostgreSQL, miniredis, a fake EasyEcom server and a stub vendor | 12, V0 |
| `app/httpclient` | Resilient HTTP foundation: timeouts, backoff with jitter, Retry-After, size limits | 3 |
| `app/integrations/easyecom` | EasyEcom client, DTOs, endpoints, mappers, webhook parser | 3 |
| `app/domain` | Gluzo domain models (order, inventory, tracking) | 3 |
| `app/idempotency` | Duplicate-event protection (PostgreSQL and in-memory stores) | 3 |
| `app/queue` | Job model, Publisher/Consumer contracts, in-memory queue; `redisqueue` on Redis Streams | 3 |
| `app/webhook` | Platform-neutral event model, validation, intake handler | 3 |
| `app/routing` | Database-backed integration routing scoped to the authenticated integration | 5 |
| `app/workflow` | Workflow engine: state, actions, policies, registry, executor with per-action retry and resume | 6 |
| `app/workflow/ordersync` | The ORDER_SYNC workflow, built on the `app/vendor` roles | 6, V0 |
| `app/worker` | Queue consumer, recovery of interrupted and transiently failed runs, manual resume | 7 |
| `app/vendor` | Vendor and origin role contracts, shared types, adapter registry | V0 |
| `app/workflowstate` | Atomic file-based and in-memory workflow state repositories | 9 |

Phases 1–12 are the original build. `V0`… are phases of the
[Vinculum plan](vinculum-integration-plan.md); each has a note under
[phases/](phases/).

## Boundaries that must hold

- Webhook handlers never call a vendor synchronously.
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

### ADR-009: Single-header credential carrier for EasyEcom webhooks

EasyEcom attaches exactly one header, `Access-Token`, to its webhook calls,
while the gateway requires a platform key and an integration token. Rather
than weaken the two-tier model or require a proxy to inject headers, the
webhook handler accepts `Access-Token: <platform key>:<integration token>`
and splits it into the two credentials before the normal middleware chain
runs. Each credential is still verified independently and the
platform/integration binding check still applies; the operator configures
the joined value once in EasyEcom. Native `X-API-Key` and `Authorization`
headers keep working for platforms that can send them.

### ADR-010: External contracts are grounded in documentation or flagged

No API path or field is invented. Where public documentation was readable
(Vinculum publishes a live Swagger) the DTOs follow it exactly. Where it was
not (EasyEcom's reference site is browser-rendered) the DTOs follow
EasyEcom's support documentation
and observed public payloads, and every unconfirmed name carries a `VERIFY`
or `TODO(VERIFY)` marker listed in [integrations.md](integrations.md). The
client, retry and mapping logic are independent of the exact names, so
confirming a field is a one-line change.

### ADR-011: Redis Streams with a consumer group as the job queue

A list-based queue loses a job when the worker holding it dies. A stream
with a consumer group keeps every delivered job in a pending list until it
is acknowledged, lets a surviving worker reclaim jobs idle longer than
`QUEUE_CLAIM_MIN_IDLE`, and gives each job a delivery count so a poison job
is moved to `<stream>:dead` after `QUEUE_MAX_DELIVERIES` instead of looping.
The consumer group is created from the beginning of the stream, so events
accepted before the first worker started are not skipped. The queue package
exposes `Job`, `Publisher` and `Consumer` only; nothing about streams leaks
into the workflow engine.

### ADR-012: The webhook handler answers 2xx for anything well-formed

EasyEcom counts every 4xx/5xx as a failed delivery, retries with growing
delays and disables the trigger after enough failures. So duplicates,
batches containing already-seen orders and empty payloads are all
acknowledged with 200/202; only malformed or unauthenticated requests are
rejected, and infrastructure outages return 503 so the retry is useful.

### ADR-013: Workflows speak to platforms through Source and Destination contracts

**Superseded by ADR-017.**

The ORDER_SYNC actions call `Source.FetchOrder`, `Destination.PrepareOrder`,
`Destination.SubmitOrder` and so on; they never see an EasyEcom or Uniware
type. Mapping to the destination document (`MAP_ORDER`) is a separate action
from sending it (`UPDATE_DESTINATION_ORDER`) so that a mapping defect and a
destination outage are distinguishable in the log and retried differently
(never versus five times). The prepared document is stored opaquely in the
workflow state, so a resumed run submits exactly what was mapped. Adding a
destination is an adapter plus routes; the workflow does not change.

### ADR-014: Recovery resumes, never replays

The worker acknowledges a job only when its run reached a durable outcome.
A shutdown mid-action leaves the state `RUNNING` and the job pending; on the
next start every `RUNNING` run is resumed at its current action, and an
attempt that was in flight when the process stopped does not count against
the retry budget because its outcome is unknown. Failed runs whose last
error was transient are resumed automatically after a cooling period, up to
`WORKER_MAX_AUTO_RESUMES`; permanent failures wait for an operator. Runs
never restart from the first action.

### ADR-015: The execution log is sanitised at the write boundary and read without an index

Every entry passes through one sanitiser before it is appended: secret-like
keys are redacted at any depth, credential fragments in free text are
masked, and personal data is partially masked. Nothing upstream is trusted
to have done this already, so a new adapter cannot leak a header by
accident. The admin viewer reads the JSONL files directly, newest day first,
and stops at a result limit; for one integration over 30 days that is fast
enough, and an index can be introduced behind the same reader interface if
volume grows. The log and the state are deliberately different artefacts:
the log answers "what happened", the state answers "where are we now".

### ADR-016: State files are replaced atomically, with a bounded retry on Windows

State is written to a temporary file, fsynced and renamed over the previous
version, so a crash mid-write cannot leave a torn file. On Windows a rename
fails while another handle (the admin viewer reading the same file) is open,
so the rename is retried for up to a second. A redelivered job for a run that
is already executing in the same process waits for it instead of bouncing
through the queue, which would otherwise consume the job's delivery budget.

### ADR-017: Vendor and origin roles replace the Source/Destination split

Supersedes ADR-013.

`Source`/`Destination` encoded one assumption: Gluzo owns the stock and
pushes it outward. That held for a warehouse Gluzo controlled. It does not
hold for a dropship vendor, which owns its stock and books its own dispatch,
so stock and shipment data flow inward while orders still flow outward.

`app/vendor` therefore splits the two sides into six narrow roles whose
direction is fixed by the role: `OrderReceiver`, `StockProvider` and
`FulfilmentProvider` on the vendor side; `Origin`, `StockSink` and
`ShipmentSink` on the origin side. `Registry.Register` type-asserts an
adapter against each role and files it under the ones it satisfies, so a
partner that publishes stock by feed simply does not implement
`StockProvider` instead of declaring a method it cannot serve. An adapter
implementing no role is rejected at registration, and asking for a role a
registered vendor lacks returns a different error from asking for an unknown
vendor, because those are different operator mistakes.

`app/vendor` imports only `app/domain/*` and `app/event`, never the workflow
engine, which is why `Route` is its own type and `ordersync` converts
`workflow.RouteInfo` at one boundary. ADR-013's other decisions — mapping as
its own action, the prepared document stored opaquely, adding a partner being
an adapter plus routes — carry over unchanged.

### ADR-008: Integration tests run against a real, embedded PostgreSQL

SQL that is only exercised by mocks is unverified SQL. The `tests` package
starts an embedded PostgreSQL 16 (binaries cached after the first download)
or uses `TEST_DATABASE_URL`, applies the real migrations, and drives the
stores through their public APIs. Unit tests elsewhere use in-memory
verifiers and stay hermetic; `go test -short ./...` skips the embedded suite.
