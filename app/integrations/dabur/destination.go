package dabur

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
	dtoorder "github.com/gluzo/integration-gateway/app/integrations/dabur/dto/order"
	"github.com/gluzo/integration-gateway/app/integrations/dabur/mapper"
	"github.com/gluzo/integration-gateway/app/workflow"
	"github.com/gluzo/integration-gateway/app/workflow/ordersync"
)

// Destination adapts the Uniware client to the order workflow's
// Destination contract.
type Destination struct {
	client *Client
	logger *slog.Logger
}

// NewDestination wraps client.
func NewDestination(client *Client, logger *slog.Logger) *Destination {
	if logger == nil {
		logger = slog.Default()
	}
	return &Destination{client: client, logger: logger}
}

// Platform implements ordersync.Destination.
func (d *Destination) Platform() string { return PlatformName }

// facility picks the Uniware facility for a route: the route's destination
// reference, else the configured default.
func (d *Destination) facility(route workflow.RouteInfo) (string, error) {
	if f := strings.TrimSpace(route.DestinationReference); f != "" {
		return f, nil
	}
	if f := strings.TrimSpace(d.client.cfg.DefaultFacility); f != "" {
		return f, nil
	}
	e := apperror.New(apperror.Mapping, fmt.Sprintf("no Uniware facility for route %s=%s: set the route's destination reference or DABUR_DEFAULT_FACILITY", route.RouteType, route.RouteValue))
	e.Integration = PlatformName
	return "", e
}

// PrepareOrder implements ordersync.Destination.
func (d *Destination) PrepareOrder(_ context.Context, o order.Order, route workflow.RouteInfo) (json.RawMessage, error) {
	facility, err := d.facility(route)
	if err != nil {
		return nil, err
	}
	req, err := mapper.ToCreateSaleOrderRequest(o, mapper.OrderOptions{
		FacilityCode:         facility,
		Channel:              d.client.cfg.Channel,
		VerificationRequired: d.client.cfg.VerificationRequired,
	})
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, apperror.Wrap(apperror.Mapping, "encode sale order", err)
	}
	return data, nil
}

// SubmitOrder implements ordersync.Destination. Creation is idempotent: if
// Uniware reports that the code already exists, the existing order is
// looked up and reported with Created=false.
func (d *Destination) SubmitOrder(ctx context.Context, prepared json.RawMessage, o order.Order, route workflow.RouteInfo) (ordersync.OrderResult, error) {
	facility, err := d.facility(route)
	if err != nil {
		return ordersync.OrderResult{}, err
	}
	code := strings.TrimSpace(o.ExternalID)

	resp, err := d.client.CreateSaleOrder(ctx, facility, prepared)
	if err == nil {
		id := code
		if resp != nil && resp.SaleOrderDetailDTO != nil && strings.TrimSpace(resp.SaleOrderDetailDTO.Code) != "" {
			id = strings.TrimSpace(resp.SaleOrderDetailDTO.Code)
		}
		return ordersync.OrderResult{DestinationOrderID: id, Created: true}, nil
	}
	if !isAlreadyExists(err) {
		return ordersync.OrderResult{}, err
	}

	d.logger.InfoContext(ctx, "uniware reports the sale order already exists; confirming",
		slog.String("sale_order_code", code), slog.String("facility", facility))
	existing, getErr := d.client.GetSaleOrder(ctx, dtoorder.GetSaleOrderRequest{Code: code})
	if getErr != nil {
		return ordersync.OrderResult{}, getErr
	}
	if existing.SaleOrderDTO == nil || strings.TrimSpace(existing.SaleOrderDTO.Code) == "" {
		return ordersync.OrderResult{}, err
	}
	return ordersync.OrderResult{DestinationOrderID: strings.TrimSpace(existing.SaleOrderDTO.Code), Created: false}, nil
}

// isAlreadyExists recognises Uniware's duplicate-code rejection.
// VERIFY with Dabur: the exact error code Uniware returns for a duplicate
// sale order; the message text match is the documented behaviour observed
// in practice.
func isAlreadyExists(err error) bool {
	var aerr *apperror.Error
	if !errors.As(err, &aerr) {
		return false
	}
	msg := strings.ToLower(aerr.ExternalMessage)
	return strings.Contains(msg, "already exist") || strings.Contains(msg, "duplicate")
}

// UpdateInventory implements ordersync.Destination.
func (d *Destination) UpdateInventory(ctx context.Context, levels []inventory.Level, route workflow.RouteInfo) (ordersync.InventoryResult, error) {
	facility, err := d.facility(route)
	if err != nil {
		return ordersync.InventoryResult{}, err
	}
	req := mapper.ToInventoryAdjustments(levels, mapper.InventoryOptions{
		FacilityCode: facility,
		ShelfCode:    d.client.cfg.ShelfCode,
		Remarks:      "gluzo integration gateway sync",
	})
	if len(req.InventoryAdjustments) == 0 {
		return ordersync.InventoryResult{}, nil
	}

	resp, err := d.client.AdjustInventoryBulk(ctx, facility, req)
	if err != nil && resp == nil {
		return ordersync.InventoryResult{}, err
	}
	var result ordersync.InventoryResult
	for _, r := range resp.InventoryAdjustmentResponses {
		if r.Successful {
			result.Updated++
		} else {
			result.Failed++
		}
	}
	if err != nil && result.Updated == 0 {
		return result, err
	}
	if result.Failed > 0 {
		d.logger.WarnContext(ctx, "uniware rejected some inventory adjustments",
			slog.Int("updated", result.Updated), slog.Int("failed", result.Failed), slog.String("facility", facility))
	}
	return result, nil
}
