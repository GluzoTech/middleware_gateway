package vinculum

import (
	"context"
	"fmt"

	"github.com/gluzo/integration-gateway/app/apperror"
	dtoshipment "github.com/gluzo/integration-gateway/app/integrations/vinculum/dto/shipment"
)

// shipmentDetailPath is the dispatch endpoint.
const shipmentDetailPath = "/RestWS/api/eretail/v1/order/shipmentDetail"

// MaxShipmentPages bounds a paged sweep, for the same reason as
// MaxStockPages: hasMore is the vendor's claim, not ours.
const MaxShipmentPages = 200

// ShipmentDetail reads one page of dispatch records.
func (c *Client) ShipmentDetail(ctx context.Context, req dtoshipment.ShipmentDetailRequest) (*dtoshipment.ShipmentDetailResponse, error) {
	const op = "ShipmentDetail"
	if req.OrderLocation == "" {
		req.OrderLocation = c.Location()
	}
	if err := req.Validate(); err != nil {
		return nil, invalidRequest(op, err)
	}

	var resp dtoshipment.ShipmentDetailResponse
	if err := c.post(ctx, shipmentDetailPath, op, req.Body(), &resp); err != nil {
		return nil, err
	}
	if err := checkEnvelope(op, resp.Envelope); err != nil {
		return nil, err
	}
	return &resp, nil
}

// FetchAllShipmentDetail reads every page for a request.
//
// Unlike a stock sweep a partial shipment read is not dangerous — it means
// some tracking arrives on the next run rather than a catalogue reading as
// out of stock — but it is still reported, because a sweep that quietly
// stopped short would leave the watermark ahead of what was actually read.
func (c *Client) FetchAllShipmentDetail(ctx context.Context, req dtoshipment.ShipmentDetailRequest) ([]dtoshipment.OrderShipment, error) {
	var out []dtoshipment.OrderShipment
	page := req.PageNumber
	if page < 1 {
		page = 1
	}
	for n := 0; n < MaxShipmentPages; n++ {
		req.PageNumber = page
		resp, err := c.ShipmentDetail(ctx, req)
		if err != nil {
			return nil, err
		}
		out = append(out, resp.Response...)
		if !resp.HasMore.Bool() || len(resp.Response) == 0 {
			return out, nil
		}
		page++
	}
	return out, pageLimitExceeded("ShipmentDetail", MaxShipmentPages)
}

// pageLimitExceeded reports a sweep that hit its page ceiling. It is
// retryable: the next scheduled run may find the vendor behaving.
func pageLimitExceeded(operation string, limit int) error {
	e := &apperror.Error{
		Category:    apperror.ExternalAPI,
		Message:     fmt.Sprintf("vinculum reported more pages than the %d-page limit allows", limit),
		Retryable:   true,
		Integration: PlatformName,
		Operation:   operation,
	}
	return e
}
