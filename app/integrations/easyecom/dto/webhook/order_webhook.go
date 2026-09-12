// Package webhook holds the EasyEcom webhook payload contracts.
//
// EasyEcom's order triggers (Create Order, Confirm Order, Cancel Order and
// so on) deliver the same order representation as Get Order Details: V2
// sends a bare array of order objects, V1 wraps them in {"orders": [...],
// "nextUrl": ...}. The order DTO is therefore shared with the order package;
// only the outer shape differs.
package webhook

import (
	"github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/order"
)

// OrderWebhook is the body of an EasyEcom order webhook in either version.
type OrderWebhook struct {
	Orders  []order.Order
	NextURL string
}

// UnmarshalJSON implements json.Unmarshaler.
func (w *OrderWebhook) UnmarshalJSON(b []byte) error {
	var data order.OrdersData
	if err := data.UnmarshalJSON(b); err != nil {
		return err
	}
	w.Orders, w.NextURL = data.Orders, data.NextURL
	return nil
}
