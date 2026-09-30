# Phase 7 — Reconciliation

Part of [EasyEcom × Vinculum](../vinculum-integration-plan.md). 30 September 2026.
**The last phase of the plan.**

**Goal:** work the gateway has stopped making progress on is visible.

---

## Why there is a phase for this

Every other safety net handles a failure it recognises: the HTTP client
retries a 5xx, the executor retries an action, the worker auto-resumes a
transient failure, the queue dead-letters a poison job, the nightly full stock
push repairs drift.

This is for the ones nothing recognised. A run that failed permanently. A run
whose worker died between the claim and the next save. A vendor rejection that
was logged correctly and then read by nobody.

**A rare failure that is invisible is worse than a common one.** A common
failure gets noticed and fixed. A rare invisible one is discovered by a
customer, months later, and by then nobody can reconstruct what happened. So
this finds nothing almost all of the time, and that is the point.

---

## What changed

```text
app/reconcile/               scanner, findings, alerter, scheduled job
app/admin/                   GET /admin/reconcile, and its template
cmd/server/main.go           scanner wired to the viewer and the scheduler
```

The nightly full stock push — the plan's second reconciliation task — was
already delivered in Phase 4, and the third, preserving a vendor's own error,
turned out to need no new code: `apperror.Info` already carries `ExternalCode`
and `ExternalMessage`, so the rejection is in the workflow state. This phase
surfaces it.

## Four reasons, because they need four different responses

Listing "stuck runs" without saying why would leave an operator to open each
one. The scan classifies:

| Reason | What it means | What to do |
|---|---|---|
| `AWAITING_RETRY` | Transient error, auto-resume budget left | Nothing — it will recover |
| `PERMANENT_FAILURE` | Non-retryable, or budget exhausted | Act |
| `ABANDONED` | Still `RUNNING`, untouched; the worker died | Act |
| `NEVER_STARTED` | Created, no action ever ran | Act |

`AWAITING_RETRY` is reported but **not counted as needing an operator**. The
classification mirrors the worker's own auto-resume rule, so a run the worker
will still pick up is never presented as a crisis. Getting this wrong in the
other direction is what makes alerting worthless: an operator who is paged for
runs that fix themselves stops reading the page.

The same reasoning is enforced in configuration — `RECONCILE_THRESHOLD` must
exceed `WORKER_RETRY_FAILED_AFTER`, or every run in ordinary retry is reported
as stuck. A misconfiguration that makes a report meaningless should fail at
start-up.

## The vendor's own words survive

"The order did not go through" is not an actionable report. "Location is
closed for dispatch" is. The finding carries the vendor's code and message
through to the page and to the execution-log entry.

## It reports and never acts

A sweep that quietly retried permanent failures would hide the pattern it
exists to reveal — and would turn a rejection the vendor means into a loop.
Resuming remains an operator's decision, one click away on the timeline page
that each finding links to.

## Two audiences, two outputs

- **A log line** per sweep, with counts by reason and the oldest age, for an
  operator or an alerting rule. Debug when nothing is found, so a real finding
  is never buried under hourly "all clear" noise.
- **An execution-log entry** per finding, under the run's own correlation ID,
  so a stuck order is searchable in the existing viewer alongside everything
  else that happened to it rather than in a separate place.

## The scheduled job does its work inline

Stock and dispatch publish queue jobs; this one does not. Those are per
location, can be slow, and benefit from the worker's retry and resume.
Reconciliation reads local state, finishes in milliseconds, and produces a
report rather than a change — there is nothing to retry and nothing to resume.

It still goes through the scheduler, for one reason: three replicas each
raising the same alert every hour would train an operator to ignore it.

---

## Exit criteria

- [x] **An order stuck at Vinculum appears in the admin viewer within the
      threshold** — asserted at the layer the criterion names: an HTTP request
      to `/admin/reconcile` whose body contains the correlation ID, the action
      it stopped at, the classification, and the vendor's own message.
- [x] `gofmt -l .`, `go vet ./...`, `go test ./...` clean.

---

## Follow-ups

1. **The scan is O(active runs) and reads every state file.** Fine at this
   scale and for an hourly sweep; if active runs ever reach tens of thousands
   the repository needs an index, which is a change behind the existing
   interface.
2. **No alert is delivered anywhere.** It is a structured log line and a log
   entry; routing it to email, Slack or a pager is deployment configuration
   rather than gateway code, and inventing a channel nobody asked for would be
   worse than leaving the hook obvious.
3. **`inventory_state` and `shipment_state` still have no retention**, carried
   over from Phases 4 and 6. Both should join the existing retention job.

---

## The plan is complete

All eight phases (0–7) are implemented, tested and committed. What remains is
not code:

- **B17** — the `ship*` address field names, the only guess in the project
  that fails silently. One test order settles it.
- **B1, B2** — the stock quantity rule and the sellable bucket, before any
  stock is published.
- **B15, B19** — three EasyEcom endpoint paths the Postman collection recorded
  without. Each fails loudly on the first call.
- **B7, B8** — the shipment status enumeration and carrier registration.
  Without them tracking numbers still reach customers; delivery status does
  not.

See [blockers.md](../blockers.md). Separately, **`go test -race` has never run
on this codebase** — Windows, no cgo — and the concurrent surface now includes
the worker, the scheduler, the rate limiter and four state stores. One run on
a Linux CI runner is the cheapest remaining assurance.
