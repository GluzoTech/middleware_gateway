package easyecom

import (
	"context"
	"net/http"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/httpclient"
	dtoinventory "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/inventory"
)

// inventoryDetailsPath is the Get Inventory Details V2 endpoint.
//
// TODO(VERIFY): still unconfirmed, and now unused. Reviewed against the
// EasyEcom Postman collection on 29 September 2026: the collection documents
// the inventory *write* endpoints (Update Inventory, Bulk Inventory Update),
// not this read. Under dropship the gateway does not read EasyEcom stock at
// all — the vendor owns it — so no workflow calls this. Phase 3 of the
// Vinculum plan decides whether it is reworked into the sink adapter or
// deleted; do not enable it in the meantime.
const inventoryDetailsPath = "/getInventoryDetailsV2"

// GetInventoryDetails fetches the stock position of one SKU.
func (c *Client) GetInventoryDetails(ctx context.Context, req dtoinventory.GetInventoryDetailsRequest) (*dtoinventory.GetInventoryDetailsResponse, error) {
	const op = "GetInventoryDetails"
	if err := req.Validate(); err != nil {
		e := apperror.Wrap(apperror.Validation, "invalid request", err)
		e.Integration, e.Operation = PlatformName, op
		return nil, e
	}

	var resp dtoinventory.GetInventoryDetailsResponse
	if err := c.call(ctx, httpclient.Request{
		Method:    http.MethodGet,
		Path:      inventoryDetailsPath,
		Query:     req.Query(),
		Operation: op,
	}, &resp); err != nil {
		return nil, err
	}
	if err := checkEnvelope(op, resp.Code, resp.Message); err != nil {
		return nil, err
	}
	return &resp, nil
}

// bulkInventoryUpdatePath is the bulk stock write endpoint.
//
// TODO(VERIFY): the EasyEcom Postman collection read on 29 September 2026
// records the operation and its request body — {"skus":[{"sku","quantity"}]}
// — but not its path. The path below follows the naming of the endpoints
// already in this package and is NOT confirmed. It is listed in
// docs/blockers.md and must be checked before Phase 4 pushes anything: a
// wrong path fails loudly on the first call, so it cannot corrupt data, but
// it will stop every push until corrected.
const bulkInventoryUpdatePath = "/bulkInventoryUpdate"

// BulkInventoryUpdate replaces the stored quantity of several SKUs.
//
// locationKey selects the EasyEcom location the write is authenticated for.
// This is the boundary that keeps vendor stock inside the vendor's own
// warehouse: the JWT is scoped to one location, so a push for BCPL cannot
// write Gluzo's quantities even if the SKUs were wrong.
func (c *Client) BulkInventoryUpdate(ctx context.Context, locationKey string, req dtoinventory.BulkInventoryUpdateRequest) (*dtoinventory.BulkInventoryUpdateResponse, error) {
	const op = "BulkInventoryUpdate"
	if err := req.Validate(); err != nil {
		e := apperror.Wrap(apperror.Validation, "invalid request", err)
		e.Integration, e.Operation = PlatformName, op
		return nil, e
	}

	var resp dtoinventory.BulkInventoryUpdateResponse
	if err := c.callAs(ctx, locationKey, httpclient.Request{
		Method:    http.MethodPost,
		Path:      bulkInventoryUpdatePath,
		Body:      req,
		Operation: op,
	}, &resp); err != nil {
		return nil, err
	}
	if err := checkEnvelope(op, resp.Code, resp.Message); err != nil {
		return nil, err
	}
	return &resp, nil
}
