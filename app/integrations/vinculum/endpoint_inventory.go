package vinculum

import (
	"context"

	dtoinventory "github.com/gluzo/integration-gateway/app/integrations/vinculum/dto/inventory"
)

// whInventoryPath is the warehouse stock endpoint.
const whInventoryPath = "/RestWS/api/eretail/v4/stock/getWhInventory"

// MaxStockPages bounds a paged sweep.
//
// hasMore comes from the vendor, so a vendor defect could leave it true
// forever and the loop would never end. Sweeping stops at this many pages and
// reports what it has rather than spinning; at Vinculum's page sizes this is
// far more stock than BCPL list.
const MaxStockPages = 500

// GetWhInventory reads one page of stock for a location.
//
// Follow GetWhInventoryResponse.HasMore by incrementing the request's
// PageNumber, or call FetchAllWhInventory to do it.
func (c *Client) GetWhInventory(ctx context.Context, req dtoinventory.GetWhInventoryRequest) (*dtoinventory.GetWhInventoryResponse, error) {
	const op = "GetWhInventory"
	if req.LocCode == "" {
		req.LocCode = c.Location()
	}
	if err := req.Validate(); err != nil {
		return nil, invalidRequest(op, err)
	}

	var resp dtoinventory.GetWhInventoryResponse
	if err := c.post(ctx, whInventoryPath, op, req.Body(), &resp); err != nil {
		return nil, err
	}
	if err := checkEnvelope(op, resp.Envelope); err != nil {
		return nil, err
	}
	return &resp, nil
}

// FetchAllWhInventory reads every page for a request and returns the rows
// concatenated.
//
// A partial read is reported as an error rather than as a short result: a
// stock sweep that silently returned half the catalogue would zero out the
// other half at the storefront. Callers that want partial results page
// themselves.
func (c *Client) FetchAllWhInventory(ctx context.Context, req dtoinventory.GetWhInventoryRequest) ([]dtoinventory.StockRow, error) {
	var rows []dtoinventory.StockRow
	page := req.PageNumber
	if page < 1 {
		page = 1
	}
	for n := 0; n < MaxStockPages; n++ {
		req.PageNumber = page
		resp, err := c.GetWhInventory(ctx, req)
		if err != nil {
			return nil, err
		}
		rows = append(rows, resp.Response...)
		if !resp.HasMore.Bool() || len(resp.Response) == 0 {
			return rows, nil
		}
		page++
	}
	return rows, pageLimitExceeded("GetWhInventory", MaxStockPages)
}
