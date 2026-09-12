package intlog_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/intlog"
)

func TestSanitizerRedactsSecretKeysAtAnyDepth(t *testing.T) {
	s := intlog.NewSanitizer("x-custom-signature")
	in := map[string]any{
		"Authorization": "Bearer abc",
		"X-API-Key":     "key",
		"nested": map[string]any{
			"access_token":  "t",
			"client_secret": "s",
			"user_password": "p",
			"safe":          "value",
			"list":          []any{"a", map[string]any{"Token": "z"}},
		},
		"headers":            http.Header{"Cookie": {"c"}, "Accept": {"application/json"}},
		"x-custom-signature": "sig",
		"amount":             12.5,
	}
	out := s.Map(in)

	data, _ := json.Marshal(out)
	for _, leaked := range []string{`"abc"`, `"key"`, `"t"`, `"s"`, `"p"`, `"z"`, `"c"`, `"sig"`} {
		if strings.Contains(string(data), leaked) {
			t.Errorf("secret %s leaked: %s", leaked, data)
		}
	}
	nested := out["nested"].(map[string]any)
	if nested["safe"] != "value" || out["amount"] != 12.5 {
		t.Errorf("non-secret values changed: %s", data)
	}
	if h := out["headers"].(map[string]any); h["Accept"] != "application/json" || h["Cookie"] != intlog.Redacted {
		t.Errorf("headers: %v", h)
	}
	// The input must be untouched.
	if in["Authorization"] != "Bearer abc" {
		t.Error("sanitizer mutated its input")
	}
}

func TestSanitizerMasksPII(t *testing.T) {
	s := intlog.NewSanitizer()
	out := s.Map(map[string]any{"email": "asha@example.com", "contact_num": "9876501234", "phone": "12", "name": "Asha"})
	if out["email"] != "a***@example.com" || out["contact_num"] != "******1234" || out["phone"] != "****" || out["name"] != "Asha" {
		t.Fatalf("masked = %v", out)
	}
}

func TestSanitizerScrubsFreeText(t *testing.T) {
	s := intlog.NewSanitizer()
	tests := map[string]string{
		"Get \"https://x/oauth/token?password=hunter2&grant_type=password\"": "hunter2",
		"header Authorization: Bearer eyJhbGci.abc.def rejected":             "eyJhbGci",
		"api_key=681d976c3b failed":                                          "681d976c3b",
		`{"token": "abc123"}`:                                                "abc123",
	}
	for in, secret := range tests {
		got := s.Text(in)
		if strings.Contains(got, secret) {
			t.Errorf("Text(%q) = %q still contains %q", in, got, secret)
		}
		if !strings.Contains(got, intlog.Redacted) {
			t.Errorf("Text(%q) = %q lacks redaction marker", in, got)
		}
	}
	if got := s.Text("order 123 shipped"); got != "order 123 shipped" {
		t.Errorf("harmless text changed: %q", got)
	}
}

func TestSanitizerEntry(t *testing.T) {
	s := intlog.NewSanitizer()
	e := intlog.Entry{
		Action:   "FETCH_ORDER",
		Request:  map[string]any{"headers": map[string]any{"Authorization": "Bearer x"}, "path": "/orders"},
		Response: map[string]any{"status": 401},
		Details:  map[string]any{"jwt_token": "abc"},
		Error:    &apperror.Info{Message: "unauthorized", Detail: "Authorization: Bearer secret-token rejected"},
	}
	out := s.Entry(e)
	if out.Request["headers"].(map[string]any)["Authorization"] != intlog.Redacted || out.Request["path"] != "/orders" {
		t.Fatalf("request = %v", out.Request)
	}
	if out.Details["jwt_token"] != intlog.Redacted {
		t.Fatalf("details = %v", out.Details)
	}
	if strings.Contains(out.Error.Detail, "secret-token") {
		t.Fatalf("error detail leaked: %s", out.Error.Detail)
	}
	if e.Error.Detail == out.Error.Detail {
		t.Fatal("original entry's error was modified")
	}
}
