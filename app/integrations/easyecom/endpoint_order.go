package easyecom

import (
	"context"
	"net/http"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/httpclient"
	dtoorder "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/order"
)

// orderDetailsPath is the Get Order Details (V2) endpoint.
// VERIFY against api-docs.easyecom.io (Orders > Get Order Details).
const orderDetailsPath = "/orders/V2/getOrderDetails"

// GetOrderDetails fetches an order by invoice ID or reference code.
func (c *Client) GetOrderDetails(ctx context.Context, req dtoorder.GetOrderDetailsRequest) (*dtoorder.GetOrderDetailsResponse, error) {
	const op = "GetOrderDetails"
	if err := req.Validate(); err != nil {
		e := apperror.Wrap(apperror.Validation, "invalid request", err)
		e.Integration, e.Operation = PlatformName, op
		return nil, e
	}

	var resp dtoorder.GetOrderDetailsResponse
	if err := c.call(ctx, httpclient.Request{
		Method:    http.MethodGet,
		Path:      orderDetailsPath,
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
