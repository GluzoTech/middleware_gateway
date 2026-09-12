package mapper

import (
	"fmt"
	"strings"

	"github.com/gluzo/integration-gateway/app/domain/tracking"
	dtotracking "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/tracking"
)

// ToDomainShipment converts an EasyEcom tracking row into the domain model.
func ToDomainShipment(src dtotracking.TrackingDetail) (tracking.Shipment, error) {
	orderID := strings.TrimSpace(src.OrderID.String())
	if orderID == "" {
		orderID = strings.TrimSpace(src.InvoiceID.String())
	}
	if orderID == "" {
		return tracking.Shipment{}, mappingError("tracking row has neither order_id nor invoice_id")
	}

	shippedAt, err := ParseTimestamp(src.ShippedAt)
	if err != nil {
		return tracking.Shipment{}, mappingError(fmt.Sprintf("tracking for order %s: %v", orderID, err))
	}
	updatedAt, err := ParseTimestamp(src.UpdatedAt)
	if err != nil {
		return tracking.Shipment{}, mappingError(fmt.Sprintf("tracking for order %s: %v", orderID, err))
	}

	out := tracking.Shipment{
		OrderExternalID: orderID,
		Carrier:         strings.TrimSpace(src.Carrier),
		TrackingNumber:  strings.TrimSpace(src.AWBNumber.String()),
		TrackingURL:     strings.TrimSpace(src.TrackingURL),
		Status:          NormaliseTrackingStatus(src.Status),
		SourceStatus:    strings.TrimSpace(src.Status),
		UpdatedAt:       updatedAt,
	}
	if !shippedAt.IsZero() {
		out.ShippedAt = &shippedAt
	}
	if err := out.Validate(); err != nil {
		return tracking.Shipment{}, mappingError(fmt.Sprintf("tracking for order %s: %v", orderID, err))
	}
	return out, nil
}

// NormaliseTrackingStatus maps a carrier/EasyEcom status label to the domain
// enumeration.
func NormaliseTrackingStatus(s string) tracking.Status {
	v := strings.ToLower(strings.TrimSpace(s))
	switch {
	case v == "":
		return tracking.StatusUnknown
	case strings.Contains(v, "cancel"):
		return tracking.StatusCancelled
	case strings.Contains(v, "return"), strings.Contains(v, "rto"):
		return tracking.StatusReturned
	case strings.Contains(v, "out for delivery"), strings.Contains(v, "out_for_delivery"):
		return tracking.StatusOutForDelivery
	case strings.Contains(v, "deliver"):
		return tracking.StatusDelivered
	case strings.Contains(v, "transit"):
		return tracking.StatusInTransit
	case strings.Contains(v, "ship"), strings.Contains(v, "manifest"), strings.Contains(v, "dispatch"), strings.Contains(v, "picked"):
		return tracking.StatusShipped
	case strings.Contains(v, "pending"), strings.Contains(v, "created"), strings.Contains(v, "booked"):
		return tracking.StatusPending
	default:
		return tracking.StatusUnknown
	}
}
