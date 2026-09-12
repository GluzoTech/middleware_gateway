# Idempotency

EasyEcom retries webhook deliveries it considers failed, and a delivery can
be considered failed after the gateway has in fact accepted it (a timeout on
EasyEcom's side, for example). The gateway therefore assumes every event may
arrive more than once and guarantees that a business workflow starts at most
once per event.

## Key derivation

EasyEcom order webhooks carry no event identifier, so the key is derived
from stable external identifiers:

```text
<platform>:<event type>:<external order id>
easyecom:ORDER_CREATED:9876543
```

`order_id` is used, falling back to `invoice_id`. The event type is part of
the key so that ORDER_CREATED and a later ORDER_CONFIRMED for the same order
are distinct events, while a re-delivered ORDER_CREATED is a duplicate. Keys
longer than 200 characters are hashed with a readable prefix.

## Claiming

`idempotency_records` has the key as its primary key. Claiming is a single
statement:

```sql
INSERT INTO idempotency_records (...) VALUES (...)
ON CONFLICT (idempotency_key) DO NOTHING RETURNING created_at
```

A row comes back for exactly one of any number of concurrent claims on the
same key; the others read the existing record and are answered as
duplicates with the original correlation ID. No application-level lock is
involved, so two gateway replicas behave the same as one.

## Record lifecycle

| Status | Set by |
| --- | --- |
| `accepted` | webhook handler, immediately after the claim |
| `processing` | worker, when the workflow starts |
| `completed` | worker, when the workflow finishes |
| `failed` | worker, when the workflow fails permanently |

If publishing to the queue fails after the claim, the claim is released so
the sender's retry is accepted rather than reported as a duplicate of an
event that was never processed.

Records older than the retention window are deleted by the retention job so
the table does not grow without bound; a key older than that window would be
accepted again, which is the intended behaviour for a platform that never
re-sends events that old.

## Verification

- `app/idempotency` unit tests cover the in-memory store, including fifty
  concurrent claims yielding one winner.
- `tests/idempotency_store_test.go` runs the same scenarios, plus retention,
  against a real PostgreSQL.
- `app/webhook` tests post the same webhook twice and assert one queued job.
