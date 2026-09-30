# Integrations

Each external platform lives in its own package under `app/integrations/`
with the same shape:

```text
<platform>/
├── config.go        credentials and tuning
├── auth.go          how outbound calls authenticate
├── client.go        transport, headers, envelope handling
├── endpoint_*.go    one method per API operation
├── dto/             one request/response type per operation
└── mapper/          DTO <-> domain, pure functions
```

DTOs never leave their package tree. The workflow engine only ever sees
domain models.

## EasyEcom

### Outbound authentication

Every API call carries two headers, both mandatory since EasyEcom's July 2023
API change:

| Header | Value |
| --- | --- |
| `X-API-Key` | Account API key from EasyEcom settings (`EASYECOM_API_KEY`) |
| `Authorization` | `Bearer <JWT>` |

The JWT (valid 90 days per EasyEcom) comes from one of two sources:

- `EASYECOM_JWT_TOKEN`: a pre-issued token, used as is. Rotation is manual.
- Login: `EASYECOM_EMAIL`, `EASYECOM_PASSWORD` and `EASYECOM_LOCATION_KEY` are
  posted to `POST /access/token`; the returned `data.token.jwt_token` is
  cached for 24 hours or until five minutes before its `exp` claim, whichever
  is sooner. A 401 from any endpoint invalidates the cache and the call is
  repeated once with a fresh login.

### Endpoints

| Operation | Method and path | Contract status |
| --- | --- | --- |
| `AccessToken` | `POST /access/token` | Verify: follows the public documentation as last observed |
| `GetOrderDetails` | `GET /orders/V2/getOrderDetails?invoice_id=` or `?reference_code=` | Verify: field names match EasyEcom's webhook documentation; path per public docs |
| `GetInventoryDetails` | `GET /getInventoryDetailsV2?sku=` | **Unverified**: endpoint name confirmed by EasyEcom release notes, parameters and response fields are not |
| `GetTrackingDetails` | `GET /Carriers/getTrackingDetails` | **Unverified**: path, parameters and response fields must be confirmed |

`https://api-docs.easyecom.io` is a browser-rendered application and could
not be read programmatically while this adapter was written. Every field or
path that could not be cross-checked is marked `VERIFY` or `TODO(VERIFY)` in
code. Confirm each against the live documentation before enabling the
corresponding workflow action in production. The client, retry, envelope and
mapping logic do not change when a field name is corrected.

### Response handling

EasyEcom wraps responses in `{code, message, data}`. A 2xx HTTP status with
a non-success `code` in the body is treated as an application error with the
same category and retry decision that the equivalent HTTP status would get.

Identifiers and amounts arrive as strings in some payloads and as numbers in
others; the `dto.Flex*` types accept both. Order collections arrive as a bare
array (V2), as `{"orders": [...], "nextUrl": ...}` (V1) or as a single order
object, and all three decode into the same `OrdersData`.

### Webhooks

Facts from EasyEcom's support documentation ("Webhook configuration",
"Outbound Webhooks - Handling Invalid Responses"):

- Twelve triggers exist, including Create Order, Confirm Order, Cancel Order,
  Update Inventory, Tracking, RTD and Manifested. Each is configured with its
  own URL and a V1/V2 payload version.
- V2 order payloads are a bare array of order objects with the fields
  `invoice_id`, `order_id`, `reference_code`, `company_name`, `warehouse_id`,
  `order_status`, `order_status_id`, `customer_name`, `contact_num`,
  `address_line_1`, `city`, `state`, `pin_code`, `total_amount`,
  `payment_mode` and `order_items` (or `suborders`) with `suborder_id`, `sku`,
  `product_id`, `suborder_quantity`, `tax_rate`, `selling_price`,
  `shipment_type` and `tax_type`.
- EasyEcom authenticates its calls with a single `Access-Token` header
  carrying the webhook token configured in EasyEcom.
- Any 4xx or 5xx response counts as a failed delivery. Failures are retried
  with growing delays, and after 400 failed requests in 24 hours EasyEcom
  deactivates the trigger.

Consequences for the gateway: respond 2xx for every well-formed,
authenticated event, including duplicates and events for warehouses that are
not routed, and reserve 4xx for malformed or unauthenticated requests. The
two-tier authentication in [authentication.md](authentication.md) needs two
headers; because EasyEcom sends one, the webhook handler accepts
`Access-Token: <platform key>:<integration token>` as an equivalent carrier
of both credentials (see ADR-009 in [architecture.md](architecture.md)).

### Mapping rules

- `order_id` becomes the domain `ExternalID`, falling back to `invoice_id`.
- `warehouse_id` becomes `WarehouseID`, the routing key.
- Timestamps without a zone are interpreted as IST.
- `payment_mode` and `order_status` are normalised by keyword; the original
  label is kept in `SourceStatus`.
- EasyEcom's order payload carries one address, used for both shipping and
  billing.
- Line total is `selling_price * suborder_quantity`.
- Currency is assumed to be INR; the payload does not carry it.

### Origin adapter

`easyecom.Source` implements `vendor.Origin`. `FetchOrder` prefers the Get
Order Details API and falls back to the webhook payload (which carries the
same order representation) when the API cannot identify the order; transient
API failures propagate so the engine retries them.

It still carries `FetchInventory` and `FetchTracking` from the removed
source/destination contracts. Neither is called by any workflow now: under dropship
the vendor owns stock and dispatch, so both flow *into* EasyEcom through the
`StockSink` and `ShipmentSink` roles instead. Phases 3 and 6 of the
[Vinculum plan](vinculum-integration-plan.md) decide whether they are
reworked into the sink adapter or deleted.

## Vendor side

No adapter implements the vendor roles yet. The previous Uniware pipeline was
removed in Phase 0 of the [Vinculum plan](vinculum-integration-plan.md);
Vinculum eRetail's client and read paths arrived in Phase 1. See
[phases/](phases/).

### Roles, not a source/destination pair

`app/vendor` defines six role interfaces. The direction of data is fixed by
the role, so an adapter cannot carry a method name that lies about which way
data moves.

| Side | Role | Methods | Meaning |
| --- | --- | --- | --- |
| Vendor | `OrderReceiver` | `PrepareOrder`, `SubmitOrder` | accepts orders for fulfilment |
| Vendor | `StockProvider` | `FetchStock` | owns stock for its SKUs; the gateway reads |
| Vendor | `FulfilmentProvider` | `FetchShipments` | ships, so owns AWB, invoice, delivery status |
| Origin | `Origin` | `FetchOrder` | customer-facing order record |
| Origin | `StockSink` | `PushStock` | receives vendor stock as authoritative |
| Origin | `ShipmentSink` | `PushShipment` | receives externally-booked dispatch |

`vendor.Registry` indexes adapters by platform name and discovers capability
by type assertion at registration, so adding a partner is one `Register`
call and a partner may implement any subset of the roles. An adapter
implementing none of them is rejected at start-up.

### Contract rules every adapter must honour

- `PrepareOrder` is pure. No network, no database. Its failures are
  permanent and are never retried.
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

### Route references

A route carries two location references and the distinction matters:

| Field | Side | For Vinculum / EasyEcom |
| --- | --- | --- |
| `VendorReference` | vendor | the three-character `orderLocation` |
| `OriginReference` | origin | the `location_key` scoping stock writes |

Both are configuration; neither appears in code. `OriginReference` has no
routing column yet — Phase 3 adds it.

## Vinculum (eRetail)

BCPL runs Vinculum eRetail. The adapter follows the live specification read on
29 September 2026; what is tenant-specific is configuration. Phase 1 built the
client, the two read endpoints and their mappers. The vendor roles themselves
are Phases 4 and 5.

### Authentication

Two static headers on every call:

| Header | Value |
| --- | --- |
| `ApiOwner` | `VINCULUM_API_OWNER` |
| `ApiKey` | `VINCULUM_API_KEY` |

No token exchange, no refresh, no expiry. The client therefore has no token
source and no retry-on-401 loop: the credentials are static, so a 401 means
they are wrong and asking again would only be told the same thing.

### Endpoints

| Operation | Method and path | Contract status |
| --- | --- | --- |
| `GetWhInventory` | `POST /RestWS/api/eretail/v4/stock/getWhInventory` | Field names per the published specification |
| `ShipmentDetail` | `POST /RestWS/api/eretail/v1/order/shipmentDetail` | Field names per the published specification |

Both page. `hasMore` drives the stock sweep, `pageNumber` the shipment sweep,
and each `FetchAll*` helper stops at a page ceiling rather than trusting a
`hasMore` that never goes false. A stock sweep that ends early is reported as
an error, not returned short: half a catalogue, taken as the whole, would
zero out the other half at the storefront.

### Response handling

Vinculum wraps responses in `responseCode`/`responseMessage`. A business
rejection arrives as HTTP 200 with a non-zero `responseCode`, so the envelope
is checked separately from the status; a caller that checked only the status
would read a rejection as a successful empty page. Envelope errors are
**non-retryable** — they describe a decision about the request. Transient
conditions arrive as 5xx and are retried by the HTTP client.

Numbers arrive as JSON numbers in some fields and as quoted strings in
others, so the `dto.Flex*` types accept both.

### Unverified, and why

| Item | Status |
| --- | --- |
| `responseCode` success value | `0` assumed. The specification names the field without enumerating values. Failing safe: a wrong assumption reports every call as an error rather than swallowing failures. |
| Request date format | `2006-01-02 15:04:05`. The specification documents the date parameters without their format. One constant, `dto.RequestTimeLayout`. |
| Response date formats | Several layouts are tried in turn, because the format of a given field is not stated and refusing a shipment over a date format loses tracking the customer is waiting for. |
| Dispatch status values | Normalised by keyword. An unrecognised label becomes `UNKNOWN` and keeps its original text in `SourceStatus`, so nothing is silently reclassified. |
| `reqType`, `filterBy`, `fulfillmentLocation`, `status[]` | Documented parameters with no published value set. Passed through when set, omitted when not, so a value BCPL supply later needs no code change. |
| Sellable `bucket` value | BCPL open item 2. Blank accepts every bucket, which is right for reading and wrong for pushing. Phase 4 requires it. |
| `qty` versus `committedQty` | Assumption A1. See below. |

Test payloads under `app/integrations/vinculum/mapper/testdata/` are built
from the specification, not captured from BCPL: no test credentials have been
issued (open item 5).

### The sellable quantity, and why it is one expression

`mapper.SellableQuantity` is assumption A1 of the plan: sellable is `qty`
minus `committedQty`. BCPL have indicated `committedQty` may itself be the net
figure, in which case the function becomes `return committed`.

It is deliberately one expression called from one place. Getting it wrong is
expensive in production — systematic oversell if too high, a catalogue reading
as out of stock if too low — so it must be observed rather than assumed. The
observation: read both values for a SKU, place one order for one unit, read
again. If `committedQty` rises, A1 holds.

### Confirm with BCPL before go-live

- Which `bucket` value is sellable stock.
- Whether `committedQty` rises or falls when an order is placed (A1).
- The `orderLocation` code for BCPL's warehouse.
- The date format their tenant expects, and the `responseCode` success value.
- Test-environment `ApiOwner`/`ApiKey` and seeded SKUs.
