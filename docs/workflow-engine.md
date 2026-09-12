# Workflow engine

## Concepts

| Term | Meaning |
| --- | --- |
| **Workflow** | A named, ordered list of steps (`workflow.Definition`). Registered in a `Registry` and bound to event types. |
| **Action** | One meaningful operation with a name and `Execute(ctx, *State) error`. Actions read and write the state and must tolerate running twice. |
| **Policy** | Per-action retry rules: `MaxAttempts`, per-attempt `Timeout`, backoff `BaseDelay`/`MaxDelay`, and `Optional`. |
| **State** | The serialisable progress of one run: statuses, per-action records, the event, resolved route, domain data, results and opaque destination payloads. Keyed by correlation ID. |
| **Executor** | Runs a workflow against a state, persisting after every action and recording every attempt in the integration log. |
| **Repository** | Where state lives. `workflowstate.FileRepository` writes atomically to `storage/workflows/active/<id>.json` and moves final runs to `completed/`. |

## Execution

```text
prepare        validate definition, initialise action records on first run
   │
   ▼
for each step from state.CurrentAction:
   │  attempt 1..MaxAttempts under Policy.Timeout
   │    success  → record SUCCESS, persist, next step
   │    skip     → record SKIPPED, workflow SKIPPED, stop (not an error)
   │    failure  → record FAILED (attempt N)
   │                retryable and budget left → backoff with jitter, persist, retry
   │                interrupted (shutdown)     → leave RUNNING for recovery
   │                optional action           → mark SKIPPED, continue
   │                otherwise                 → workflow FAILED, stop
   ▼
workflow COMPLETED
```

Persistence points: before each attempt (so a crash shows the action as
`RUNNING` with its attempt number), after each successful action, before
each backoff, and at every status change. The engine never replays an
action that already succeeded.

## Retry and resume

- Retries happen **per action**, never by restarting the workflow.
- Retryability comes from the error category (`apperror`): network, timeout
  and rate-limit errors and 5xx responses retry; validation, authentication,
  authorization, mapping and 4xx responses do not.
- Backoff is exponential with equal jitter: attempt *n* waits between half
  and all of `min(BaseDelay * 2^(n-1), MaxDelay)`.
- A `FAILED` run keeps `current_action`, `last_successful_action`,
  `next_action` and the attempt counters. Resuming loads the state and calls
  `Run` again; the executor starts at `current_action`. A manual resume calls
  `ResetCurrentAttempts()` first to grant a fresh retry budget; automatic
  recovery after a crash keeps the counters.
- An interrupted run (shutdown mid-action or mid-backoff) stays `RUNNING`
  and appears in `ListActive`, so a restarted worker resumes it.

Example state after a failure:

```json
{
  "correlation_id": "INT-2f5c…",
  "workflow": "ORDER_SYNC",
  "status": "FAILED",
  "current_action": 3,
  "last_successful_action": "MAP_ORDER",
  "next_action": "UPDATE_DESTINATION_ORDER",
  "actions": [
    {"name": "RESOLVE_INTEGRATION", "status": "SUCCESS", "attempt": 1},
    {"name": "FETCH_ORDER", "status": "SUCCESS", "attempt": 1},
    {"name": "MAP_ORDER", "status": "SUCCESS", "attempt": 1},
    {"name": "UPDATE_DESTINATION_ORDER", "status": "FAILED", "attempt": 5,
     "last_error": {"category": "external_api_error", "message": "unexpected status 503", "retryable": true}}
  ]
}
```

## Skips

An action may end the workflow successfully by returning `workflow.Skip(reason)`.
`RESOLVE_INTEGRATION` does this when the event's routing key has no active
route: the event is well-formed but not configured for synchronisation. The
run is persisted as `SKIPPED` with the reason and is never retried.

## ORDER_SYNC

Bound to `ORDER_CREATED` and `ORDER_CONFIRMED`.

| # | Action | Adapter call | Policy |
| --- | --- | --- | --- |
| 1 | `RESOLVE_INTEGRATION` | `routing.Resolver.Resolve` | 3 attempts (database outage), skip when no route |
| 2 | `FETCH_ORDER` | `Source.FetchOrder` | 3 attempts |
| 3 | `MAP_ORDER` | `Destination.PrepareOrder` | 1 attempt (pure mapping) |
| 4 | `UPDATE_DESTINATION_ORDER` | `Destination.SubmitOrder` | 5 attempts |
| 5 | `FETCH_INVENTORY` | `Source.FetchInventory` | 3 attempts, optional |
| 6 | `UPDATE_INVENTORY` | `Destination.UpdateInventory` | 3 attempts, optional |
| 7 | `FETCH_TRACKING` | `Source.FetchTracking` | 2 attempts, optional |

Routing runs first so that events for unconfigured warehouses cost no API
calls. Mapping is a separate action from submission so that a mapping
problem shows up in the log as `MAP_ORDER FAILED mapping_error`, distinct
from a destination outage on `UPDATE_DESTINATION_ORDER`. The prepared
document is stored opaquely in the state so a resumed run submits exactly
what was mapped.

## Adapter contracts

```go
type Source interface {
    Platform() string
    FetchOrder(ctx, event.Event) (order.Order, error)
    FetchInventory(ctx, order.Order) ([]inventory.Level, error)
    FetchTracking(ctx, order.Order) (*tracking.Shipment, error) // nil when none yet
}

type Destination interface {
    Platform() string
    PrepareOrder(ctx, order.Order, workflow.RouteInfo) (json.RawMessage, error)
    SubmitOrder(ctx, json.RawMessage, order.Order, workflow.RouteInfo) (OrderResult, error) // idempotent
    UpdateInventory(ctx, []inventory.Level, workflow.RouteInfo) (InventoryResult, error)
}
```

Adding `EasyEcom → Client B` means implementing `Destination` for Client B,
registering it under its platform name, and adding routes. The workflow, the
engine, the queue and the logs do not change.

## Extending the engine

Steps are a slice executed in order. Conditional or parallel execution can
be added by introducing a step kind that wraps several actions; the
per-action records, retry policies and persistence points already exist per
action, so that change is local to the executor loop.
