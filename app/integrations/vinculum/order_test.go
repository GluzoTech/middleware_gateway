package vinculum_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/domain/order"
	"github.com/gluzo/integration-gateway/app/integrations/vinculum"
	dtoorder "github.com/gluzo/integration-gateway/app/integrations/vinculum/dto/order"
)

// sampleOrder carries vendor item codes, as the workflow hands them over
// after translating against the SKU map.
func sampleOrder() order.Order {
	return order.Order{
		ExternalID:    "9876543",
		ReferenceCode: "AMZ-1",
		OrderedAt:     time.Date(2026, 9, 30, 11, 30, 0, 0, time.UTC),
		PaymentMode:   order.PaymentCashOnDelivery,
		TotalAmount:   999.50,
		Customer:      order.Customer{Name: "Asha Verma", Phone: "9876501234", Email: "asha@example.com"},
		ShippingAddress: order.Address{
			Name: "Asha Verma", Line1: "12 MG Road", City: "Pune",
			State: "Maharashtra", PostalCode: "411001", Phone: "9876501234",
		},
		Items: []order.Item{
			{ExternalID: "555001", SKU: "BCPL-CHY-500", Quantity: 2, UnitPrice: 499.75, TaxRate: 18},
			{ExternalID: "555002", SKU: "BCPL-HNY-250", Quantity: 1, UnitPrice: 250},
		},
	}
}

func decodePrepared(t *testing.T, doc []byte) dtoorder.CreateOrderRequest {
	t.Helper()
	var req dtoorder.CreateOrderRequest
	if err := json.Unmarshal(doc, &req); err != nil {
		t.Fatalf("decode prepared order: %v", err)
	}
	return req
}

func TestPrepareOrderMapsTheDocument(t *testing.T) {
	ctx := context.Background()
	f := newFakeVinculum(t)
	v := f.vendor(t)

	doc, err := v.PrepareOrder(ctx, sampleOrder(), vendorRoute())
	if err != nil {
		t.Fatalf("PrepareOrder: %v", err)
	}
	req := decodePrepared(t, doc)

	// Gluzo's own order id becomes Vinculum's order number: that is what
	// makes a second submission a duplicate rather than a new order.
	if req.OrderNo != "9876543" {
		t.Errorf("orderNo = %q, want the origin order id", req.OrderNo)
	}
	if req.OrderLocation != "DEL" {
		t.Errorf("orderLocation = %q, want the route's vendor reference", req.OrderLocation)
	}
	if req.PaymentType != "COD" {
		t.Errorf("paymentType = %q", req.PaymentType)
	}
	// BCPL books the courier, so the gateway has no waybill and must not
	// invent one.
	if req.AWBNo != "" {
		t.Errorf("awbNo = %q, want empty under dropship", req.AWBNo)
	}
	if req.ShipPincode != "411001" || req.ShipCity != "Pune" {
		t.Errorf("shipping address = %+v", req)
	}

	if len(req.OrderAmount) != 2 {
		t.Fatalf("got %d lines, want 2", len(req.OrderAmount))
	}
	// Line numbers are one-based and unique: Vinculum's shipment response is
	// line-level, so a repeated number would make a dispatch ambiguous.
	if req.OrderAmount[0].LineNo != 1 || req.OrderAmount[1].LineNo != 2 {
		t.Errorf("line numbers = %d, %d", req.OrderAmount[0].LineNo, req.OrderAmount[1].LineNo)
	}
	if req.OrderAmount[0].SKU != "BCPL-CHY-500" || req.OrderAmount[0].OrderQty != 2 {
		t.Errorf("line 1 = %+v", req.OrderAmount[0])
	}
	// The origin's line id travels in a user-defined field so a dispatch can
	// be traced back without another lookup.
	if req.OrderAmount[0].UDF1 != "555001" {
		t.Errorf("udf1 = %q, want the origin line id", req.OrderAmount[0].UDF1)
	}
	// A zero MRP reads as "free" on a customer-facing document.
	if req.OrderAmount[0].MRP != 499.75 {
		t.Errorf("mrp = %v, want the unit price when the origin carries none", req.OrderAmount[0].MRP)
	}
}

// PrepareOrder must be pure: the role contract says so, and a resumed run
// depends on it producing the same document from the same input.
func TestPrepareOrderIsPureAndReachesNoNetwork(t *testing.T) {
	ctx := context.Background()
	f := newFakeVinculum(t)
	v := f.vendor(t)

	first, err := v.PrepareOrder(ctx, sampleOrder(), vendorRoute())
	if err != nil {
		t.Fatalf("PrepareOrder: %v", err)
	}
	second, err := v.PrepareOrder(ctx, sampleOrder(), vendorRoute())
	if err != nil {
		t.Fatalf("PrepareOrder: %v", err)
	}
	if string(first) != string(second) {
		t.Error("the same order mapped to two different documents")
	}
	if f.orderCalls.Load() != 0 {
		t.Error("PrepareOrder made a network call")
	}
}

// An order that cannot be dispatched must fail mapping, not be shipped to an
// incomplete address — which nobody notices until a customer complains.
func TestPrepareOrderRejectsAnUnshippableOrder(t *testing.T) {
	ctx := context.Background()
	f := newFakeVinculum(t)
	v := f.vendor(t)

	o := sampleOrder()
	o.ShippingAddress.PostalCode = ""
	o.BillingAddress = order.Address{}

	_, err := v.PrepareOrder(ctx, o, vendorRoute())
	if err == nil {
		t.Fatal("an order with no postal code must not map")
	}
	if !contains(err.Error(), "postal code") {
		t.Errorf("error %q does not name the missing field", err)
	}
}

func TestSubmitOrderCreatesTheOrder(t *testing.T) {
	ctx := context.Background()
	f := newFakeVinculum(t)
	v := f.vendor(t)

	doc, err := v.PrepareOrder(ctx, sampleOrder(), vendorRoute())
	if err != nil {
		t.Fatalf("PrepareOrder: %v", err)
	}
	ack, err := v.SubmitOrder(ctx, doc, sampleOrder(), vendorRoute())
	if err != nil {
		t.Fatalf("SubmitOrder: %v", err)
	}
	if !ack.Created {
		t.Error("the first submission should report Created")
	}
	if ack.VendorOrderID != "9876543" {
		t.Errorf("vendor order id = %q", ack.VendorOrderID)
	}
	if got := f.createdOrders(); len(got) != 1 {
		t.Errorf("the vendor holds %d orders, want 1", len(got))
	}
}

// Exit criterion: submitting the same order twice creates one order at
// Vinculum.
func TestSubmittingTheSameOrderTwiceCreatesOne(t *testing.T) {
	ctx := context.Background()
	f := newFakeVinculum(t)
	v := f.vendor(t)

	doc, err := v.PrepareOrder(ctx, sampleOrder(), vendorRoute())
	if err != nil {
		t.Fatalf("PrepareOrder: %v", err)
	}

	first, err := v.SubmitOrder(ctx, doc, sampleOrder(), vendorRoute())
	if err != nil {
		t.Fatalf("first SubmitOrder: %v", err)
	}
	second, err := v.SubmitOrder(ctx, doc, sampleOrder(), vendorRoute())
	if err != nil {
		t.Fatalf("second SubmitOrder must not be an error: %v", err)
	}

	if !first.Created {
		t.Error("the first submission should report Created")
	}
	// The vendor rejected the repeat; that rejection is what makes this
	// idempotent, and it is reported as an existing order rather than raised.
	if second.Created {
		t.Error("the second submission reported a creation")
	}
	if second.VendorOrderID != first.VendorOrderID {
		t.Errorf("the two submissions named different orders: %q and %q", first.VendorOrderID, second.VendorOrderID)
	}
	if got := f.createdOrders(); len(got) != 1 {
		t.Fatalf("the vendor holds %d orders, want exactly 1: %v", len(got), got)
	}
}

// A transport failure or a 5xx says nothing about whether the order exists.
// Treating one as a duplicate would report an order as accepted that the
// vendor never saw.
func TestATransportFailureIsNotMistakenForADuplicate(t *testing.T) {
	ctx := context.Background()
	f := newFakeVinculum(t)
	f.httpStatus = 500
	v := f.vendor(t)

	doc, err := v.PrepareOrder(ctx, sampleOrder(), vendorRoute())
	if err != nil {
		t.Fatalf("PrepareOrder: %v", err)
	}
	if _, err := v.SubmitOrder(ctx, doc, sampleOrder(), vendorRoute()); err == nil {
		t.Fatal("a 5xx must not be reported as an existing order")
	}
}

// An unrecognised rejection fails loudly rather than being taken for a
// duplicate. The order is then stuck and visible — never duplicated, because
// Vinculum did the rejecting.
func TestAnUnrecognisedRejectionIsAnError(t *testing.T) {
	ctx := context.Background()
	f := newFakeVinculum(t)
	f.orderRejection = "location is closed for dispatch"
	v := f.vendor(t)

	doc, err := v.PrepareOrder(ctx, sampleOrder(), vendorRoute())
	if err != nil {
		t.Fatalf("PrepareOrder: %v", err)
	}
	ack, err := v.SubmitOrder(ctx, doc, sampleOrder(), vendorRoute())
	if err == nil {
		t.Fatalf("expected an error, got %+v", ack)
	}
}

// Exit criterion: the rate limiter holds under a burst of 200 queued orders.
func TestTheRateLimiterHoldsUnderABurst(t *testing.T) {
	ctx := context.Background()
	f := newFakeVinculum(t)

	// A fake clock that advances when the limiter sleeps, so a five-minute
	// window is exercised without a test that takes five minutes.
	clock := &fakeClock{now: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
	c, err := vinculum.NewClient(vinculum.Config{
		BaseURL: f.srv.URL, APIOwner: testOwner, APIKey: testKey,
		Location: "DEL", SellableBucket: "Good", Timeout: 2 * time.Second,
		OrderRateLimit: 80, OrderRateWindow: 5 * time.Minute,
	}, noSleep())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	vinculum.SetOrderLimiterClockForTest(c, clock.Now, clock.Sleep)

	const burst = 200
	// The time each call was permitted, read from the limiter's own clock:
	// the fake server sees wall time, which does not move here.
	calls := make([]time.Time, 0, burst)
	for i := 0; i < burst; i++ {
		req := dtoorder.CreateOrderRequest{
			OrderNo:       "ORD-" + itoa(i),
			OrderLocation: "DEL",
			OrderAmount:   []dtoorder.Line{{LineNo: 1, SKU: "BCPL-A", OrderQty: 1}},
		}
		if _, err := c.CreateOrder(ctx, req); err != nil {
			t.Fatalf("CreateOrder %d: %v", i, err)
		}
		calls = append(calls, clock.Now())
	}

	if got := f.orderCalls.Load(); int(got) != burst {
		t.Fatalf("the vendor saw %d calls, want %d", got, burst)
	}
	// No five-minute window may contain more than 80 calls. Checked over
	// every window that starts at a call, which is where a violation would
	// begin.
	for i := range calls {
		n := 0
		for j := i; j < len(calls); j++ {
			if calls[j].Sub(calls[i]) < 5*time.Minute {
				n++
			}
		}
		if n > 80 {
			t.Fatalf("%d calls fell within five minutes of call %d, over the documented 80", n, i)
		}
	}
	// And it did have to wait: 200 calls at 80 per five minutes cannot all
	// happen at once, so a limiter that never slept would be broken.
	if clock.slept == 0 {
		t.Error("the limiter never waited; 200 calls were let through instantly")
	}
}

func TestTheRateLimiterStopsOnCancellation(t *testing.T) {
	f := newFakeVinculum(t)
	clock := &fakeClock{now: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
	c, err := vinculum.NewClient(vinculum.Config{
		BaseURL: f.srv.URL, APIOwner: testOwner, APIKey: testKey,
		Location: "DEL", SellableBucket: "Good", Timeout: time.Second,
		OrderRateLimit: 1, OrderRateWindow: time.Hour,
	}, noSleep())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	vinculum.SetOrderLimiterClockForTest(c, clock.Now, func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	})

	req := dtoorder.CreateOrderRequest{OrderNo: "A", OrderLocation: "DEL",
		OrderAmount: []dtoorder.Line{{LineNo: 1, SKU: "S", OrderQty: 1}}}
	if _, err := c.CreateOrder(ctx, req); err != nil {
		t.Fatalf("first CreateOrder: %v", err)
	}
	req.OrderNo = "B"
	if _, err := c.CreateOrder(ctx, req); err == nil {
		t.Fatal("a cancelled wait must not proceed to the call")
	}
}

// fakeClock advances only when the limiter sleeps.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	slept int
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.slept++
	return nil
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
