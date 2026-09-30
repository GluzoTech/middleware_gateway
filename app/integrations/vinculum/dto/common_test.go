package dto_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/integrations/vinculum/dto"
)

// Vinculum is inconsistent about whether a number arrives as a JSON number
// or as a quoted string, and which of the two a given field uses is not
// documented. Every Flex type has to accept both or a field silently reads
// as zero.
func TestFlexIntAcceptsNumbersAndStrings(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{`120`, 120, false},
		{`"120"`, 120, false},
		{`"120.0"`, 120, false},
		{`120.0`, 120, false},
		{`0`, 0, false},
		{`""`, 0, false},
		{`null`, 0, false},
		{`"  "`, 0, false},
		{`-5`, -5, false},
		{`"abc"`, 0, true},
		{`{"a":1}`, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			var got dto.FlexInt
			err := json.Unmarshal([]byte(tc.in), &got)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %s", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("unmarshal %s: %v", tc.in, err)
			}
			if int(got) != tc.want {
				t.Errorf("%s = %d, want %d", tc.in, int(got), tc.want)
			}
		})
	}
}

func TestFlexBoolAcceptsTheFormsHasMoreArrivesIn(t *testing.T) {
	tests := []struct {
		in      string
		want    bool
		wantErr bool
	}{
		{`true`, true, false},
		{`false`, false, false},
		{`"true"`, true, false},
		{`"TRUE"`, true, false},
		{`1`, true, false},
		{`0`, false, false},
		{`"Y"`, true, false},
		{`"no"`, false, false},
		{`null`, false, false},
		{`""`, false, false},
		{`"perhaps"`, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			var got dto.FlexBool
			err := json.Unmarshal([]byte(tc.in), &got)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %s", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("unmarshal %s: %v", tc.in, err)
			}
			if got.Bool() != tc.want {
				t.Errorf("%s = %v, want %v", tc.in, got.Bool(), tc.want)
			}
		})
	}
}

func TestFlexStringKeepsNumbersAsTheirLiteralText(t *testing.T) {
	var got dto.FlexString
	if err := json.Unmarshal([]byte(`9876543`), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// An order number that arrives unquoted must not become 9.876543e+06.
	if got.String() != "9876543" {
		t.Errorf("got %q, want the literal digits", got.String())
	}
}

func TestEnvelopeFailed(t *testing.T) {
	var ok dto.Envelope
	if err := json.Unmarshal([]byte(`{"responseCode":0,"responseMessage":"SUCCESS"}`), &ok); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ok.Failed() {
		t.Error("responseCode 0 is success")
	}

	var bad dto.Envelope
	if err := json.Unmarshal([]byte(`{"responseCode":"102","responseMessage":"bad sku"}`), &bad); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !bad.Failed() {
		t.Error("a non-zero responseCode is a failure, quoted or not")
	}
	if bad.ResponseMessage != "bad sku" {
		t.Errorf("message = %q", bad.ResponseMessage)
	}
}

// A missing envelope must not read as success. An empty body would otherwise
// be an empty successful page, and a sweep would take it as "no stock".
func TestEnvelopeAbsentCodeReadsAsSuccessOnly(t *testing.T) {
	var env dto.Envelope
	if err := json.Unmarshal([]byte(`{}`), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Failed() {
		t.Error("an absent responseCode decodes to zero, which is success; " +
			"callers distinguish an empty page by its row count")
	}
}

func TestFormatTime(t *testing.T) {
	if got := dto.FormatTime(time.Time{}); got != "" {
		t.Errorf("a zero time must render empty so the bound is omitted, got %q", got)
	}
	ts := time.Date(2026, 9, 28, 14, 5, 0, 0, time.UTC)
	if got := dto.FormatTime(ts); got != "2026-09-28 14:05:00" {
		t.Errorf("got %q", got)
	}
	// Anything handed in is normalised to UTC, so a local-time caller cannot
	// shift the window by its own offset.
	ist := time.FixedZone("IST", 5*3600+1800)
	if got := dto.FormatTime(ts.In(ist)); got != "2026-09-28 14:05:00" {
		t.Errorf("got %q, want the same instant in UTC", got)
	}
}

func TestParseTime(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{"", time.Time{}, false},
		{"   ", time.Time{}, false},
		{"2026-09-28 14:05:00", time.Date(2026, 9, 28, 14, 5, 0, 0, time.UTC), false},
		{"2026-09-28T14:05:00", time.Date(2026, 9, 28, 14, 5, 0, 0, time.UTC), false},
		{"2026-09-28T14:05:00Z", time.Date(2026, 9, 28, 14, 5, 0, 0, time.UTC), false},
		{"28-09-2026 14:05:00", time.Date(2026, 9, 28, 14, 5, 0, 0, time.UTC), false},
		{"2026-09-28", time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC), false},
		{"last Tuesday", time.Time{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := dto.ParseTime(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse %q: %v", tc.in, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("%q = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}
