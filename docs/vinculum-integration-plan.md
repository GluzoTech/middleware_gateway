# EasyEcom × Vinculum — implementation plan

Gluzo sells BCPL products on its storefront. BCPL holds the stock, ships it
and invoices it. Gluzo never takes possession. This is vendor dropship, and
the gateway has no concept of it today.

BCPL runs Vinculum eRetail. This plan makes Vinculum the gateway's only
fulfilment partner and removes the Dabur pipeline.

Sections 1–4 are the design. Section 5 is the work. Section 6 is API
reference read from the live Vinculum Swagger and the EasyEcom Postman
collection on 29 September 2026 — nothing in it is inferred.

---

## 1. Scope

**In:** EasyEcom as the order origin and the stock/shipment sink. Vinculum as
the fulfilment vendor. A role-based vendor abstraction that a second dropship
partner can be added to without touching the engine.

**Out:** Dabur. Gluzo is no longer connected to it, so the pipeline is
removed rather than left dormant:

```text
app/integrations/dabur/          whole package tree
config.Dabur and every DABUR_* variable
Dabur client and destination wiring in cmd/server/main.go
the fake Uniware server in tests/e2e_test.go
Dabur sections of docs/integrations.md and README.md
the Unicommerce link in API_Documentation_Links
```

**Untouched:** queue, worker, retry, resume, idempotency, routing, the
append-only execution log, the admin viewer, two-tier authentication, the
migration runner. All built platform-neutral; none of it changes.

---

## 2. Why the existing contracts change

`ordersync` defines `Source` (FetchOrder, FetchInventory, FetchTracking) and
`Destination` (PrepareOrder, SubmitOrder, UpdateInventory).

That shape encodes an assumption: **Gluzo owns the stock and pushes it
outward.** True for Dabur. False for BCPL.

| | Dabur (removed) | BCPL (new) |
| --- | --- | --- |
| Stock owner | Gluzo | BCPL |
| Stock direction | EasyEcom → Uniware | **Vinculum → EasyEcom** |
| Who ships | Gluzo | BCPL |
| Dispatch direction | EasyEcom already holds it | **Vinculum → EasyEcom** |
| Trigger | Webhook only | Webhook **and scheduled** |

Only the order half keeps its direction. Stock and dispatch reverse.

Reusing `Source`/`Destination` would leave adapters whose method names lie
about which way data moves — a `Destination.UpdateInventory` on an adapter
that never receives inventory. That reads as harmless in review and produces
an incident later.

---

## 3. The abstraction

Replace the source/destination split with **role interfaces** in a new
`app/vendor` package. Direction is fixed by the role.

### Vendor side — a dropship fulfilment partner

| Role | Methods | Meaning |
| --- | --- | --- |
| `OrderReceiver` | `PrepareOrder`, `SubmitOrder` | accepts orders for fulfilment |
| `StockProvider` | `FetchStock` | owns stock for its SKUs; the gateway reads |
| `FulfilmentProvider` | `FetchShipments` | ships, so owns AWB, invoice, delivery status |

### Origin side — Gluzo's OMS

| Role | Methods | Meaning |
| --- | --- | --- |
| `Origin` | `FetchOrder` | customer-facing order record |
| `StockSink` | `PushStock` | receives vendor stock as authoritative |
| `ShipmentSink` | `PushShipment` | receives externally-booked dispatch |

### Shared types

`Route`, `OrderAck`, `StockCursor`, `StockPage`, `Window`, `ShipmentPage`,
`StockResult`. All plain values.

`app/vendor` imports only `app/domain` and `app/event` — never the workflow
engine, the queue or a platform package. That is why `Route` is its own type
rather than `workflow.RouteInfo`; `ordersync` converts at the boundary.

`Route` carries two location references, and the distinction matters:

- `VendorReference` — the vendor-side location. For Vinculum, the
  three-character `orderLocation`.
- `OriginReference` — the origin-side location. For EasyEcom, the
  `location_key` whose JWT scopes stock writes to BCPL's own warehouse.

Both are route configuration. Neither appears in code.

### Registry and the extension point

`vendor.Registry` indexes adapters by platform name and discovers capability
by type assertion at registration. Adding a partner is one `Register` call.
A partner that takes orders by API but publishes stock by feed simply does
not implement `StockProvider`, rather than declaring a method it cannot
serve. An adapter implementing no role is rejected at start-up, so a wiring
mistake fails immediately instead of at the first action.

### Contract rules every adapter must honour

- `PrepareOrder` is pure. No network, no database. Failures are permanent
  and are never retried.
- `SubmitOrder` is idempotent. Resume, redelivery and retry all call it more
  than once for the same order; an order that already exists is reported
  with `Created=false`, not raised as an error.
- `PushStock` treats the quantity as authoritative and replaces what the
  origin holds. It never adds, subtracts or reconciles against the origin's
  own view.
- `PushShipment` never moves a status backwards. A "delivered" notice can
  arrive before the "shipped" one; a late arrival may correct the tracking
  number but leaves a more advanced status alone.
- No part of the gateway writes vendor stock back to the vendor.

---

## 4. Target package layout

```text
app/vendor/
├── vendor.go                    role contracts, Route, shared types
├── registry.go                  registration and capability lookup
└── vendor_test.go

app/integrations/vinculum/
├── config.go                    ApiOwner/ApiKey, base URL, location, bucket
├── client.go                    transport, static headers, envelope handling
├── endpoint_inventory.go        GetWhInventory
├── endpoint_order.go            CreateOrder
├── endpoint_shipment.go         ShipmentDetail
├── dto/inventory/               get_wh_inventory.go
├── dto/order/                   create_order_request.go, response.go
├── dto/shipment/                shipment_detail.go
├── mapper/                      inventory.go, order.go, shipment.go (pure)
└── vendor.go                    OrderReceiver + StockProvider + FulfilmentProvider

app/integrations/easyecom/
├── config.go                    per-location credentials
├── endpoint_inventory.go        + BulkInventoryUpdate
├── endpoint_tracking.go         + AssignShipmentDetails, UpdateTrackingStatus
├── source.go                    reworked to vendor.Origin
└── sink.go                      StockSink + ShipmentSink

app/scheduler/                   periodic job publisher, single-firing
app/skumap/                      Gluzo SKU ↔ vendor item code
app/workflow/ordersync/          reworked onto vendor roles
app/workflow/stocksync/          new
app/workflow/shipmentsync/       new
```

Removed: `app/integrations/dabur/`.

---

## 5. Phases

Ordered so that nothing writes into BCPL's live system until the read paths
and the SKU mapping are proven. Each phase ends with a green build and a
green test run; no phase leaves the repo broken overnight.

### Phase 0 — Remove Dabur, add the contracts

**Goal:** the engine runs on the new abstraction with no vendor implemented.

| Task | Where |
| --- | --- |
| Delete the Dabur package | `git rm -r app/integrations/dabur` |
| Write the role contracts and shared types | `app/vendor/vendor.go` |
| Write the registry | `app/vendor/registry.go` |
| Rework the workflow onto roles; convert `workflow.RouteInfo` → `vendor.Route` at the boundary | `app/workflow/ordersync/ordersync.go` |
| Point the EasyEcom source at `vendor.Origin` | `app/integrations/easyecom/source.go` |
| Drop `config.Dabur` and the `DABUR_*` block, including the `DABUR_TIMEOUT` entry in the durations list | `app/config/config.go`, `.env.example` |
| Drop the Dabur client and destination wiring from `buildWorker` | `cmd/server/main.go` |
| Replace the fake Uniware with a stub vendor implementing all three roles | `tests/e2e_test.go` |
| Rewrite the Dabur sections | `docs/integrations.md`, `docs/architecture.md`, `README.md` |
| Record the role split as an ADR, superseding ADR-013 | `docs/architecture.md` |

**Exit criteria:** `gofmt -l .` clean, `go vet ./...` clean, `go test ./...`
passing including the e2e scenario against the stub vendor. No reference to
`dabur` anywhere in the tree.

**Blocked by:** nothing. Start here.

### Phase 1 — Vinculum client and read paths

**Goal:** the gateway can read BCPL's stock and shipments. Read-only, so
nothing can be broken on BCPL's side.

| Task | Where |
| --- | --- |
| Config and validation | `vinculum/config.go` |
| Client: base URL, static `ApiOwner`/`ApiKey` headers, `responseCode`/`responseMessage` envelope | `vinculum/client.go` |
| `GetWhInventory` with paging on `hasMore` | `vinculum/endpoint_inventory.go`, `dto/inventory/` |
| `ShipmentDetail` with `date_from`/`date_to` and paging | `vinculum/endpoint_shipment.go`, `dto/shipment/` |
| Stock mapper: bucket filter, quantity rule (A1), `inventory.Level` | `vinculum/mapper/inventory.go` |
| Shipment mapper: `tracking.Shipment`, status normalisation | `vinculum/mapper/shipment.go` |
| Add `InvoiceNumber` and `SellerGSTIN` to the shipment model | `app/domain/tracking/tracking.go` |
| Revisit the two EasyEcom `TODO(VERIFY)` markers now the collection has been read | `easyecom/endpoint_inventory.go`, `endpoint_tracking.go`, `docs/integrations.md` |

**Exit criteria:** table-driven mapper tests against recorded payloads;
client tests against an `httptest` server covering success, envelope error,
paging and timeout. No live calls in tests.

**Blocked by:** BCPL open items 2 and 5 for a live smoke test. Unit work can
proceed without them; A1 is a one-expression change (section 7).

### Phase 2 — Scheduler

**Goal:** periodic work enters the existing queue.

| Task | Where |
| --- | --- |
| Named periodic jobs, each with its own interval | `app/scheduler/scheduler.go` |
| Watermark per job so a missed tick does not lose work | `app/scheduler/` + a migration |
| Single-firing across replicas via a short-lived Redis lock per job name | `app/scheduler/lock.go` |
| Wire into `run()` alongside the retention goroutine | `cmd/server/main.go` |

Scheduled jobs go through the same queue, worker, retry, resume and logging
path as webhook events. Nothing bypasses the execution log.

**Exit criteria:** two replicas under test fire a tick exactly once; a
process killed mid-tick resumes from the watermark rather than skipping.

**Blocked by:** Phase 0.

### Phase 3 — SKU mapping and EasyEcom as a sink

**Goal:** the gateway can write stock into BCPL's EasyEcom location.

| Task | Where |
| --- | --- |
| `sku_map`: Gluzo SKU ↔ vendor item code, integration-scoped, active flag, safety buffer defaulting to 0 | migration `0004_sku_map.sql`, `app/skumap/` |
| Per-location EasyEcom credentials, token cache keyed by location | `easyecom/config.go`, `easyecom/auth.go` |
| `BulkInventoryUpdate` | `easyecom/endpoint_inventory.go` |
| `StockSink` implementation, authenticating for `Route.OriginReference` | `easyecom/sink.go` |
| `gatewayctl skumap add/list/remove` | `cmd/gatewayctl/` |

An unmapped SKU fails the action with a non-retryable mapping error naming
the SKU. It must never pass through silently.

**Exit criteria:** store tests against the embedded PostgreSQL, matching the
existing `tests/routing_store_test.go` pattern. A push authenticated for
location A cannot write location B.

**Blocked by:** EasyEcom open item 6 for a live test; the code can be written
against a fake first.

### Phase 4 — `STOCK_SYNC` workflow

**Goal:** BCPL stock stays current in EasyEcom.

```text
FETCH_VENDOR_STOCK   GetWhInventory for the route's location, paged
MAP_STOCK            sellable bucket, quantity rule, SKU translation,
                     drop entries unchanged since the last push
PUSH_STOCK           BulkInventoryUpdate against BCPL's EasyEcom location
```

| Task | Where |
| --- | --- |
| Workflow definition and actions | `app/workflow/stocksync/` |
| `inventory_state`: last pushed quantity per SKU | migration `0005_inventory_state.sql` |
| Register the scheduled job | `cmd/server/main.go` |

Rules:

- Zero propagates immediately with no buffer applied.
- Clamp at 10,000 before sending (EasyEcom's cap).
- A nightly full push ignoring `inventory_state` corrects drift from a
  missed run.

**Exit criteria:** a run with no changes pushes nothing; a run with one
change pushes one SKU; the nightly sweep pushes everything.

**Blocked by:** Phases 1–3, and BCPL open items 1 and 2.

### Phase 5 — Orders to Vinculum

**Goal:** a BCPL order placed on the storefront reaches BCPL's warehouse.

| Task | Where |
| --- | --- |
| `CreateOrder` | `vinculum/endpoint_order.go`, `dto/order/` |
| Order mapper; `orderNo` = EasyEcom order id; persist `lineno` | `vinculum/mapper/order.go` |
| `OrderReceiver`; recognise Vinculum's duplicate rejection and report `Created=false` | `vinculum/vendor.go` |
| Per-vendor rate limiter for 80 calls / 5 minutes | `app/worker/` or `vinculum/client.go` |
| Route rows: `route_type = warehouse_id`, vendor reference = BCPL's `orderLocation` | `gatewayctl route add` |
| Drop the stock steps from `ORDER_SYNC` — BCPL stock is maintained by `STOCK_SYNC`, not per order | `app/workflow/ordersync/` |

**Exit criteria:** submitting the same order twice creates one order at
Vinculum; a resumed run after a simulated crash does not duplicate; the rate
limiter holds under a burst of 200 queued orders.

**Blocked by:** Phase 0, and BCPL open items 3 and 5. **Do not run this phase
against BCPL production.**

### Phase 6 — `SHIPMENT_SYNC` workflow

**Goal:** the customer sees tracking.

```text
FETCH_SHIPMENTS   ShipmentDetail from the last watermark, paged
MAP_SHIPMENTS     Vinculum shipment → tracking.Shipment
PUSH_SHIPMENT     AssignShipmentDetails, then updateTrackingStatus
```

| Task | Where |
| --- | --- |
| Workflow definition and actions | `app/workflow/shipmentsync/` |
| `AssignShipmentDetails`, `UpdateTrackingStatus` | `easyecom/endpoint_tracking.go` |
| `ShipmentSink` with a status state machine that never regresses | `easyecom/sink.go` |
| `courier_map`: Vinculum `transporter` → EasyEcom `companyCarrierId` | migration `0006_courier_map.sql` |
| Register the scheduled job | `cmd/server/main.go` |

Out-of-order arrival is normal and must be handled, not treated as an error.

**Exit criteria:** a delivered-before-shipped sequence leaves the order
delivered; a redelivered shipment event is a no-op.

**Blocked by:** Phases 1–2, and EasyEcom open items 7 and 8.

### Phase 7 — Reconciliation

| Task | Where |
| --- | --- |
| Flag orders in a non-terminal state beyond a threshold | `app/workflow/` + admin viewer |
| Nightly full stock push | Phase 4 |
| Vinculum stock rejection → dead-letter with the vendor error preserved, plus an alert | `vinculum/vendor.go`, `app/worker/` |

A stock rejection is not a customer-facing flow for v1, but it must be
visible. A rare failure that is invisible is worse than a common one.

**Exit criteria:** an order stuck at Vinculum appears in the admin viewer
within the threshold.

---

## 6. API reference

### Vinculum — authentication

Two static headers on every call. No token exchange, no refresh, no expiry
handling; the token-source machinery in the EasyEcom client has no
counterpart here.

```text
ApiOwner: <owner>
ApiKey:   <key>
```

### Vinculum — stock

```text
POST /RestWS/api/eretail/v4/stock/getWhInventory
```

Request: `skuCodes[]`, `buckets`, `locCode`, `pageNumber`, `fromDate`,
`toDate`, `reqType`.

Response: `responseCode`, `responseMessage`, `hasMore`, `response[]` of:

| Field | Meaning |
| --- | --- |
| `skuCode` | BCPL item code |
| `location` | BCPL warehouse |
| `qty` | quantity |
| `committedQty` | quantity against open orders |
| `bucket` | stock category |

`fromDate`/`toDate` and `pageNumber` make an incremental pull possible;
`hasMore` drives paging.

### Vinculum — order create

```text
POST /RestWS/api/eretail/v4/order/create
```

**Documented rate limit: 80 calls per 5 minutes** — roughly one order every
four seconds. The queue provides pacing; the limiter enforces the ceiling.

Header fields: `orderNo` (mandatory, Varchar(50)), `orderLocation`
(mandatory, Varchar(3)), `orderType`, `paymentType`, `status`, `orderDate`,
`orderCurrency`, plus the `ship*` address block. `awbNo` exists but stays
empty — BCPL books the courier.

`orderAmount[]` lines: `lineno`, `sku`, `orderQty`, `unitPrice`, `mrp`,
`discountAmt`, `taxPercentage`, `vendor`, `udf1`–`udf4`.

`orderNo` carries the EasyEcom order id. Vinculum rejects a duplicate, and
that rejection is what makes `SubmitOrder` idempotent.

`lineno` must be persisted: the shipment response is line-level.

### Vinculum — shipment

```text
POST /RestWS/api/eretail/v1/order/shipmentDetail
```

Request: `order_no[]`, `date_from`, `date_to`, `order_location`, `status[]`,
`pageNumber`, `fulfillmentLocation`, `filterBy`.

Response carries `extOrderNo`, `order_no`, `status` and a `shipDetail` block:

| Field | Maps to |
| --- | --- |
| `tracking_number` | `tracking.Shipment.TrackingNumber` |
| `transporter` | `tracking.Shipment.Carrier` |
| `tracking_url` | `tracking.Shipment.TrackingURL` |
| `shipdate` | `tracking.Shipment.ShippedAt` |
| `status` | `SourceStatus`, normalised into `Status` |
| `invoiceNo` | `tracking.Shipment.InvoiceNumber` (new) |
| `sellerGstNo` | `tracking.Shipment.SellerGSTIN` (new) |
| `delivereddate`, `deliveryNumber`, `ewbNo` | carried in the workflow state |

### EasyEcom — authentication and the warehouse boundary

```text
POST /access/token   { email, password, location_key }
```

**The returned JWT is scoped to one location.** EasyEcom's own guidance
confirms it: multi-warehouse updates require a separate login per warehouse.

Use this deliberately. BCPL gets its own location with its own
`location_key`, and the gateway authenticates for that location when writing
BCPL stock. It then becomes structurally incapable of overwriting Gluzo's own
quantities — a code defect cannot cause it.

### EasyEcom — stock write

```text
POST  Update Inventory        { "sku": "...", "quantity": 500 }
POST  Bulk Inventory Update   { "skus": [ { "sku": "...", "quantity": 4000 }, ... ] }
```

**`quantity` is absolute — it replaces the stored value, it is not a delta.**
The mapping from Vinculum is therefore a straight copy.

An unknown SKU returns an error, so `sku_map` must be correct before any
push. Values above 10,000 are silently clamped and reported, so cap on our
side and do not treat the response as a failure.

Use the bulk form.

### EasyEcom — shipment write

```text
POST  AssignShipmentDetails
{ invoiceId, courier, awbNum, companyCarrierId, shippingLabelUrl, invoiceUrl,
  origin_code, destination_code }
```

Built for an externally-booked shipment — exactly the dropship case.
`companyCarrierId` is an EasyEcom-assigned number, so BCPL's couriers must be
registered in EasyEcom and mapped from Vinculum's `transporter` string.

```text
POST  updateTrackingStatus
{ current_shipment_status_id, awb, history_scans[], estimated_delivery_date,
  delivery_date }
```

`current_shipment_status_id` is a numeric enum to be obtained from EasyEcom
and mapped from `tracking.Status`.

---

## 7. Assumptions

**A1 — `qty` is total stock and `committedQty` is the portion already
promised, so the sellable figure is `qty - committedQty`.**

The conventional reading, and what this plan is written against. BCPL have
indicated `committedQty` may itself be the net figure.

Confirmation: read both values for a SKU, place one order for one unit, read
again. If `committedQty` rises, A1 holds. If it falls, the sellable figure is
`committedQty` itself.

Blast radius: one expression in `vinculum/mapper/inventory.go`. Nothing else
changes. Getting it wrong is expensive in production — systematic oversell,
or a catalogue that reads as out of stock — so it must be observed, not
assumed.

**A2 — Vinculum cannot call a URL we host.**

The published Swagger contains no callback registration endpoint; every
documented operation is inbound. The plan therefore pulls on a schedule.

If BCPL confirm push is available, add a receiving endpoint and keep the
scheduled pull as a backstop. A webhook dropped during a deployment leaves an
order with no tracking forever; a scheduled sweep recovers by itself. Push
improves latency, it does not remove the sweep.

Blast radius: additive. Workflows and mappers unchanged.

---

## 8. Open items

Blocking, from BCPL:

1. Does `committedQty` rise or fall when an order is placed? (A1) — blocks Phase 4
2. Which `bucket` value is sellable stock? — blocks Phase 4
3. The `orderLocation` code for BCPL's warehouse — blocks Phase 5
4. Can Vinculum call a URL we host, with what authentication and retry
   behaviour? (A2) — does not block; changes nothing if the answer is no
5. Test-environment `ApiOwner`/`ApiKey` and seeded SKUs — blocks live testing
   of Phases 1 and 5

Blocking, from EasyEcom:

6. A separate location for BCPL and its `location_key` — blocks Phase 3
7. The `current_shipment_status_id` enum — blocks Phase 6
8. BCPL's couriers registered, to obtain their `companyCarrierId` values —
   blocks Phase 6
9. Rate limits on the bulk inventory endpoint — tuning only

Not blocking: credentials, base URLs, poll intervals, safety buffers. All
configuration; placeholders are fine.

Business decision, not technical:

**Mixed carts.** An order containing both a Gluzo SKU and a BCPL SKU becomes
two shipments, two tracking numbers and two invoices under two GST
registrations — `shipmentDetail` returns `sellerGstNo`, confirming BCPL
invoices under their own entity. Either block mixed carts at checkout for v1,
or model split orders through EasyEcom, the gateway and WooCommerce. Blocking
is substantially cheaper and is the recommendation for v1.

Deferred by agreement: backorders, checkout-time stock pre-check, and a
customer-facing path for a stock rejection. BCPL maintain buffer stock.

---

## 9. Constraints carried over

From `docs/architecture.md`, unchanged:

- Vinculum DTOs never leave `app/integrations/vinculum`.
- Mappers stay pure: no HTTP, no database, no authentication.
- No warehouse, facility or location identifier is hardcoded; all of it is
  route configuration.
- Webhook handlers never call Vinculum synchronously.
- Nothing logs a credential, at any level.
- The execution log is append-only; workflow state is mutable.
- No API path or field is invented. Section 6 is sourced from the live
  specification; anything added later that cannot be confirmed carries a
  `TODO(VERIFY)` marker and is listed in `docs/integrations.md`.
