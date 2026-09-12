package easyecom

import (
	"context"
	"net/http"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/httpclient"
	dtotracking "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/tracking"
)

// trackingDetailsPath is the shipment tracking endpoint.
// TODO(VERIFY): confirm the path, parameters and response fields against
// api-docs.easyecom.io before relying on tracking synchronisation.
const trackingDetailsPath = "/Carriers/getTrackingDetails"

// GetTrackingDetails fetches tracking information for a shipment.
func (c *Client) GetTrackingDetails(ctx context.Context, req dtotracking.GetTrackingDetailsRequest) (*dtotracking.GetTrackingDetailsResponse, error) {
	const op = "GetTrackingDetails"
	if err := req.Validate(); err != nil {
		e := apperror.Wrap(apperror.Validation, "invalid request", err)
		e.Integration, e.Operation = PlatformName, op
		return nil, e
	}

	var resp dtotracking.GetTrackingDetailsResponse
	if err := c.call(ctx, httpclient.Request{
		Method:    http.MethodGet,
		Path:      trackingDetailsPath,
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
