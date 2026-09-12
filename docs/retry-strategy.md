# Retry strategy

Retries exist at three layers, each with a distinct job.

## 1. HTTP attempt retries (`app/httpclient`)

Every outbound call has a per-attempt timeout and a bounded retry policy
(default three attempts, 500 ms base delay doubling to 10 s, equal jitter).
They cover blips: a dropped connection, a 503 during a deploy, a 429 with
`Retry-After`.

| Status | Category | Retried |
| --- | --- | --- |
| 400, 422 | validation | no |
| 401 | authentication | no (EasyEcom's client refreshes its JWT once and repeats the call) |
| 403 | authorization | no |
| 404, 409, other 4xx | external API | no |
| 408 | timeout | yes |
| 429 | rate limit | yes, waiting at least `Retry-After` (capped at 60 s) |
| 5xx | external API | yes |
| connection error, per-attempt timeout | network / timeout | yes |
| caller's context cancelled | internal | no |

Bodies are re-sent from memory on every attempt, so only requests the
caller could safely repeat should be routed through a retrying client. The
gateway's calls are idempotent by construction: lookups, and order creation
keyed by the source order code.

## 2. Action retries (`app/workflow`)

When an HTTP call has exhausted its attempts the action fails with the last
error. The workflow engine decides, per action, whether to try the whole
action again after a longer backoff (default three attempts, 2 s base
doubling to 30 s). This layer covers outages that last seconds to minutes
rather than milliseconds. `MAP_ORDER` never retries: mapping is
deterministic. Inventory and tracking actions are optional: after their
budget they are marked `SKIPPED` and the order is still synchronised.

## 3. Workflow resume (`app/workflow` + worker)

After an action's budget is spent the run is `FAILED` with its position and
error persisted. Nothing is lost: the run can be resumed from the failed
action once the cause is fixed (credentials rotated, destination back up),
and a worker restart automatically resumes runs that were interrupted
mid-flight. Resuming never replays succeeded actions.

## What is never retried

- Validation and mapping errors: the input will not change by waiting.
- Authentication and authorization errors, apart from the single JWT refresh.
- 4xx responses other than 408 and 429.
- Anything after the caller's context was cancelled (shutdown).

## Jitter

Both HTTP and action backoff use equal jitter, `delay/2 + rand * delay/2`,
so many workers retrying against one recovering API spread their load
instead of hammering it in lockstep.
