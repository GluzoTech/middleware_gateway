package apperror_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gluzo/integration-gateway/app/apperror"
)

func TestInfoOf(t *testing.T) {
	if apperror.InfoOf(nil) != nil {
		t.Fatal("InfoOf(nil) should be nil")
	}

	e := apperror.Wrap(apperror.ExternalAPI, "unexpected status 502", errors.New("upstream said no"))
	e.Integration, e.Operation, e.HTTPStatus, e.ExternalCode = "dabur", "CreateSaleOrder", 502, "E1"
	e.Retryable = true
	info := apperror.InfoOf(e)
	if info.Category != apperror.ExternalAPI || info.Message != "unexpected status 502" || !info.Retryable || info.HTTPStatus != 502 || info.ExternalCode != "E1" || info.Detail != "upstream said no" || info.Operation != "CreateSaleOrder" {
		t.Fatalf("unexpected info: %+v", info)
	}
	if !strings.Contains(info.String(), "upstream said no") {
		t.Fatalf("String = %q", info.String())
	}

	plain := apperror.InfoOf(errors.New("boom"))
	if plain.Category != apperror.Internal || plain.Detail != "boom" || plain.Retryable {
		t.Fatalf("plain error info: %+v", plain)
	}

	long := apperror.InfoOf(errors.New(strings.Repeat("x", 1000)))
	if len(long.Detail) > 510 {
		t.Fatalf("detail not bounded: %d", len(long.Detail))
	}

	data, err := json.Marshal(info)
	if err != nil || !strings.Contains(string(data), `"category":"external_api_error"`) {
		t.Fatalf("json = %s, %v", data, err)
	}
}
