// Package inventory holds the Vinculum stock contracts.
//
// Source: the live Vinculum eRetail specification for
// POST /RestWS/api/eretail/v4/stock/getWhInventory, read 29 September 2026.
// Field names below are taken from it; nothing is inferred. Where the
// specification names a parameter without describing its accepted values,
// the field is carried through unchanged and marked.
package inventory

import (
	"errors"
	"strings"
	"time"

	"github.com/gluzo/integration-gateway/app/integrations/vinculum/dto"
)

// GetWhInventoryRequest selects which stock rows to read.
//
// Every field is optional except LocCode: an empty SKU list means "every
// SKU at this location", which is what a full sweep wants. PageNumber is
// one-based, following the specification.
type GetWhInventoryRequest struct {
	SKUCodes []string
	Buckets  string
	LocCode  string

	PageNumber int

	// FromDate and ToDate bound the pull to rows changed in a period, which
	// is what makes an incremental sweep possible. Zero values are omitted.
	FromDate time.Time
	ToDate   time.Time

	// ReqType is documented as a parameter without an enumerated set of
	// values. It is passed through when set and omitted when not, so a value
	// BCPL supplies later needs no code change.
	ReqType string
}

// Validate reports whether the request addresses a location.
func (r GetWhInventoryRequest) Validate() error {
	if strings.TrimSpace(r.LocCode) == "" {
		return errors.New("location code is required")
	}
	return nil
}

// Body renders the request as the JSON document Vinculum expects. Empty
// fields are omitted rather than sent blank, because the specification does
// not say how a blank bound is treated and an omitted one is unambiguous.
func (r GetWhInventoryRequest) Body() map[string]any {
	body := map[string]any{
		"locCode": strings.TrimSpace(r.LocCode),
	}
	if skus := trimAll(r.SKUCodes); len(skus) > 0 {
		body["skuCodes"] = skus
	}
	if b := strings.TrimSpace(r.Buckets); b != "" {
		body["buckets"] = b
	}
	if r.PageNumber > 0 {
		body["pageNumber"] = r.PageNumber
	}
	if v := dto.FormatTime(r.FromDate); v != "" {
		body["fromDate"] = v
	}
	if v := dto.FormatTime(r.ToDate); v != "" {
		body["toDate"] = v
	}
	if t := strings.TrimSpace(r.ReqType); t != "" {
		body["reqType"] = t
	}
	return body
}

// GetWhInventoryResponse is one page of stock rows.
type GetWhInventoryResponse struct {
	dto.Envelope

	// HasMore reports whether a further page exists. It drives paging; the
	// caller increments pageNumber while it is true.
	HasMore dto.FlexBool `json:"hasMore"`

	Response []StockRow `json:"response"`
}

// StockRow is the stock position of one SKU in one bucket at one location.
type StockRow struct {
	// SKUCode is BCPL's item code, not Gluzo's. Translating the two is the
	// SKU map's job and happens outside this package.
	SKUCode  dto.FlexString `json:"skuCode"`
	Location dto.FlexString `json:"location"`

	// Qty is the quantity held and CommittedQty the portion already promised
	// to open orders. Which of the two yields the sellable figure is
	// assumption A1 in the integration plan, and it is applied in the mapper
	// rather than here: a DTO records what arrived.
	Qty          dto.FlexInt `json:"qty"`
	CommittedQty dto.FlexInt `json:"committedQty"`

	// Bucket is the stock category. Only the sellable bucket reaches the
	// storefront.
	Bucket dto.FlexString `json:"bucket"`
}

func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
