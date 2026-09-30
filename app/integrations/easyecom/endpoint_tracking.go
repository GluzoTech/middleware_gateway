package easyecom

import (
	"context"
	"net/http"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/httpclient"
	dtotracking "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/tracking"
)

// trackingDetailsPath is the shipment tracking endpoint.
//
// TODO(VERIFY): still unconfirmed, and now unused. Reviewed against the
// EasyEcom Postman collection on 29 September 2026: the collection documents
// the shipment *write* endpoints (AssignShipmentDetails, updateTrackingStatus),
// not this read. Under dropship EasyEcom holds no tracking until the gateway
// pushes what BCPL booked, so reading it back would return nothing. Phase 6
// of the Vinculum plan decides whether it is reworked into the sink adapter
// or deleted.
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

// Shipment write endpoints.
//
// TODO(VERIFY): the EasyEcom Postman collection read on 29 September 2026
// records both operations and their request bodies but not their paths. The
// paths below follow the naming of the endpoints already in this package and
// are NOT confirmed; they are listed in docs/blockers.md. A wrong path fails
// loudly on the first call, so it cannot corrupt data, but it stops every
// dispatch push until corrected.
const (
	assignShipmentDetailsPath = "/AssignShipmentDetails"
	updateTrackingStatusPath  = "/updateTrackingStatus"
)

// AssignShipmentDetails records an externally booked dispatch against an
// order.
//
// locationKey selects the EasyEcom location the write authenticates for, for
// the same reason a stock push does: the JWT is scoped to one location, so a
// dispatch cannot be recorded against an order in a location the route does
// not name.
func (c *Client) AssignShipmentDetails(ctx context.Context, locationKey string, req dtotracking.AssignShipmentDetailsRequest) (*dtotracking.AssignShipmentDetailsResponse, error) {
	const op = "AssignShipmentDetails"
	if err := req.Validate(); err != nil {
		e := apperror.Wrap(apperror.Validation, "invalid request", err)
		e.Integration, e.Operation = PlatformName, op
		return nil, e
	}

	var resp dtotracking.AssignShipmentDetailsResponse
	if err := c.callAs(ctx, locationKey, httpclient.Request{
		Method:    http.MethodPost,
		Path:      assignShipmentDetailsPath,
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

// UpdateTrackingStatus moves a shipment's delivery status on.
func (c *Client) UpdateTrackingStatus(ctx context.Context, locationKey string, req dtotracking.UpdateTrackingStatusRequest) (*dtotracking.UpdateTrackingStatusResponse, error) {
	const op = "UpdateTrackingStatus"
	if err := req.Validate(); err != nil {
		e := apperror.Wrap(apperror.Validation, "invalid request", err)
		e.Integration, e.Operation = PlatformName, op
		return nil, e
	}

	var resp dtotracking.UpdateTrackingStatusResponse
	if err := c.callAs(ctx, locationKey, httpclient.Request{
		Method:    http.MethodPost,
		Path:      updateTrackingStatusPath,
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
