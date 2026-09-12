package order

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// GetOrderDetailsResponse is the envelope returned by Get Order Details.
type GetOrderDetailsResponse struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    OrdersData `json:"data"`
}

// OrdersData is the data member. EasyEcom has shipped it in three shapes:
// a bare array of orders (V2), an object {"orders": [...], "nextUrl": ...}
// (V1) and a single order object. All three decode into Orders.
type OrdersData struct {
	Orders  []Order
	NextURL string
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *OrdersData) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	d.Orders, d.NextURL = nil, ""
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return nil
	}
	switch b[0] {
	case '[':
		return json.Unmarshal(b, &d.Orders)
	case '{':
		var wrapped struct {
			Orders  []Order         `json:"orders"`
			NextURL string          `json:"nextUrl"`
			OrderID json.RawMessage `json:"order_id"`
		}
		if err := json.Unmarshal(b, &wrapped); err != nil {
			return err
		}
		if wrapped.Orders != nil {
			d.Orders, d.NextURL = wrapped.Orders, wrapped.NextURL
			return nil
		}
		if wrapped.OrderID != nil {
			var single Order
			if err := json.Unmarshal(b, &single); err != nil {
				return err
			}
			d.Orders = []Order{single}
		}
		return nil
	default:
		return fmt.Errorf("order data must be an array or object, got %s", string(b[:min(len(b), 16)]))
	}
}
