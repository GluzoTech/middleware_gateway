# Webhook flow

```text
POST /webhooks/easyecom[/:event]
        │
        ▼
Correlation ID assigned (server-side, returned as X-Correlation-ID)
        │
        ▼
Body size limit (HTTP_MAX_BODY_BYTES)
        │
        ▼
Access-Token "<platform key>:<integration token>" split into
X-API-Key + Authorization when the native headers are absent
        │
        ▼
Tier 1: platform API key  ──▶ 401 / 403 / 503
        │
        ▼
Tier 2: integration token ──▶ 401 / 403 / 503
        │  (token's integration must belong to the platform)
        ▼
Handler: platform must match the endpoint's platform ──▶ 403
        │
        ▼
Event type from the path segment (none = ORDER_CREATED) ──▶ 400
        │
        ▼
Platform parser: body -> one Event per order ──▶ 400
        │
        ▼
Validate each Event (identifiers, routing key, idempotency key) ──▶ 400
        │
        ▼
Idempotency claim ──▶ duplicate: reported, not queued
        │
        ▼
Publish Job to Redis Streams ──▶ failure: claim released, 503
        │
        ▼
202 accepted  (200 when every event was a duplicate or the payload was empty)
```

The handler never calls EasyEcom or a vendor. Its only I/O is one insert into
`idempotency_records` and one `XADD` per event, so it answers in
milliseconds regardless of downstream health.

## Paths and event types

| Path | Event type |
| --- | --- |
| `POST /webhooks/easyecom` | `ORDER_CREATED` |
| `POST /webhooks/easyecom/order-created` | `ORDER_CREATED` |
| `POST /webhooks/easyecom/order-confirmed` | `ORDER_CONFIRMED` |
| `POST /webhooks/easyecom/order-cancelled` | `ORDER_CANCELLED` |
| `POST /webhooks/easyecom/inventory-updated` | `INVENTORY_UPDATED` (rejected until its contract is confirmed) |
| `POST /webhooks/easyecom/tracking-updated` | `TRACKING_UPDATED` (rejected until its contract is confirmed) |

Configure each EasyEcom trigger with the matching URL and payload version V2.

## Responses

| Status | Meaning | Sender action |
| --- | --- | --- |
| 202 | At least one event accepted and queued | none |
| 200 | Every event was a duplicate, or the payload carried no orders | none |
| 400 | Unknown event, malformed payload, missing identifiers | fix configuration; retrying will not help |
| 401 / 403 | Authentication failed | fix credentials |
| 413 | Body exceeds `HTTP_MAX_BODY_BYTES` | split the batch |
| 503 | PostgreSQL or Redis unavailable | retry later |

Every response body carries `correlation_id`; accepted responses list one
entry per event with its own correlation ID and `accepted` or `duplicate`.

Example accepted response:

```json
{
  "status": "accepted",
  "correlation_id": "INT-2f5c…",
  "events": [
    {"external_order_id": "9876543", "correlation_id": "INT-2f5c…", "status": "accepted"}
  ]
}
```

## What travels on the queue

The job payload is the platform-neutral `webhook.Event`: platform, event
type, correlation ID, integration ID, external order ID, reference code,
invoice number, routing key (`warehouse_id=12345`), idempotency key, receipt
time, and the original order object exactly as EasyEcom sent it. The worker
decodes that object with the EasyEcom DTOs, so nothing is lost between the
HTTP layer and the workflow.

## Batches

A V1-style payload may carry several orders. Each becomes its own event,
idempotency claim, correlation ID and job. The first event shares the
request's correlation ID so the HTTP log line and the workflow line up.
