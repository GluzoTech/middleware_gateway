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
