package easyecom

import (
	"context"
	"net/http"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/httpclient"
	dtoinventory "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/inventory"
)

// inventoryDetailsPath is the Get Inventory Details V2 endpoint.
// TODO(VERIFY): confirm the path, parameters and response fields against
// api-docs.easyecom.io before enabling inventory synchronisation.
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
