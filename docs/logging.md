# Logging

Two logs exist with different jobs.

| Log | Purpose | Where | Format |
| --- | --- | --- | --- |
| Application log | Operational events: startup, HTTP requests, failures | stdout | JSON via `log/slog` |
| Integration log | What happened to each event: every action attempt and outcome | `LOG_DIRECTORY/<YYYY-MM-DD>/` | JSONL, append-only |

## Integration log

```text
storage/logs/
├── 2026-09-12/
│   ├── integration.jsonl   every entry
│   └── error.jsonl         failed entries only
└── 2026-09-13/
    ├── integration.jsonl
    └── error.jsonl
```

Each line is one JSON document:

```json
{"timestamp":"2026-09-12T10:30:01Z","correlation_id":"INT-1001","workflow":"ORDER_SYNC","platform":"easyecom","integration":"dabur","external_order_id":"9876543","action":"FETCH_ORDER","status":"SUCCESS","attempt":1,"duration_ms":420}
```

| Field | Meaning |
| --- | --- |
| `correlation_id` | Ties the entry to the webhook, job, workflow state and every other entry of the same event |
| `workflow`, `action`, `status`, `attempt`, `duration_ms` | What ran and how it went |
| `platform` / `integration` | Source platform / destination platform |
| `external_order_id` / `order_id` | Source order identifier / destination order identifier once known |
| `error` | Category, message, retryability and external code/message when the attempt failed |
| `details`, `request`, `response` | Sanitised metadata |

Actions are the workflow's action names plus `WEBHOOK_RECEIVED`,
`WORKFLOW_STARTED`, `WORKFLOW_RESUMED`, `WORKFLOW_COMPLETED`,
`WORKFLOW_FAILED` and `WORKFLOW_SKIPPED`.

Days are partitioned in UTC, so retention and search are independent of the
host time zone.

### Append-only

Entries are appended with `O_APPEND`; nothing rewrites a line. A failed
attempt and its later success are two lines:

```json
{"action":"FETCH_INVENTORY","status":"FAILED","attempt":1,"error":{"category":"timeout_error",...}}
{"action":"FETCH_INVENTORY","status":"SUCCESS","attempt":2}
```

Workflow state (`storage/workflows/`) is the mutable, current view; the
integration log is the immutable history. The separation is deliberate.

### Sanitisation

Every entry passes through `intlog.Sanitizer` before it is written:

- Map keys containing `authorization`, `x-api-key`, `access-token`,
  `access_token`, `refresh_token`, `jwt_token`, `api_key`, `password`,
  `secret`, `token`, `cookie`, `card_number`, `cvv` (and more) are replaced
  with `***REDACTED***` at any depth.
- Free text (error messages, details) has `Bearer …`, `password=…`,
  `token: …` fragments masked.
- Personal data under keys such as `email`, `phone`, `contact_num` is masked
  (`a***@example.com`, `******1234`).

The HTTP client never logs headers or query strings at all, and strips query
strings from transport errors, so credentials do not reach the sanitiser in
the first place.

### Retention

`LOG_RETENTION_DAYS` (default 30) day directories are kept. Every
`LOG_RETENTION_INTERVAL` the retention job:

1. deletes log day directories older than the window (never today's, never
   anything whose name is not a date);
2. deletes idempotency records older than the window;
3. deletes completed workflow state files older than the window.

Active workflow state is never deleted. The job is idempotent and safe to run
at any time.

## Admin log viewer

Mounted under `/admin` only when `ADMIN_LOG_VIEWER_TOKEN` is set. Every
request needs `Authorization: Bearer <token>` (or `X-Admin-Token`); the token
is compared in constant time and responses are never cached.

| Route | Purpose |
| --- | --- |
| `GET /admin/logs` | Search page (HTML); add `format=json` for JSON |
| `GET /admin/logs/search` | JSON search |
| `GET /admin/logs/:correlationId` | Chronological timeline with the workflow state (HTML, or JSON with `format=json`) |
| `GET /admin/workflows/:correlationId` | Persisted workflow state |
| `POST /admin/workflows/:correlationId/resume` | Resume a failed run from its failed action (202, runs in the background) |

Search filters: `correlation_id`, `order_id`, `external_order_id`,
`integration`, `platform`, `workflow`, `action`, `status`, `date_from`,
`date_to`, `errors_only`, `limit`. A search without dates covers the last
seven days; a correlation ID lookup covers every day.

The timeline shows the execution flow the way the operations team reads it:

```text
✓ WEBHOOK_RECEIVED
▶ WORKFLOW_STARTED
✓ RESOLVE_INTEGRATION
✓ FETCH_ORDER
✓ MAP_ORDER
✗ UPDATE_DESTINATION_ORDER   attempt 1   external_api_error: unexpected status 503
✓ UPDATE_DESTINATION_ORDER   attempt 2
✓ FETCH_INVENTORY
✓ UPDATE_INVENTORY
– FETCH_TRACKING              skipped: no shipment yet
✓ WORKFLOW_COMPLETED
```

Searches read the JSONL files directly, newest day first, and stop at the
limit. That is adequate for 30 days of a single integration; an index can be
added behind the same reader interface if volume grows.
