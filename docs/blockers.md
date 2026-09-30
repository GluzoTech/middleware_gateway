# Blockers and flagged decisions — feasibility

Updated 30 September 2026, after Phase 6. Companion to the
[integration plan](vinculum-integration-plan.md) and the phase notes under
[phases/](phases/).

Every open question in the build is listed here with the same four judgements:
**what we assumed**, **whether it blocks work now**, **what it costs to be
wrong**, and **how much changes when the answer arrives**. The last one is the
important column. A question whose answer changes one expression is not a
reason to stop; a question whose answer changes a data model is.

Nothing below is currently stopping development. Three items stop
**production**.

---

## 1. The three that gate production

These are the only items that can cause silent, customer-visible damage. They
must be answered before anything writes to a live system.

### B1 — Does `committedQty` rise or fall when an order is placed?

**Assumed:** sellable = `qty - committedQty` (assumption A1).

**Blocks:** Phase 4 going live. Not Phase 4 being written.

**Cost of being wrong:** this is the single most expensive unknown in the
project. If `committedQty` is already the net figure, we subtract it twice and
the catalogue reads as systematically short — or, with the opposite sign
error, we oversell stock BCPL does not have. Both are silent: the numbers look
plausible either way. Oversell surfaces as cancelled customer orders, which is
the worst way to discover an integration bug.

**Blast radius of the fix:** one expression.
`mapper.SellableQuantity` in `app/integrations/vinculum/mapper/inventory.go`
becomes `return committed`. Nothing else changes — the function has one caller
and is isolated for exactly this reason.

**How to settle it in ten minutes, without BCPL's engineers:** call
`getWhInventory` for one SKU and record `qty` and `committedQty`. Place one
order for one unit of it. Call again. If `committedQty` rose, A1 holds. This
needs only test credentials (B5), not a meeting.

**Recommendation:** treat as the first thing done the day credentials arrive.
Do not go live on the assumption.

### B2 — Which `bucket` value is sellable stock?

**Assumed:** none. `VINCULUM_SELLABLE_BUCKET` is blank, which currently accepts
every bucket.

**Blocks:** Phase 4 going live.

**Cost of being wrong:** blank is safe for reading and actively dangerous for
pushing. Accepting every bucket would publish damaged, quarantined and
in-transit stock as sellable. The test payload deliberately includes a
`Damaged` row to keep this visible.

**Blast radius of the fix:** one environment variable. No code.

**Recommendation, implemented in Phase 4:** the Vinculum adapter refuses to
start when the bucket is unset, rather than defaulting. A blank that means
"everything" is fine in a read-only phase and is a trap in a writing one, so
an unset value now stops the deployment instead of publishing damaged and
in-transit stock as sellable.

### B3 — A separate EasyEcom location for BCPL, and its `location_key`

**Assumed:** BCPL gets its own location. The architecture depends on it.

**Blocks:** Phase 3 going live. Phase 3 can be written against a fake.

**Why it matters more than it looks:** EasyEcom's JWT is scoped to one
location. Authenticating for BCPL's location makes the gateway *structurally
incapable* of overwriting Gluzo's own stock — a code defect cannot cause it.
Without a separate location, that guarantee becomes a code convention, and a
convention is one bad mapping away from zeroing Gluzo's own catalogue.

**Blast radius if refused:** this is the one item where "no" is architectural
rather than configurable. We would need an explicit SKU allowlist on every
push and an integration test proving a non-BCPL SKU cannot be written. Perhaps
a day's work, and a permanently weaker guarantee.

**Recommendation:** press for the separate location. Do not design around a
"no" until it is actually a no.

---

## 2. Everything else, in one table

| # | Question | Who answers | Assumed | Blocks | If wrong | Blast radius |
|---|---|---|---|---|---|---|
| B4 | `orderLocation` code for BCPL's warehouse | BCPL | — | Phase 5 live | Orders go nowhere, loudly | One route row |
| B5 | Test `ApiOwner` / `ApiKey`, seeded SKUs | BCPL | — | Live test of Phases 1, 5 | Cannot verify anything against the real API | None; unblocks B1 |
| B6 | Can Vinculum call a URL we host? (A2) | BCPL | No | Nothing | We poll when we could push | Additive: a new endpoint, sweep stays as backstop |
| B7 | `current_shipment_status_id` enum | EasyEcom | — | Phase 6 live | Tracking status wrong or rejected | One mapping table |
| B8 | BCPL couriers registered → `companyCarrierId` | EasyEcom | — | Phase 6 live | Shipments rejected at push | One `courier_map` table, already planned |
| B9 | Rate limit on bulk inventory | EasyEcom | Unthrottled | Nothing | Throttling under load | One limiter, same shape as Vinculum's |
| B10 | Vinculum `responseCode` success value | BCPL | `0` | Nothing | **Fails loud:** every call reads as an error | One constant |
| B11 | Vinculum request date format | BCPL | `2006-01-02 15:04:05` | Nothing | Sweeps return nothing or everything | One constant |
| B12 | Vinculum dispatch status values | BCPL | Keyword matching | Nothing | Unknown labels become `UNKNOWN`, raw text kept | One function |
| B13 | `reqType`, `filterBy`, `fulfillmentLocation`, `status[]` | BCPL | Omitted | Nothing | Possibly over-broad sweeps | Configuration; no code change |
| B14 | EasyEcom `getInventoryDetailsV2`, `Carriers/getTrackingDetails` | EasyEcom | Unverified | Nothing | Nothing — **unused under dropship** | Delete or rework in Phases 3, 6 |
| B15 | Bulk Inventory Update's **path** | EasyEcom | `/bulkInventoryUpdate` | Phase 4 live | **Fails loud:** every push errors on the first call | One constant |
| B16 | Bulk Inventory Update's per-SKU result shape | EasyEcom | Absent list = all accepted | Nothing | Partial failures reported as successes | One function |
| B17 | Vinculum's `ship*` address field names | BCPL | Documented prefix, conventional names | **Phase 5 live** | **Fails quietly:** order accepted and shipped with a field missing | Nine struct tags |
| B18 | The duplicate-order rejection's code and wording | BCPL | Keyword match, overridable by config | Nothing | Fails loudly; order stuck and visible, never duplicated | Configuration |
| B19 | `AssignShipmentDetails` and `updateTrackingStatus` **paths** | EasyEcom | Conventional names | Phase 6 live | **Fails loud:** every dispatch push errors on the first call | Two constants |

**On B17 — read this one.** It is the only unverified item in the project
that can be wrong **without anyone noticing**. Every other guess fails loudly:
a wrong path errors on the first call, a wrong `responseCode` convention
reports every call as an error, an unrecognised duplicate leaves an order
stuck and visible. A wrong shipping-address field name means Vinculum accepts
the order, ships it, and the parcel goes somewhere wrong or nowhere at all.

It is the concrete reason not to run Phase 5 against BCPL production before
B4 and B5 are settled. **One test order against their test environment
settles it**, and that is the cheapest verification in the whole list.

**On B15.** Added in Phase 3 and worth flagging clearly: the Postman
collection records the bulk operation and its request body but **not its
path**, so the path in code is a guess following the naming of the endpoints
already in the package. It is the one unverified item that will stop Phase 4
working outright — loudly, on the first call, with no risk to data. Ask for it
in the same message as the rest.

**On B16.** Less visible and therefore worth watching: if EasyEcom does report
per-SKU failures in a shape we do not recognise, a partial rejection reads as
a clean push. The sink logs every rejection it *does* recognise, so the first
live run will show whether the shape matches.

**On B10 and B11.** These are the two assumptions I am least comfortable
having made, and both were made in the way that fails loudly rather than
quietly. A wrong `responseCode` convention reports every call as an error on
the first live request; a wrong date format returns an obviously empty or
obviously enormous sweep. Neither can corrupt data. That is the reason they
are not in section 1.

**On B14.** This closed with a negative result, which is worth stating
plainly: the EasyEcom Postman collection documents the *write* endpoints, not
these two reads, so they remain unverified. Under dropship nothing calls them.
They were not confirmed on a guess, and Phases 3 and 6 decide whether they are
reworked or deleted.

---

## 3. Decisions that are ours, not theirs

### D1 — Mixed carts *(business decision, needed before Phase 5 goes live)*

An order containing both a Gluzo SKU and a BCPL SKU becomes two shipments, two
tracking numbers and two invoices under **two GST registrations** —
`shipmentDetail` returns `sellerGstNo`, which confirms BCPL invoices under
their own entity.

Two options:

| | Block mixed carts at checkout | Model split orders end to end |
|---|---|---|
| Work | Roughly a day, in the storefront | Weeks, across EasyEcom, the gateway and WooCommerce |
| Customer effect | Must place two orders | Seamless |
| Risk | Low | High: split-order state across three systems |

**Recommendation: block for v1.** It is substantially cheaper, it is
reversible, and the split-order model is a project rather than a feature. If
the commercial view is that blocking is unacceptable, that needs to be said
now, because it changes the shape of Phase 5 rather than adding to it.

### D2 — `ORDER_CANCELLED` events are accepted but not acted on

Carried over from the original build. The worker skips them. If a customer
cancels, nothing tells BCPL.

**Feasibility:** small — Vinculum publishes order operations, and a
cancellation workflow is the same shape as `ORDER_SYNC`. Not in the plan's
eight phases. **Recommendation:** decide whether v1 ships without it. A
cancellation that never reaches the warehouse is a parcel shipped to someone
who asked you not to.

### D3 — Phase 0 follow-ups, deliberately deferred

| Item | Cost | When |
|---|---|---|
| Rename `workflow.RouteInfo` to origin/vendor terms | Touches persisted state, admin viewer and log; own commit | Any time; do it alone |
| `Route.OriginReference` has no routing column | One migration | Phase 3 needs it |
| `auth.PlatformTypeDestination` wording | Cosmetic | Fold into the rename |

None of these block anything. The rename is the sort of change that is cheap
now and expensive after two more phases of persisted state have accumulated.

---

## 4. Environment gaps

| Item | Status | Risk |
|---|---|---|
| Docker image and compose | **Never run** — no Docker on the dev machine | The image is unverified. It should be built in CI before any deployment is attempted. |
| `go test -race` | **Never run** — Windows, no cgo | The scheduler and worker are concurrent. This is the one gap I would close first: run the suite with `-race` on a Linux CI runner. |

Neither is a code problem and both are cheap to close. The `-race` gap matters
more after Phase 2, which added a second concurrent subsystem.

---

## 5. What to send, and to whom

**To BCPL** — one message, answers unblock production:

1. Does `committedQty` rise or fall when an order is placed? *(B1 — we can
   test this ourselves given item 4)*
2. Which `bucket` value represents good, sellable stock? *(B2)*
3. The three-character `orderLocation` code for your warehouse. *(B4)*
4. Test-environment `ApiOwner` / `ApiKey` and a few seeded SKUs. *(B5)*
5. What `responseCode` does a successful call return, and what date format do
   `fromDate` / `toDate` expect? *(B10, B11)*
6. Can Vinculum call a URL we host? *(B6 — a "no" costs us nothing)*
7. **The exact field names of the `ship*` address block** on order create, and
   the `responseCode` returned for a duplicate order number. *(B17, B18 — B17
   is the one that fails silently)*

**To EasyEcom** — answers unblock Phases 3 and 6:

1. A separate location for BCPL and its `location_key`. *(B3 — the important
   one)*
2. The `current_shipment_status_id` enumeration. *(B7 — until it arrives the
   gateway assigns tracking numbers but does not set delivery status, by
   design: it will not guess a number that would silently put an order into
   the wrong state)*
3. Register BCPL's couriers so we can read their `companyCarrierId` values.
   *(B8)*
4. Rate limits and the maximum batch size on the bulk inventory endpoint.
   *(B9)*
5. **The exact paths of Bulk Inventory Update, AssignShipmentDetails and
   updateTrackingStatus**, and the shape of the bulk per-SKU response.
   *(B15, B16, B19 — each needed before its phase works at all)*

---

## 6. What proceeds regardless

No phase is waiting on any answer above to be **written**.

| Phase | Writable now | Live-testable now |
|---|---|---|
| 3 — SKU map, EasyEcom stock sink | **Done** | No (B3) |
| 4 — `STOCK_SYNC` | **Done** | No (B1, B2, B15) |
| 5 — Orders to Vinculum | **Done** | No (B4, B5, B17) — **and must not run against BCPL production** |
| 6 — `SHIPMENT_SYNC` | **Done** | No (B7, B8, B19) |
| 7 — Reconciliation | Yes | Yes |

The pattern that makes this possible is the same one used in Phases 0 to 2:
every unknown is isolated behind one expression, one constant, one
configuration value or one table, and is marked `TODO(VERIFY)` in code. The
cost of a wrong assumption is a one-line change, not a redesign — which is why
building ahead of the answers is the cheaper order, not the reckless one.

The one thing that would change this judgement is a **no** on B3. That is
architectural, and it is worth asking about first.
