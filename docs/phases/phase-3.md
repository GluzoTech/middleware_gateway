# Phase 3 — SKU mapping and EasyEcom as a stock sink

Part of [EasyEcom × Vinculum](../vinculum-integration-plan.md). 30 September 2026.

**Goal:** the gateway can translate between Gluzo's SKUs and BCPL's item
codes, and write stock into BCPL's own EasyEcom location — without being able
to write anyone else's.

---

## What changed

```text
app/skumap/                       mapping, both directions, safety buffer
  0006_sku_map.sql                integration-scoped, active flag
app/integrations/easyecom/
  sink.go                         vendor.StockSink
  auth.go                         token cache keyed by location
  endpoint_inventory.go           + BulkInventoryUpdate
  dto/inventory/                  + bulk_inventory_update.go
0005_..._origin_reference.sql     routes gain an origin-side reference
cmd/gatewayctl/                   skumap add/list/set-status/remove
```

Nothing calls the sink yet. `STOCK_SYNC` is Phase 4.

---

## The location boundary is the point of this phase

EasyEcom issues a JWT scoped to **one location**. Authenticating for BCPL's
location makes the gateway *structurally incapable* of writing quantities
into Gluzo's own warehouse: a defect in the SKU set does not cause a
cross-warehouse write, because the platform rejects it.

That guarantee only holds if the token is per location, so:

- `TokenSource.Token` and `Invalidate` take a location key. It cannot be
  client state — one process writes to several locations and must never
  confuse them.
- The cache is a map keyed by location. A single cached token for the process
  would hand BCPL's credential to a write meant for Gluzo's warehouse, which
  is the exact failure the design exists to prevent.
- `Invalidate` discards one location's token. A 401 for one location says
  nothing about another's.

The exit-criterion test makes the fake EasyEcom behave the way the real one
does: each SKU belongs to a location, each issued JWT is bound to a location,
and **the token's scope decides**, not the request body. A push on BCPL's
route carrying a Gluzo SKU is rejected by the fake exactly as the platform
would reject it.

A route with no origin reference falls back to the process default **and logs
a warning**. That fallback is right for a single-location deployment and wrong
for this pipeline, so it is visible rather than silent.

---

## SKU mapping

Scoped to an integration, so two vendors may both use the item code
`HONEY-250` without colliding, and a mapping can never be used by a pipeline
it was not configured for.

**Unique in both directions**, because both are used: stock arrives under the
vendor's code and is published under Gluzo's; an order is placed under
Gluzo's and sent under the vendor's. A second row for either side would make
one direction ambiguous.

**An unmapped SKU is an error, never a skip.** Quietly dropping one during a
sweep publishes nothing for it, and a storefront showing no update looks
exactly like a storefront that is up to date. The error names the SKU,
because the operator's next action is to add exactly that mapping.

**A disabled mapping is different from an unmapped one.** Disabling is a
decision and simply leaves the SKU out of the index; unmapped is a
configuration error. Conflating them would turn an operator's deliberate pause
into a failing sweep every cycle.

**Lookups ignore case and surrounding space.** Vendors are inconsistent about
the case of item codes, and a mapping that failed because a code arrived
lower-cased would report a configuration error that is not one.

### The safety buffer, and the one rule it does not obey

`Mapping.Publishable` withholds the buffer from the published quantity — the
vendor's stock moves through channels the gateway cannot see, so the figure
read is always slightly stale.

**Zero propagates immediately, with no buffer applied.** An item the vendor
has none of is out of stock now; subtracting a buffer from nothing to reach
nothing would be the same answer reached more slowly. The result is also
clamped, so a buffer larger than the stock never turns "one left" negative.

---

## Route references, both ends

Phase 0's second follow-up, closed here. `integration_routes` gains
`origin_reference` alongside `destination_reference`, and it is plumbed
through `routing.Route`, `routing.Resolution`, `workflow.RouteInfo` and
`vendor.Route`.

The two are separate fields rather than one because they name different ends
of the same pipeline, and conflating them would make a stock push authenticate
for whichever one happened to be set.

`gatewayctl route add` takes `--origin-ref`, and `route list` shows both.

---

## Exit criteria

- [x] **Store tests against the embedded PostgreSQL**, following
      `tests/routing_store_test.go`: both-direction uniqueness, scoping to an
      integration, disable versus remove, and an index built from the store
      translating what the store holds.
- [x] **A push authenticated for location A cannot write location B.** Proved
      against a fake that scopes tokens the way EasyEcom does.
- [x] `gofmt -l .`, `go vet ./...`, `go test ./...` clean.

---

## Unverified, and honestly so

**The bulk endpoint's path is not confirmed.** The Postman collection read on
29 September 2026 records the operation and its request body —
`{"skus":[{"sku","quantity"}]}` — but not its path. `/bulkInventoryUpdate`
follows the naming of the endpoints already in this package and is a guess.

It is marked `TODO(VERIFY)` in code and added to
[blockers.md](../blockers.md). A wrong path fails loudly on the first call, so
it cannot corrupt data — but it will stop every push until corrected.

**The per-SKU result shape is not confirmed either.** An absent result list is
read as "the whole batch was accepted", which is the reading consistent with
the `{code, message}` envelope every other EasyEcom endpoint returns.

**The batch size of 500 is a conservative guess** (EasyEcom open item 9). One
constant.

---

## Follow-ups

1. **One EasyEcom account, several locations.** The design assumes the same
   email and password with a different `location_key` per location, which is
   what the documented login takes. If BCPL's location needs its own account,
   `Config` grows a per-location credential map; the token cache is already
   keyed correctly for it.
2. **`ListRoutes` and the admin viewer** do not show the origin reference in
   the timeline template. Cosmetic; belongs with Phase 7.
3. **No workflow uses any of this yet.** Phase 4 wires `MAP_STOCK` onto the
   SKU index and `PUSH_STOCK` onto the sink.

---

## Next

Phase 4 — `STOCK_SYNC`. It needs the sellable bucket (B2) and A1 settled
before it can go live, and **should refuse to start without a configured
bucket** rather than defaulting to "every bucket", per the recommendation in
[blockers.md](../blockers.md).
