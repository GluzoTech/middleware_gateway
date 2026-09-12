package easyecom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/domain/inventory"
	"github.com/gluzo/integration-gateway/app/domain/order"
	"github.com/gluzo/integration-gateway/app/domain/tracking"
	"github.com/gluzo/integration-gateway/app/event"
	dtoinventory "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/inventory"
	dtoorder "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/order"
	dtotracking "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/tracking"
	"github.com/gluzo/integration-gateway/app/integrations/easyecom/mapper"
)

// Source adapts the EasyEcom client to the order workflow's Source contract.
//
// FetchOrder prefers the Get Order Details API so the workflow works from
// EasyEcom's current view of the order. When the API cannot identify the
// order (no reference, or a permanent not-found) the webhook payload, which
// carries the same order representation, is used instead; transient API
// failures propagate so the engine retries them.
type Source struct {
	client *Client
	logger *slog.Logger
}

// NewSource wraps client.
func NewSource(client *Client, logger *slog.Logger) *Source {
	if logger == nil {
		logger = slog.Default()
	}
	return &Source{client: client, logger: logger}
}

// Platform implements ordersync.Source.
func (s *Source) Platform() string { return PlatformName }

// FetchOrder implements ordersync.Source.
func (s *Source) FetchOrder(ctx context.Context, ev event.Event) (order.Order, error) {
	req := dtoorder.GetOrderDetailsRequest{InvoiceID: ev.InvoiceNumber, ReferenceCode: ev.ReferenceCode}
	if req.Validate() != nil {
		s.logger.InfoContext(ctx, "easyecom event carries no lookup identifier; using webhook payload",
			slog.String("external_order_id", ev.ExternalOrderID), slog.String("correlation_id", ev.CorrelationID))
		return orderFromPayload(ev)
	}

	resp, err := s.client.GetOrderDetails(ctx, req)
	if err != nil {
		if apperror.IsRetryable(err) {
			return order.Order{}, err
		}
		s.logger.WarnContext(ctx, "easyecom order lookup failed permanently; using webhook payload",
			slog.String("external_order_id", ev.ExternalOrderID), slog.String("correlation_id", ev.CorrelationID),
			slog.String("error", err.Error()))
		return orderFromPayload(ev)
	}

	dto, found := pickOrder(resp.Data.Orders, ev.ExternalOrderID)
	if !found {
		s.logger.WarnContext(ctx, "easyecom order lookup returned no matching order; using webhook payload",
			slog.String("external_order_id", ev.ExternalOrderID), slog.String("correlation_id", ev.CorrelationID))
		return orderFromPayload(ev)
	}
	return mapper.ToDomainOrder(dto)
}

// pickOrder selects the order matching externalID, or the only order when
// the response carries exactly one.
func pickOrder(orders []dtoorder.Order, externalID string) (dtoorder.Order, bool) {
	for _, o := range orders {
		if strings.TrimSpace(o.OrderID.String()) == externalID || strings.TrimSpace(o.InvoiceID.String()) == externalID {
			return o, true
		}
	}
	if len(orders) == 1 {
		return orders[0], true
	}
	return dtoorder.Order{}, false
}

func orderFromPayload(ev event.Event) (order.Order, error) {
	if len(ev.Payload) == 0 {
		return order.Order{}, apperror.New(apperror.Validation, "event carries no order payload")
	}
	var dto dtoorder.Order
	if err := json.Unmarshal(ev.Payload, &dto); err != nil {
		return order.Order{}, apperror.Wrap(apperror.Validation, "event payload is not an EasyEcom order", err)
	}
	return mapper.ToDomainOrder(dto)
}

// FetchInventory implements ordersync.Source. Each distinct SKU is queried;
// a partial result is returned when some lookups fail and everything failed
// is reported as an error.
func (s *Source) FetchInventory(ctx context.Context, o order.Order) ([]inventory.Level, error) {
	seen := make(map[string]bool)
	var levels []inventory.Level
	var failures []error
	for _, item := range o.Items {
		sku := strings.TrimSpace(item.SKU)
		if sku == "" || seen[sku] {
			continue
		}
		seen[sku] = true

		resp, err := s.client.GetInventoryDetails(ctx, dtoinventory.GetInventoryDetailsRequest{SKU: sku, WarehouseID: o.WarehouseID})
		if err != nil {
			failures = append(failures, fmt.Errorf("sku %s: %w", sku, err))
			continue
		}
		mapped, skipped := mapper.ToDomainInventory(resp.Data)
		if skipped > 0 {
			s.logger.WarnContext(ctx, "easyecom inventory rows skipped", slog.String("sku", sku), slog.Int("skipped", skipped))
		}
		levels = append(levels, mapped...)
	}
	if len(levels) == 0 && len(failures) > 0 {
		return nil, failures[0]
	}
	if len(failures) > 0 {
		s.logger.WarnContext(ctx, "easyecom inventory partially fetched", slog.Int("failed_skus", len(failures)), slog.String("first_error", errors.Join(failures...).Error()))
	}
	return levels, nil
}

// FetchTracking implements ordersync.Source. A permanent not-found means no
// shipment exists yet and yields nil, nil.
func (s *Source) FetchTracking(ctx context.Context, o order.Order) (*tracking.Shipment, error) {
	req := dtotracking.GetTrackingDetailsRequest{InvoiceID: o.InvoiceNumber, ReferenceCode: o.ReferenceCode}
	if req.Validate() != nil {
		return nil, nil
	}
	resp, err := s.client.GetTrackingDetails(ctx, req)
	if err != nil {
		if apperror.IsRetryable(err) {
			return nil, err
		}
		var aerr *apperror.Error
		if errors.As(err, &aerr) && (aerr.HTTPStatus == 404 || aerr.ExternalCode == "404") {
			return nil, nil
		}
		return nil, err
	}
	for _, detail := range resp.Data {
		shipment, err := mapper.ToDomainShipment(detail)
		if err != nil {
			s.logger.WarnContext(ctx, "easyecom tracking row skipped", slog.String("error", err.Error()))
			continue
		}
		if shipment.OrderExternalID == o.ExternalID || shipment.OrderExternalID == o.InvoiceNumber || len(resp.Data) == 1 {
			return &shipment, nil
		}
	}
	return nil, nil
}
