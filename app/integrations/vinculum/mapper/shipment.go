package mapper

import (
	"strings"
	"time"

	"github.com/gluzo/integration-gateway/app/domain/tracking"
	"github.com/gluzo/integration-gateway/app/integrations/vinculum/dto"
	dtoshipment "github.com/gluzo/integration-gateway/app/integrations/vinculum/dto/shipment"
)

// ShipmentSummary reports what a mapping did with records it did not return.
type ShipmentSummary struct {
	// NotShipped counts orders with no shipDetail block yet. This is the
	// normal state of an order between acceptance and dispatch, not a fault.
	NotShipped int
	// Skipped counts records that could not be mapped: no order number, or
	// a shipment that fails its own validation.
	Skipped int
}

// ToDomainShipments converts Vinculum dispatch records into domain shipments.
//
// observedAt stamps UpdatedAt; it is passed in so the mapper stays pure.
//
// Records are dropped rather than failing the batch: one unmapped order must
// not cost the tracking of every other order in the page.
func ToDomainShipments(src []dtoshipment.OrderShipment, observedAt time.Time) ([]tracking.Shipment, ShipmentSummary) {
	out := make([]tracking.Shipment, 0, len(src))
	var sum ShipmentSummary
	for _, rec := range src {
		s, err := ToDomainShipment(rec, observedAt)
		switch {
		case err != nil:
			sum.Skipped++
		case s == nil:
			sum.NotShipped++
		default:
			out = append(out, *s)
		}
	}
	return out, sum
}

// ToDomainShipment converts one dispatch record.
//
// A nil shipment with a nil error means the order has not shipped yet, which
// is an ordinary state and deliberately not an error: an order waiting in
// BCPL's warehouse is exactly what the sweep expects to find most of the
// time.
func ToDomainShipment(rec dtoshipment.OrderShipment, observedAt time.Time) (*tracking.Shipment, error) {
	// ExtOrderNo is the number the gateway sent, which is EasyEcom's order
	// id and the identifier every other part of the gateway uses. Vinculum's
	// own order_no is the fallback, and means nothing downstream.
	orderID := strings.TrimSpace(rec.ExtOrderNo.String())
	if orderID == "" {
		orderID = strings.TrimSpace(rec.OrderNo.String())
	}
	if orderID == "" {
		return nil, mappingError("shipment record has neither extOrderNo nor order_no")
	}
	if rec.ShipDetail == nil {
		return nil, nil
	}
	d := rec.ShipDetail

	shippedAt, err := dto.ParseTime(d.ShipDate.String())
	if err != nil {
		return nil, mappingErrorf("shipment for order %s: shipdate: %v", orderID, err)
	}
	deliveredAt, err := dto.ParseTime(d.DeliveredDate.String())
	if err != nil {
		return nil, mappingErrorf("shipment for order %s: delivereddate: %v", orderID, err)
	}

	// The dispatch block carries its own status; the order-level one is the
	// fallback for a record that ships without restating it.
	sourceStatus := strings.TrimSpace(d.Status.String())
	if sourceStatus == "" {
		sourceStatus = strings.TrimSpace(rec.Status.String())
	}

	out := tracking.Shipment{
		OrderExternalID: orderID,
		Carrier:         strings.TrimSpace(d.Transporter.String()),
		TrackingNumber:  strings.TrimSpace(d.TrackingNumber.String()),
		TrackingURL:     strings.TrimSpace(d.TrackingURL.String()),
		Status:          NormaliseShipmentStatus(sourceStatus),
		SourceStatus:    sourceStatus,
		UpdatedAt:       observedAt,
		InvoiceNumber:   strings.TrimSpace(d.InvoiceNo.String()),
		SellerGSTIN:     strings.TrimSpace(d.SellerGstNo.String()),
	}
	if !shippedAt.IsZero() {
		out.ShippedAt = &shippedAt
	}
	if !deliveredAt.IsZero() {
		out.DeliveredAt = &deliveredAt
	}
	if err := out.Validate(); err != nil {
		return nil, mappingErrorf("shipment for order %s: %v", orderID, err)
	}
	return &out, nil
}

// NormaliseShipmentStatus maps a Vinculum dispatch label to the domain
// enumeration.
//
// Matching is by keyword rather than by an exact table because the
// specification names the status parameter without enumerating its values.
// An unrecognised label becomes StatusUnknown and keeps its original text in
// SourceStatus, so nothing is silently reclassified and the raw value stays
// visible in the execution log.
//
// TODO(VERIFY): replace with the exact set once BCPL supply it.
func NormaliseShipmentStatus(s string) tracking.Status {
	v := strings.ToLower(strings.TrimSpace(s))
	switch {
	case v == "":
		return tracking.StatusUnknown
	case strings.Contains(v, "cancel"):
		return tracking.StatusCancelled
	case strings.Contains(v, "return"), strings.Contains(v, "rto"):
		return tracking.StatusReturned
	case strings.Contains(v, "out for delivery"), strings.Contains(v, "out_for_delivery"), strings.Contains(v, "ofd"):
		return tracking.StatusOutForDelivery
	case strings.Contains(v, "deliver"):
		return tracking.StatusDelivered
	case strings.Contains(v, "transit"):
		return tracking.StatusInTransit
	case strings.Contains(v, "ship"), strings.Contains(v, "dispatch"), strings.Contains(v, "manifest"), strings.Contains(v, "picked"), strings.Contains(v, "handover"):
		return tracking.StatusShipped
	case strings.Contains(v, "pending"), strings.Contains(v, "created"), strings.Contains(v, "new"), strings.Contains(v, "pack"), strings.Contains(v, "process"):
		return tracking.StatusPending
	default:
		return tracking.StatusUnknown
	}
}
