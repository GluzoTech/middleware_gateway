// Package dto holds the building blocks shared by EasyEcom API contracts.
//
// EasyEcom responses are wrapped in a {code, message, data} envelope, and
// identifiers and amounts arrive as JSON strings in some payloads and as
// numbers in others. The Flex types absorb that variance at the boundary so
// the rest of the gateway sees consistent Go values.
package dto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Envelope is the standard EasyEcom response wrapper.
type Envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// FlexString decodes a JSON string, number or boolean into a string. Null
// and absent values decode to "".
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
	*s = FlexString(string(b)) // numbers and booleans keep their literal text
	return nil
}

// String returns the underlying value.
func (s FlexString) String() string { return string(s) }

// FlexFloat decodes a JSON number or numeric string into a float64. Null,
// absent and empty-string values decode to zero.
type FlexFloat float64

// UnmarshalJSON implements json.Unmarshaler.
func (f *FlexFloat) UnmarshalJSON(b []byte) error {
	text, ok, err := scalarText(b)
	if err != nil || !ok {
		*f = 0
		return err
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return fmt.Errorf("dto: %q is not a number", text)
	}
	*f = FlexFloat(n)
	return nil
}

// FlexInt decodes a JSON integer, integral float or numeric string into an
// int64. Null, absent and empty-string values decode to zero.
type FlexInt int64

// UnmarshalJSON implements json.Unmarshaler.
func (i *FlexInt) UnmarshalJSON(b []byte) error {
	text, ok, err := scalarText(b)
	if err != nil || !ok {
		*i = 0
		return err
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		*i = FlexInt(n)
		return nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || f != float64(int64(f)) {
		return fmt.Errorf("dto: %q is not an integer", text)
	}
	*i = FlexInt(int64(f))
	return nil
}

// scalarText returns the textual form of a JSON scalar. ok is false for
// null and empty strings.
func scalarText(b []byte) (string, bool, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return "", false, nil
	}
	if b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return "", false, err
		}
		v = strings.TrimSpace(v)
		if v == "" {
			return "", false, nil
		}
		return v, true, nil
	}
	if b[0] == '{' || b[0] == '[' {
		return "", false, fmt.Errorf("dto: cannot decode %s as a number", truncate(b))
	}
	return string(b), true, nil
}

func truncate(b []byte) string {
	if len(b) > 32 {
		return string(b[:32]) + "..."
	}
	return string(b)
}
