package inventory

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gluzo/integration-gateway/app/integrations/easyecom/dto"
)

// MaxQuantity is the largest value EasyEcom stores.
//
// Values above it are silently clamped and reported rather than rejected, so
// the gateway caps on its own side: a push whose response quietly disagrees
// with what was sent is a push whose effect nobody can reason about.
const MaxQuantity = 10_000

// MaxBulkItems bounds one request.
//
// TODO(VERIFY): EasyEcom open item 9 — the documented limit on this endpoint
// is not published. 500 is a conservative batch, and the sink splits larger
// pushes rather than sending one enormous body. Correcting it is one
// constant.
const MaxBulkItems = 500

// BulkInventoryUpdateItem is one SKU's new quantity.
//
// Quantity is absolute: it replaces the stored value and is not a delta.
// That is what makes a vendor-owned stock position safe to mirror — the
// gateway never has to know what EasyEcom currently holds, and a missed run
// corrects itself on the next one rather than compounding.
type BulkInventoryUpdateItem struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

// BulkInventoryUpdateRequest replaces the quantity of several SKUs at once.
type BulkInventoryUpdateRequest struct {
	Items []BulkInventoryUpdateItem `json:"skus"`
}

// Validate reports whether the request can be sent.
func (r BulkInventoryUpdateRequest) Validate() error {
	if len(r.Items) == 0 {
		return errors.New("at least one sku is required")
	}
	if len(r.Items) > MaxBulkItems {
		return fmt.Errorf("at most %d skus per request, got %d", MaxBulkItems, len(r.Items))
	}
	var errs []error
	for i, item := range r.Items {
		if strings.TrimSpace(item.SKU) == "" {
			errs = append(errs, fmt.Errorf("item %d has no sku", i))
		}
		if item.Quantity < 0 {
			errs = append(errs, fmt.Errorf("item %d (%s) has a negative quantity", i, item.SKU))
		}
		if item.Quantity > MaxQuantity {
			errs = append(errs, fmt.Errorf("item %d (%s) exceeds the maximum quantity %d", i, item.SKU, MaxQuantity))
		}
	}
	return errors.Join(errs...)
}

// BulkInventoryUpdateResponse is the envelope returned by a bulk update.
//
// TODO(VERIFY): the EasyEcom Postman collection records the operation and its
// request body but not the shape of its per-SKU result. Failures are read
// from an optional list if one is present, and the absence of that list is
// treated as "all accepted", which is the reading consistent with the
// {code, message} envelope every other endpoint returns. Confirm before
// relying on partial-failure reporting.
type BulkInventoryUpdateResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`

	// Data carries per-SKU outcomes where EasyEcom reports them.
	Data []BulkInventoryUpdateResult `json:"data"`
}

// BulkInventoryUpdateResult is one SKU's outcome.
type BulkInventoryUpdateResult struct {
	SKU     string         `json:"sku"`
	Status  dto.FlexString `json:"status"`
	Message string         `json:"message"`
}

// Failed reports whether this result describes a rejection.
//
// An absent status is success: EasyEcom's envelope already carries the
// overall outcome, and a per-SKU entry that says nothing is not a failure.
func (r BulkInventoryUpdateResult) Failed() bool {
	switch strings.ToLower(strings.TrimSpace(r.Status.String())) {
	case "", "success", "ok", "1", "true", "updated":
		return false
	default:
		return true
	}
}
