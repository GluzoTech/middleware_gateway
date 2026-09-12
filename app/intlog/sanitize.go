package intlog

import (
	"net/http"
	"regexp"
	"strings"
)

// Redacted replaces every sensitive value.
const Redacted = "***REDACTED***"

// DefaultSecretKeys are matched case-insensitively as substrings of map keys
// and header names. Any key containing one of them is redacted outright.
var DefaultSecretKeys = []string{
	"authorization",
	"x-api-key",
	"access-token",
	"access_token",
	"accesstoken",
	"refresh_token",
	"jwt_token",
	"api_key",
	"apikey",
	"password",
	"passwd",
	"secret",
	"client_secret",
	"token",
	"cookie",
	"set-cookie",
	"credit_card",
	"card_number",
	"cvv",
}

// DefaultPIIKeys are masked rather than removed: enough is kept to correlate
// a record with a customer, not enough to contact them from the logs.
var DefaultPIIKeys = []string{
	"email",
	"phone",
	"mobile",
	"contact_num",
	"contactnum",
	"notificationmobile",
	"notificationemail",
}

// secretPatterns catch credentials embedded in free text such as error
// messages: "Bearer abc…", "password=…", "api_key: …".
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9\-._~+/]+=*`),
	regexp.MustCompile(`(?i)\b(password|passwd|secret|client_secret|token|access_token|refresh_token|jwt_token|api_key|apikey|x-api-key)(["']?\s*[=:]\s*["']?)[^\s"'&,;]+`),
}

// Sanitizer removes secrets and masks personal data in log entries.
type Sanitizer struct {
	secretKeys []string
	piiKeys    []string
}

// NewSanitizer builds a Sanitizer with the default key lists plus extraKeys
// treated as secrets.
func NewSanitizer(extraSecretKeys ...string) *Sanitizer {
	s := &Sanitizer{piiKeys: append([]string(nil), DefaultPIIKeys...)}
	for _, k := range append(append([]string(nil), DefaultSecretKeys...), extraSecretKeys...) {
		s.secretKeys = append(s.secretKeys, strings.ToLower(k))
	}
	return s
}

// Entry returns a sanitised copy of e.
func (s *Sanitizer) Entry(e Entry) Entry {
	e.Request = s.Map(e.Request)
	e.Response = s.Map(e.Response)
	e.Details = s.Map(e.Details)
	if e.Error != nil {
		errCopy := *e.Error
		errCopy.Message = s.Text(errCopy.Message)
		errCopy.ExternalMessage = s.Text(errCopy.ExternalMessage)
		errCopy.Detail = s.Text(errCopy.Detail)
		e.Error = &errCopy
	}
	return e
}

// Map returns a sanitised deep copy of m. The input is never modified.
func (s *Sanitizer) Map(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out, _ := s.value("", m).(map[string]any)
	return out
}

// Headers returns a sanitised copy of h as a plain map suitable for a log
// entry's Request or Response.
func (s *Sanitizer) Headers(h http.Header) map[string]any {
	if h == nil {
		return nil
	}
	out := make(map[string]any, len(h))
	for k, vs := range h {
		switch {
		case s.isSecretKey(k):
			out[k] = Redacted
		case len(vs) == 1:
			out[k] = s.Text(vs[0])
		default:
			copied := make([]any, len(vs))
			for i, v := range vs {
				copied[i] = s.Text(v)
			}
			out[k] = copied
		}
	}
	return out
}

// Text masks credential-looking fragments inside free text.
func (s *Sanitizer) Text(v string) string {
	for _, p := range secretPatterns {
		v = p.ReplaceAllString(v, "${1}${2}"+Redacted)
	}
	return v
}

func (s *Sanitizer) value(key string, v any) any {
	if key != "" && s.isSecretKey(key) {
		return Redacted
	}
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, inner := range t {
			out[k] = s.value(k, inner)
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(t))
		for k, inner := range t {
			out[k] = s.value(k, inner)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, inner := range t {
			out[i] = s.value("", inner)
		}
		return out
	case []string:
		out := make([]any, len(t))
		for i, inner := range t {
			out[i] = s.value("", inner)
		}
		return out
	case http.Header:
		return s.Headers(t)
	case string:
		if key != "" && s.isPIIKey(key) {
			return maskPII(t)
		}
		return s.Text(t)
	default:
		return v
	}
}

func (s *Sanitizer) isSecretKey(key string) bool {
	k := strings.ToLower(key)
	for _, secret := range s.secretKeys {
		if strings.Contains(k, secret) {
			return true
		}
	}
	return false
}

func (s *Sanitizer) isPIIKey(key string) bool {
	k := strings.ToLower(key)
	for _, pii := range s.piiKeys {
		if strings.Contains(k, pii) {
			return true
		}
	}
	return false
}

// maskPII keeps the domain of an email and the last four digits of a phone
// number.
func maskPII(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return v
	}
	if at := strings.LastIndex(v, "@"); at > 0 {
		return string(v[0]) + "***" + v[at:]
	}
	if len(v) <= 4 {
		return "****"
	}
	return strings.Repeat("*", len(v)-4) + v[len(v)-4:]
}
