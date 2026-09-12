Yes. I’d give Cursor a **project-level master prompt**, but I would explicitly tell it **not to over-engineer** and to implement the system incrementally.

Below is a prompt you can paste into Cursor Agent/Composer at the root of the repository.

# Gluzo Integration Gateway — Master Engineering Prompt

You are a senior Go backend engineer and system architect.

Build a production-ready integration gateway service called **Gluzo Integration Gateway**.

The immediate business use case is:

**EasyEcom → Gluzo Integration Gateway → Dabur/Uniware APIs**

The architecture must, however, be designed so that additional source/destination platforms can be added later without rewriting the core workflow engine.

Do NOT build a generic SaaS platform or dynamic mapping UI now. Build a clean, extensible integration engine for the current production use case.

---

# 1. Primary Objective

The gateway receives webhooks/events from EasyEcom.

When an order event arrives:

1. Authenticate the incoming request.
2. Validate the webhook payload.
3. Generate a correlation ID.
4. Determine which client/integration the order belongs to.
5. Create an integration workflow.
6. Queue the workflow for asynchronous processing.
7. Execute workflow actions.
8. Transform external API DTOs into Gluzo domain models.
9. Transform domain models into Dabur/Uniware DTOs.
10. Call the required external APIs.
11. Record the success/failure of every action.
12. Retry failed retryable actions.
13. Resume an interrupted workflow from the last successful action.
14. Maintain append-only structured logs.
15. Provide a protected log viewer.
16. Retain logs for 30 days.
17. Provide search/filter capabilities for logs.

The system must be reliable under duplicate webhooks, external API failures, timeouts, retries, and application restarts.

---

# 2. Technology Stack

Use:

* Go
* Gin for HTTP
* PostgreSQL for persistent relational/configuration data
* Redis for queue/job processing if appropriate
* JSONL files for integration execution logs
* Standard Go `net/http` underneath integration clients
* UUID for correlation IDs
* Environment variables for secrets/configuration
* Docker/Docker Compose for local development
* Structured JSON logging
* Go modules
* Go tests

Do not introduce MongoDB for the initial version.

Do not introduce Kafka unless there is a demonstrated requirement.

Do not introduce Kubernetes.

Keep infrastructure minimal and production-oriented.

---

# 3. Architectural Principles

Follow these principles strictly:

### Separation of concerns

External integrations must be isolated from the core domain.

Use:

```text
External API DTO
      ↓
Mapper
      ↓
Gluzo Domain Model
      ↓
Mapper
      ↓
External API DTO
```

Never allow EasyEcom DTOs to leak into Dabur code.

Never allow Dabur DTOs to leak into EasyEcom code.

---

# 4. Core Architecture

Use this conceptual architecture:

```text
                     EasyEcom
                         │
                         │ Webhook
                         ▼
                 ┌─────────────────┐
                 │ Webhook Handler │
                 └────────┬────────┘
                          │
                          ▼
                    Authentication
                          │
                          ▼
                    Event Validation
                          │
                          ▼
                    Correlation ID
                          │
                          ▼
                    Idempotency
                          │
                          ▼
                         Queue
                          │
                          ▼
                  Workflow Executor
                          │
                          ▼
                    Workflow State
                          │
             ┌────────────┼────────────┐
             │            │            │
             ▼            ▼            ▼
         Action 1      Action 2      Action N
             │            │            │
             └────────────┼────────────┘
                          ▼
                   Integration API
                          │
                          ▼
                   Action Result
                          │
              ┌───────────┴───────────┐
              ▼                       ▼
        Workflow State             JSONL Log
```

---

# 5. Project Structure

Use a clean structure similar to:

```text
gluzo-integration-gateway/
│
├── cmd/
│   └── server/
│       └── main.go
│
├── app/
│   │
│   ├── domain/
│   │   ├── order/
│   │   │   ├── order.go
│   │   │   ├── customer.go
│   │   │   └── item.go
│   │   │
│   │   ├── inventory/
│   │   │   └── inventory.go
│   │   │
│   │   └── tracking/
│   │       └── tracking.go
│   │
│   ├── integrations/
│   │   │
│   │   ├── easyecom/
│   │   │   ├── client.go
│   │   │   ├── auth.go
│   │   │   ├── config.go
│   │   │   │
│   │   │   ├── dto/
│   │   │   │   ├── webhook/
│   │   │   │   ├── order/
│   │   │   │   ├── inventory/
│   │   │   │   └── tracking/
│   │   │   │
│   │   │   ├── endpoints/
│   │   │   │   ├── order.go
│   │   │   │   ├── inventory.go
│   │   │   │   └── tracking.go
│   │   │   │
│   │   │   └── mapper/
│   │   │       ├── order.go
│   │   │       ├── inventory.go
│   │   │       └── tracking.go
│   │   │
│   │   └── dabur/
│   │       ├── client.go
│   │       ├── auth.go
│   │       ├── config.go
│   │       │
│   │       ├── dto/
│   │       │   ├── order/
│   │       │   ├── inventory/
│   │       │   └── tracking/
│   │       │
│   │       ├── endpoints/
│   │       │   ├── order.go
│   │       │   ├── inventory.go
│   │       │   └── tracking.go
│   │       │
│   │       └── mapper/
│   │           ├── order.go
│   │           ├── inventory.go
│   │           └── tracking.go
│   │
│   ├── workflow/
│   │   ├── workflow.go
│   │   ├── state.go
│   │   ├── action.go
│   │   ├── registry.go
│   │   │
│   │   └── order_sync/
│   │       ├── workflow.go
│   │       └── actions/
│   │           ├── fetch_order.go
│   │           ├── resolve_integration.go
│   │           ├── map_order.go
│   │           ├── update_dabur_order.go
│   │           ├── fetch_inventory.go
│   │           ├── update_inventory.go
│   │           └── fetch_tracking.go
│   │
│   ├── webhook/
│   │   ├── handler.go
│   │   ├── event.go
│   │   └── validator.go
│   │
│   ├── routing/
│   │   └── integration_router.go
│   │
│   ├── queue/
│   │   ├── producer.go
│   │   ├── consumer.go
│   │   └── job.go
│   │
│   ├── workflow_state/
│   │   ├── repository.go
│   │   └── file_repository.go
│   │
│   ├── logging/
│   │   ├── logger.go
│   │   ├── integration_logger.go
│   │   ├── sanitizer.go
│   │   └── retention.go
│   │
│   ├── auth/
│   │   ├── platform.go
│   │   ├── access_token.go
│   │   └── middleware.go
│   │
│   ├── database/
│   │   ├── postgres/
│   │   └── migrations/
│   │
│   └── http/
│       ├── router.go
│       └── middleware/
│
├── configs/
│
├── storage/
│   ├── logs/
│   └── workflows/
│
├── docs/
│
├── tests/
│
├── .env.example
├── docker-compose.yml
├── Dockerfile
├── go.mod
└── README.md
```

Adjust the structure if necessary, but preserve the architectural boundaries.

---

# 6. DTO Rules

DTOs represent external API contracts.

Do NOT create one universal DTO for each business model.

For example:

```text
dto/inventory/
    get_inventory_request.go
    get_inventory_response.go
    update_inventory_request.go
    update_inventory_response.go
```

If two API operations have genuinely different JSON contracts, they must have different DTOs.

Reuse nested DTOs only when the external structure is genuinely identical.

Do not reuse a DTO merely because both APIs are conceptually "inventory".

---

# 7. Domain Model Rules

Domain models represent Gluzo's internal business representation.

Example:

```go
type Order struct {
    ExternalID    string
    ReferenceCode string
    Customer      Customer
    PaymentMode   string
    TotalAmount   float64
    Items         []OrderItem
}
```

Domain models must NOT contain external API-specific JSON tags or naming unless absolutely necessary.

The domain must not know about EasyEcom or Dabur.

---

# 8. Mapper Rules

Mappers translate:

```text
EasyEcom DTO → Domain
Domain → Dabur DTO
```

Mappers may:

* rename fields
* restructure nested objects
* convert types
* combine fields
* split fields
* translate enum values
* normalize external representations

Example:

```go
func ToDaburOrder(
    order domain.Order,
) dabur.UpdateOrderRequest
```

Do not put HTTP calls inside mappers.

Do not put database calls inside mappers.

Do not put authentication inside mappers.

Mappers must remain deterministic and testable.

---

# 9. Integration Client Rules

Each external integration gets its own client.

Example:

```go
type Client struct {
    config     Config
    httpClient *http.Client
}
```

The client handles:

* base URL
* authentication
* HTTP request creation
* headers
* timeout
* response parsing
* external API errors

The client must not contain workflow orchestration.

---

# 10. Endpoint Rules

Each external API operation should have an explicit method.

Example:

```go
func (c *Client) GetOrder(
    ctx context.Context,
    orderID string,
) (*dto.GetOrderResponse, error)
```

```go
func (c *Client) UpdateOrder(
    ctx context.Context,
    request dto.UpdateOrderRequest,
) (*dto.UpdateOrderResponse, error)
```

Do not create one generic `CallAPI()` function and put all business behavior into it.

Generic HTTP functionality belongs in the client.

Business operation semantics belong in endpoint methods.

---

# 11. Webhook Processing

Endpoint:

```text
POST /webhooks/easyecom
```

Processing:

```text
Receive request
    ↓
Validate platform API key
    ↓
Validate platform access token
    ↓
Validate payload
    ↓
Generate correlation ID
    ↓
Check idempotency
    ↓
Create event
    ↓
Persist/queue event
    ↓
Return 200/202
```

Do NOT synchronously call Dabur from the webhook handler.

The handler must remain fast.

---

# 12. Two-Tier Authentication

Implement two authentication layers.

### Tier 1: Platform API Key

Example:

```http
X-API-Key: <platform-key>
```

This identifies/authorizes the platform.

Example:

```text
EasyEcom → Platform API Key
```

### Tier 2: Integration Access Token

Example:

```http
Authorization: Bearer <integration-token>
```

This identifies the configured client/integration.

The two credentials must be validated independently.

Do not log either credential.

Store user-created access tokens securely. Prefer storing a hash rather than plaintext.

---

# 13. Integration Routing

The router determines which integration should process the event.

Example:

```text
EasyEcom warehouse_id
        ↓
integration_routes
        ↓
DABUR
```

Do not hardcode:

```go
if warehouseID == 12345 {
    return Dabur
}
```

Instead use database-backed routing configuration.

Potential routing fields:

```text
platform
warehouse_id
marketplace_id
channel
company
integration_id
```

The actual routing key must be configurable.

---

# 14. Workflow Engine

Create:

```go
type Workflow interface {
    Execute(
        ctx context.Context,
        event WebhookEvent,
    ) error
}
```

Create:

```go
type Action interface {
    Execute(
        ctx context.Context,
        state *WorkflowState,
    ) error
}
```

Workflow state should contain:

```go
type WorkflowState struct {
    CorrelationID string
    WorkflowName string
    EventType    string

    CurrentAction int
    Status        string

    IntegrationID string

    Order     *domain.Order
    Inventory []domain.Inventory
    Tracking  *domain.Tracking

    Results map[string]any
}
```

Keep workflow state serializable.

---

# 15. Action Design

Actions should represent meaningful operations.

Good:

```text
FetchOrder
ResolveIntegration
MapOrder
UpdateDaburOrder
FetchInventory
UpdateInventory
FetchTracking
```

Bad:

```text
ExtractSKU
ExtractPhone
ExtractName
CreateString
ValidateString
```

Do not over-fragment actions.

---

# 16. Action Execution

Actions execute sequentially initially.

Example:

```text
Fetch Order
    ↓
Resolve Integration
    ↓
Map Order
    ↓
Update Dabur Order
    ↓
Fetch Inventory
    ↓
Update Inventory
    ↓
Fetch Tracking
```

Design the workflow engine so parallel/conditional execution can be added later, but do NOT implement a complex DAG engine now.

---

# 17. Workflow Resume

Every successfully completed action must update workflow state.

Example:

```json
{
  "correlation_id": "INT-123",
  "workflow": "ORDER_SYNC",
  "current_action": 4,
  "last_successful_action": "UPDATE_DABUR_ORDER",
  "next_action": "FETCH_INVENTORY",
  "status": "FAILED"
}
```

If the process crashes:

```text
Read workflow state
      ↓
Find next action
      ↓
Resume there
```

Do not replay successful actions unnecessarily.

---

# 18. Action-Level Retry

Retries must happen at action level.

Example:

```text
Fetch Order              ✓
Map Order                ✓
Update Dabur Order       ✓
Fetch Inventory          ✗
                         ↓
                       Retry
                         ↓
                       Retry
                         ↓
                       Success
```

Do NOT restart the entire workflow unless explicitly required.

Each action should have:

```text
retryable
max_retries
attempt
timeout
failure behavior
```

Use exponential backoff with jitter for external API retries where appropriate.

Do not retry obviously non-retryable errors such as invalid request/authentication errors.

---

# 19. Idempotency

Assume EasyEcom may send duplicate webhooks.

The system must prevent duplicate business operations.

Create an idempotency mechanism using a stable event identifier if available.

If no reliable event ID exists, derive an appropriate idempotency key using stable external identifiers.

The idempotency record should contain:

```text
idempotency_key
correlation_id
event_type
status
created_at
processed_at
```

Design it so concurrent duplicate requests cannot both start the same workflow.

---

# 20. Logging

Do NOT use MongoDB initially.

Use date-partitioned JSONL files.

Example:

```text
storage/
└── logs/
    ├── 2026-09-12/
    │   ├── integration.jsonl
    │   └── error.jsonl
    │
    ├── 2026-09-13/
    │   ├── integration.jsonl
    │   └── error.jsonl
```

Each line must be a valid JSON document.

Example:

```json
{
  "timestamp": "2026-09-12T10:30:01Z",
  "correlation_id": "INT-1001",
  "workflow": "ORDER_SYNC",
  "integration": "DABUR",
  "action": "FETCH_ORDER",
  "status": "SUCCESS",
  "attempt": 1,
  "duration_ms": 420
}
```

---

# 21. Logs Must Be Append-Only

Do NOT modify previous log entries.

If an action fails:

```json
{
  "action": "FETCH_INVENTORY",
  "status": "FAILED",
  "attempt": 1
}
```

and succeeds on retry:

```json
{
  "action": "FETCH_INVENTORY",
  "status": "SUCCESS",
  "attempt": 2
}
```

Keep both.

Workflow state is mutable.

Execution logs are append-only.

This separation is intentional.

---

# 22. Logging Sensitive Data

Create a sanitizer.

Never log:

```text
Authorization
X-API-Key
access_token
api_key
password
secret
client_secret
```

Redact sensitive customer/payment information where necessary.

Example:

```json
{
  "Authorization": "***REDACTED***"
}
```

Do not log secrets even at debug level.

---

# 23. Log Viewer

Create an internal protected log viewer.

Example routes:

```text
GET /admin/logs
GET /admin/logs/search
GET /admin/logs/:correlationId
```

Protect it with a secure admin token.

Support:

```text
correlation_id
order_id
external_order_id
integration
platform
action
status
date_from
date_to
```

The UI should display the execution flow chronologically.

Example:

```text
Order #12345

✓ WEBHOOK_RECEIVED
✓ RESOLVE_INTEGRATION
✓ FETCH_ORDER
✓ MAP_ORDER
✓ UPDATE_DABUR_ORDER
✗ FETCH_INVENTORY
  Attempt 1: timeout
✓ FETCH_INVENTORY
  Attempt 2
✓ UPDATE_INVENTORY
```

---

# 24. 30-Day Log Retention

Implement daily cleanup.

Keep approximately the latest 30 days.

Delete older log directories.

Do not delete active/current log files.

Retention must be safe and idempotent.

---

# 25. Workflow State Storage

Initially use file-based workflow state.

Example:

```text
storage/
└── workflows/
    └── active/
        ├── INT-1001.json
        └── INT-1002.json
```

Updates must be atomic.

Use:

```text
write temporary file
      ↓
fsync if appropriate
      ↓
atomic rename
```

Avoid partially written state files.

Design the repository behind an interface:

```go
type WorkflowStateRepository interface {
    Get(ctx context.Context, correlationID string) (*WorkflowState, error)
    Save(ctx context.Context, state *WorkflowState) error
}
```

This allows future replacement with PostgreSQL/Redis/MongoDB without rewriting workflow logic.

---

# 26. Queue

Implement a queue abstraction:

```go
type Queue interface {
    Publish(ctx context.Context, job Job) error
    Consume(ctx context.Context) (<-chan Job, error)
}
```

For the first implementation, Redis-backed processing is acceptable.

Keep queue-specific implementation outside the workflow engine.

---

# 27. Database

Use PostgreSQL for structured configuration and persistent state that benefits from relational constraints.

Suggested entities:

```text
platforms
integrations
integration_routes
integration_credentials
idempotency_records
```

Potential structure:

```text
platforms
---------
id
name
type
status

integrations
------------
id
platform_id
name
source_platform
destination_platform
status

integration_routes
------------------
id
integration_id
route_type
route_value
status
```

Credentials must be stored securely.

Do not expose credentials through API responses.

---

# 28. Error Handling

Create meaningful error categories:

```text
validation_error
authentication_error
authorization_error
network_error
timeout_error
rate_limit_error
external_api_error
mapping_error
workflow_error
non_retryable_error
```

External errors should preserve:

```text
HTTP status
external error code
external message
correlation ID
```

without leaking secrets.

---

# 29. HTTP Client Resilience

Every external API call must have:

* context cancellation
* timeout
* retry policy where appropriate
* status code handling
* response body size protection
* structured errors

Do not use infinite timeouts.

Do not retry every HTTP status.

Suggested behavior:

```text
400 → no retry
401 → no retry
403 → no retry
404 → usually no retry
408 → retry
429 → retry respecting Retry-After
500 → retry
502 → retry
503 → retry
504 → retry
```

Make this configurable where appropriate.

---

# 30. Configuration

Use environment variables for secrets and deployment configuration.

Create:

```text
.env.example
```

Example:

```text
APP_ENV=development
APP_PORT=8080

DATABASE_URL=

REDIS_URL=

EASYECOM_BASE_URL=
EASYECOM_API_KEY=

DABUR_BASE_URL=
DABUR_API_KEY=

LOG_DIRECTORY=./storage/logs
WORKFLOW_DIRECTORY=./storage/workflows

LOG_RETENTION_DAYS=30

ADMIN_LOG_VIEWER_TOKEN=
```

Do not commit real credentials.

---

# 31. Testing

Write tests for:

### DTO parsing

```text
EasyEcom webhook JSON → DTO
```

### Mappers

```text
EasyEcom DTO → Domain
Domain → Dabur DTO
```

### Routing

```text
warehouse_id → Dabur
warehouse_id → unknown
```

### Authentication

```text
valid platform key
invalid platform key
valid access token
invalid access token
```

### Idempotency

```text
same webhook twice
```

must not execute the business workflow twice.

### Workflow

```text
all actions succeed
```

### Retry

```text
action fails → retry → succeeds
```

### Resume

```text
action 1 ✓
action 2 ✓
action 3 ✗
process crashes
restart
resume action 3
```

### Logging

Verify valid JSONL output and sensitive-field sanitization.

### Retention

Verify files older than 30 days are removed.

---

# 32. Observability

Every operation must have:

```text
correlation_id
workflow_id if applicable
integration_id
external_order_id
action
attempt
timestamp
duration
status
```

The correlation ID must propagate through:

```text
Webhook
→ Queue
→ Worker
→ Workflow
→ Action
→ Integration Client
→ Logs
```

This is mandatory.

---

# 33. API Design

Internal/admin endpoints should be versioned where appropriate.

Example:

```text
POST /webhooks/easyecom

GET /health
GET /ready

GET /admin/logs
GET /admin/logs/search
GET /admin/logs/:correlationId
```

Health endpoint should not expose sensitive configuration.

---

# 34. Security

Implement:

* secure token comparison
* hashed user access tokens
* token expiration
* token revocation
* request validation
* maximum request body size
* timeout protection
* log sanitization
* secure HTTP headers where appropriate
* no secrets in source code
* no secrets in logs
* no credentials in error messages

Admin log viewer must never be publicly accessible without authentication.

---

# 35. Documentation

Create:

```text
docs/
├── architecture.md
├── webhook-flow.md
├── workflow-engine.md
├── authentication.md
├── retry-strategy.md
├── idempotency.md
├── logging.md
├── integrations.md
└── deployment.md
```

Document the reasoning behind architectural decisions, not just implementation details.

---

# 36. Development Strategy

DO NOT attempt to implement the entire system in one giant change.

Work incrementally.

Follow this sequence:

## Phase 1 — Foundation

Implement:

```text
Go project
Gin
configuration
HTTP server
health endpoint
Docker
PostgreSQL connection
Redis connection
project structure
```

## Phase 2 — Authentication

Implement:

```text
platform API key
integration access token
middleware
secure token storage
```

## Phase 3 — EasyEcom

Implement:

```text
EasyEcom client
EasyEcom DTOs
EasyEcom webhook
EasyEcom mapper
```

## Phase 4 — Domain

Implement:

```text
Order
Customer
OrderItem
Inventory
Tracking
```

## Phase 5 — Routing

Implement:

```text
integration configuration
route resolution
warehouse/channel mapping
```

## Phase 6 — Workflow

Implement:

```text
Workflow
WorkflowState
Action
WorkflowRegistry
OrderSyncWorkflow
```

## Phase 7 — Queue

Implement:

```text
producer
consumer
worker
job lifecycle
```

## Phase 8 — Dabur

Implement:

```text
Dabur client
Dabur authentication
Dabur DTOs
Dabur endpoints
Dabur mappers
```

Use the actual Dabur/Uniware API documentation provided in the repository.

DO NOT invent API endpoints or payload fields.

If API documentation is missing, create clearly marked TODO interfaces rather than guessing.

## Phase 9 — Retry + Resume

Implement:

```text
action state
attempt tracking
backoff
resume
failure handling
```

## Phase 10 — Logging

Implement:

```text
JSONL logger
correlation ID
sanitization
date partitioning
30-day retention
```

## Phase 11 — Log Viewer

Implement:

```text
authentication
date filtering
identifier search
workflow timeline
action status
request/response metadata
errors
retry history
```

## Phase 12 — Testing

Implement comprehensive unit/integration tests.

---

# 37. Critical Constraints

DO NOT:

* hardcode Dabur warehouse IDs
* hardcode API credentials
* couple EasyEcom DTOs to Dabur DTOs
* call Dabur synchronously from webhook handlers
* retry entire workflows unnecessarily
* mutate append-only logs
* log secrets
* create one universal DTO for all endpoints
* create a giant generic API caller containing business logic
* introduce MongoDB without an actual requirement
* create a generic mapping UI now
* build a complex DAG engine now
* over-engineer abstractions that aren't currently needed
* invent external API contracts

---

# 38. Design for Future Extension

The architecture should make this possible later:

```text
EasyEcom → Dabur
EasyEcom → Client B
Shopify → Dabur
WooCommerce → ERP
Amazon → 3PL
```

Adding a connector should primarily involve:

```text
new integration client
new DTOs
new endpoints
new mappers
new workflow/actions
configuration
```

The core webhook, queue, logging, authentication, workflow state, retry and observability infrastructure should remain reusable.

---

# 39. Coding Standards

Follow idiomatic Go.

Use:

* small interfaces
* dependency injection
* explicit errors
* context propagation
* constructor functions
* table-driven tests
* meaningful package names
* interfaces at consumer boundaries
* no unnecessary abstractions

Avoid:

* global mutable state
* huge service classes
* giant switch statements
* magic constants
* unnecessary reflection
* unnecessary generics
* unnecessary frameworks

Use Go's standard library whenever possible.

---

# 40. Definition of Done

The system is considered functional when this scenario works:

```text
EasyEcom sends ORDER_CREATED
        ↓
Webhook authenticated
        ↓
Correlation ID created
        ↓
Duplicate webhook detected/handled
        ↓
Event queued
        ↓
Worker receives event
        ↓
Dabur integration resolved
        ↓
EasyEcom order fetched
        ↓
EasyEcom DTO mapped to Domain Order
        ↓
Domain Order mapped to Dabur DTO
        ↓
Dabur API called
        ↓
Success logged
        ↓
Next action executes
        ↓
An intentional failure occurs
        ↓
Failure logged
        ↓
Action retries
        ↓
Application restart simulated
        ↓
Workflow state loaded
        ↓
Workflow resumes from failed action
        ↓
Remaining actions complete
        ↓
Complete execution trace visible
        ↓
30-day log retention works
```

---

# 41. How You Should Work as Cursor Agent

Before writing significant code:

1. Inspect the existing repository.
2. Identify existing files and conventions.
3. Do not overwrite existing working functionality without reason.
4. Present the proposed implementation for the current phase.
5. Implement one phase at a time.
6. Run tests after each meaningful change.
7. Run `go vet`.
8. Run formatting.
9. Fix compilation errors immediately.
10. Update documentation when architecture changes.

Never silently invent business requirements.

If something is ambiguous, make the smallest reasonable assumption and clearly document it.

At the end of every phase, provide:

```text
Implemented:
- ...

Files changed:
- ...

Tests:
- ...

Remaining:
- ...

Architecture decisions:
- ...
```

---
