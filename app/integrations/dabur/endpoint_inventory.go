package dabur

import (
	"context"
	"net/http"
	"strings"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/httpclient"
	dtoinventory "github.com/gluzo/integration-gateway/app/integrations/dabur/dto/inventory"
)

// adjustInventoryBulkPath is the bulk inventory adjustment endpoint.
const adjustInventoryBulkPath = "/services/rest/v1/inventory/adjust/bulk"

// AdjustInventoryBulk applies several inventory adjustments in facility.
func (c *Client) AdjustInventoryBulk(ctx context.Context, facility string, req dtoinventory.AdjustInventoryBulkRequest) (*dtoinventory.AdjustInventoryBulkResponse, error) {
	const op = "AdjustInventoryBulk"
	if strings.TrimSpace(facility) == "" {
		e := apperror.New(apperror.Validation, "facility code is required")
		e.Integration, e.Operation = PlatformName, op
		return nil, e
	}
	if len(req.InventoryAdjustments) == 0 {
		e := apperror.New(apperror.Validation, "at least one adjustment is required")
		e.Integration, e.Operation = PlatformName, op
		return nil, e
	}

	var resp dtoinventory.AdjustInventoryBulkResponse
	if err := c.call(ctx, httpclient.Request{
		Method:    http.MethodPost,
		Path:      adjustInventoryBulkPath,
		Body:      req,
		Operation: op,
	}, facility, &resp); err != nil {
		return nil, err
	}
	if err := checkResponse(op, resp.Response); err != nil {
		return &resp, err
	}
	return &resp, nil
}
