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

## Dabur (Uniware)

Planned for phase 8 against Unicommerce's public Uniware documentation
(OAuth password grant on `/oauth/token`, `POST /services/rest/v1/oms/saleOrder/create`,
`POST /services/rest/v1/oms/saleorder/get`,
`POST /services/rest/v1/inventory/adjust/bulk`, with `Authorization: bearer`
and `Facility` headers). Dabur's tenant-specific configuration, facility
codes and any customisations must be confirmed with Dabur.
