// Package dto holds the building blocks shared by the Vinculum eRetail API
// contracts.
//
// Vinculum wraps every response in a responseCode/responseMessage envelope
// and is inconsistent about whether numbers arrive as JSON numbers or as
// quoted strings. The Flex types absorb that at the boundary so the rest of
// the gateway sees ordinary Go values.
package dto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SuccessCode is the responseCode Vinculum returns when a call succeeded.
//
// TODO(VERIFY): the published specification names the field but does not
// enumerate its values. Zero-means-success is the convention the envelope is
// read against; confirm it with BCPL before Phase 4 or 5 writes anything.
// Getting it wrong is loud rather than silent — every call would be reported
// as an error — so it fails safe.
const SuccessCode = 0

// RequestTimeLayout is the format dates are sent in.
//
// TODO(VERIFY): the specification documents fromDate, toDate, date_from and
// date_to as date parameters without giving their format. This layout is the
// one Vinculum uses elsewhere in its payloads; confirm it with BCPL. It is
// referenced in one place per request type so correcting it is a one-line
// change.
const RequestTimeLayout = "2006-01-02 15:04:05"

// responseTimeLayouts are the formats a timestamp in a response is tried
// against, in order. Several are accepted because the specification does not
// state which one a given field uses, and rejecting a shipment over a date
// format would lose tracking the customer is waiting for.
var responseTimeLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	time.RFC3339,
	"02-01-2006 15:04:05",
	"02/01/2006 15:04:05",
	"2006-01-02",
	"02-01-2006",
}

// FormatTime renders t for a request parameter. A zero time renders empty so
// that an unset bound is simply omitted.
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(RequestTimeLayout)
}

// ParseTime reads a timestamp from a response. An empty value yields the zero
// time and no error, because most date fields are optional and an absent
// dispatch date is not a fault.
func ParseTime(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, nil
	}
	for _, layout := range responseTimeLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("dto: unrecognised timestamp %q", v)
}

// Envelope is the wrapper every Vinculum response carries.
type Envelope struct {
	ResponseCode    FlexInt `json:"responseCode"`
	ResponseMessage string  `json:"responseMessage"`
}

// Failed reports whether the envelope describes an application-level error.
func (e Envelope) Failed() bool { return int(e.ResponseCode) != SuccessCode }

// FlexInt decodes a JSON number or numeric string into an int. Null, absent
// and empty-string values decode to zero.
type FlexInt int

// UnmarshalJSON implements json.Unmarshaler.
func (i *FlexInt) UnmarshalJSON(b []byte) error {
	text, ok, err := scalarText(b)
	if err != nil || !ok {
		*i = 0
		return err
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return fmt.Errorf("dto: cannot decode %q as an integer", text)
	}
	*i = FlexInt(int(n))
	return nil
}

// FlexFloat decodes a JSON number or numeric string into a float64.
type FlexFloat float64

// UnmarshalJSON implements json.Unmarshaler.
func (f *FlexFloat) UnmarshalJSON(b []byte) error {
	text, ok, err := scalarText(b)
	if err != nil || !ok {
		*f = 0
		return err
	}
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return fmt.Errorf("dto: cannot decode %q as a number", text)
	}
	*f = FlexFloat(v)
	return nil
}

// FlexString decodes a JSON string, number or boolean into a string.
type FlexString string

// UnmarshalJSON implements json.Unmarshaler.
func (s *FlexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*s = ""
		return nil
	}
	if b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = FlexString(v)
		return nil
	}
	if b[0] == '{' || b[0] == '[' {
		return fmt.Errorf("dto: cannot decode %s as a string", truncate(b))
	}
	*s = FlexString(string(b))
	return nil
}

// String returns the underlying value.
func (s FlexString) String() string { return string(s) }

// FlexBool decodes a JSON boolean, number or quoted form of either. Vinculum
// reports hasMore as any of them depending on the endpoint.
type FlexBool bool

// UnmarshalJSON implements json.Unmarshaler.
func (v *FlexBool) UnmarshalJSON(b []byte) error {
	text, ok, err := scalarText(b)
	if err != nil || !ok {
		*v = false
		return err
	}
	switch strings.ToLower(text) {
	case "true", "1", "y", "yes":
		*v = true
	case "false", "0", "n", "no":
		*v = false
	default:
		return fmt.Errorf("dto: cannot decode %q as a boolean", text)
	}
	return nil
}

// Bool returns the underlying value.
func (v FlexBool) Bool() bool { return bool(v) }

// scalarText reduces a JSON scalar to its text. ok is false for null, absent
// and empty-string values, which every Flex type treats as a zero value.
func scalarText(b []byte) (string, bool, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return "", false, nil
	}
	if b[0] == '{' || b[0] == '[' {
		return "", false, fmt.Errorf("dto: cannot decode %s as a scalar", truncate(b))
	}
	text := string(b)
	if b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return "", false, err
		}
		text = v
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false, nil
	}
	return text, true, nil
}

func truncate(b []byte) []byte {
	const max = 32
	if len(b) <= max {
		return b
	}
	return b[:max]
}
