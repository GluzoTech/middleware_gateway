package dabur

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/httpclient"
	dtoorder "github.com/gluzo/integration-gateway/app/integrations/dabur/dto/order"
)

// Endpoint paths from the Uniware documentation.
const (
	createSaleOrderPath = "/services/rest/v1/oms/saleOrder/create"
	getSaleOrderPath    = "/services/rest/v1/oms/saleorder/get"
)

// CreateSaleOrder creates an order in facility. body may be a
// CreateSaleOrderRequest or its already-encoded JSON.
func (c *Client) CreateSaleOrder(ctx context.Context, facility string, body json.RawMessage) (*dtoorder.CreateSaleOrderResponse, error) {
	const op = "CreateSaleOrder"
	if strings.TrimSpace(facility) == "" {
		e := apperror.New(apperror.Validation, "facility code is required")
		e.Integration, e.Operation = PlatformName, op
		return nil, e
	}
	if len(body) == 0 {
		e := apperror.New(apperror.Validation, "request body is required")
		e.Integration, e.Operation = PlatformName, op
		return nil, e
	}

	var resp dtoorder.CreateSaleOrderResponse
	if err := c.call(ctx, httpclient.Request{
		Method:    http.MethodPost,
		Path:      createSaleOrderPath,
		RawBody:   body,
		Operation: op,
	}, facility, &resp); err != nil {
		return nil, err
	}
	if err := checkResponse(op, resp.Response); err != nil {
		return &resp, err
	}
	return &resp, nil
}

// GetSaleOrder fetches an existing order by code.
func (c *Client) GetSaleOrder(ctx context.Context, req dtoorder.GetSaleOrderRequest) (*dtoorder.GetSaleOrderResponse, error) {
	const op = "GetSaleOrder"
	if strings.TrimSpace(req.Code) == "" {
		e := apperror.New(apperror.Validation, "sale order code is required")
		e.Integration, e.Operation = PlatformName, op
		return nil, e
	}

	var resp dtoorder.GetSaleOrderResponse
	if err := c.call(ctx, httpclient.Request{
		Method:    http.MethodPost,
		Path:      getSaleOrderPath,
		Body:      req,
		Operation: op,
	}, "", &resp); err != nil {
		return nil, err
	}
	if err := checkResponse(op, resp.Response); err != nil {
		return &resp, err
	}
	return &resp, nil
}
