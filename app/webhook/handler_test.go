package webhook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/gluzo/integration-gateway/app/auth"
	"github.com/gluzo/integration-gateway/app/correlation"
	"github.com/gluzo/integration-gateway/app/httpserver/middleware"
	"github.com/gluzo/integration-gateway/app/idempotency"
	"github.com/gluzo/integration-gateway/app/integrations/easyecom"
	"github.com/gluzo/integration-gateway/app/queue"
	"github.com/gluzo/integration-gateway/app/webhook"
)

const (
	platformKey  = "gluzo_pk_easyecom"
	otherKey     = "gluzo_pk_shopify"
	accessToken  = "gluzo_at_easyecom_dabur"
	otherToken   = "gluzo_at_shopify_dabur"
	validPayload = `[{"order_id":9876543,"invoice_id":"INV-1","reference_code":"AMZ-1","warehouse_id":12345,"order_items":[{"sku":"A","suborder_quantity":1}]}]`
)

type failingPublisher struct{ err error }

func (f failingPublisher) Publish(context.Context, queue.Job) error { return f.err }

type failingStore struct {
	idempotency.Store
	err error
}

func (f failingStore) Claim(context.Context, idempotency.Record) (idempotency.Claim, error) {
	return idempotency.Claim{}, f.err
}

type env struct {
	router   *gin.Engine
	logs     *bytes.Buffer
	queue    *queue.Memory
	store    *idempotency.MemoryStore
	integ    auth.Integration
	platform auth.Platform
}

func newEnv(t *testing.T, opts ...func(*env, *webhook.Handler)) *env {
	t.Helper()
	gin.SetMode(gin.TestMode)
	e := &env{logs: &bytes.Buffer{}, queue: queue.NewMemory(16), store: idempotency.NewMemoryStore()}
	logger := slog.New(slog.NewJSONHandler(e.logs, nil))

	creds := auth.NewMemoryStore()
	e.platform = auth.Platform{ID: uuid.New(), Name: "easyecom", Type: auth.PlatformTypeSource, Status: auth.StatusActive}
	shopify := auth.Platform{ID: uuid.New(), Name: "shopify", Type: auth.PlatformTypeSource, Status: auth.StatusActive}
	creds.AddPlatform(e.platform, platformKey)
	creds.AddPlatform(shopify, otherKey)
	e.integ = auth.Integration{ID: uuid.New(), Name: "easyecom-dabur", SourcePlatformID: e.platform.ID, Status: auth.StatusActive}
	creds.AddToken(auth.Token{ID: uuid.New(), IntegrationID: e.integ.ID}, e.integ, accessToken)
	shopifyInteg := auth.Integration{ID: uuid.New(), Name: "shopify-dabur", SourcePlatformID: shopify.ID, Status: auth.StatusActive}
	creds.AddToken(auth.Token{ID: uuid.New(), IntegrationID: shopifyInteg.ID}, shopifyInteg, otherToken)

	handler := webhook.NewHandler(easyecom.WebhookParser{}, e.store, e.queue, logger)
	var publisher queue.Publisher = e.queue
	var store idempotency.Store = e.store
	for _, opt := range opts {
		opt(e, handler)
	}
	if e.queue == nil {
		publisher = failingPublisher{err: errors.New("redis down")}
		handler = webhook.NewHandler(easyecom.WebhookParser{}, store, publisher, logger)
	}
	if e.store == nil {
		store = failingStore{err: errors.New("postgres down")}
		handler = webhook.NewHandler(easyecom.WebhookParser{}, store, publisher, logger)
	}

	r := gin.New()
	r.Use(middleware.Correlation(), middleware.BodyLimit(1024))
	g := r.Group("/webhooks/easyecom",
		auth.AcceptCombinedCredential(""),
		auth.RequirePlatformKey(creds, logger),
		auth.RequireAccessToken(creds, logger),
	)
	g.POST("", handler.Handle)
	g.POST("/:event", handler.Handle)
	e.router = r
	return e
}

func (e *env) post(path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

func authHeaders() map[string]string {
	return map[string]string{auth.PlatformKeyHeader: platformKey, "Authorization": "Bearer " + accessToken}
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, w.Body.String())
	}
	return body
}

func (e *env) nextJob(t *testing.T) queue.Job {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deliveries, _ := e.queue.Consume(ctx)
	select {
	case d := <-deliveries:
		return d.Job
	case <-ctx.Done():
		t.Fatal("no job queued")
	}
	return queue.Job{}
}

func TestWebhookAcceptedAndQueued(t *testing.T) {
	e := newEnv(t)
	w := e.post("/webhooks/easyecom", validPayload, authHeaders())
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	body := decode(t, w)
	if body["status"] != "accepted" {
		t.Fatalf("body = %v", body)
	}
	corr := w.Header().Get(correlation.HeaderName)
	if body["correlation_id"] != corr {
		t.Fatalf("correlation id mismatch: %v vs %s", body["correlation_id"], corr)
	}

	job := e.nextJob(t)
	if job.CorrelationID != corr || job.EventType != webhook.EventOrderCreated || job.Platform != "easyecom" || job.IntegrationID != e.integ.ID.String() {
		t.Fatalf("job = %+v", job)
	}
	var ev webhook.Event
	if err := json.Unmarshal(job.Payload, &ev); err != nil {
		t.Fatalf("job payload is not an event: %v", err)
	}
	if ev.ExternalOrderID != "9876543" || ev.RoutingKey.Value != "12345" || ev.ReferenceCode != "AMZ-1" || ev.IdempotencyKey != "easyecom:ORDER_CREATED:9876543" || ev.ReceivedAt.IsZero() {
		t.Fatalf("event = %+v", ev)
	}
	rec, err := e.store.Get(context.Background(), ev.IdempotencyKey)
	if err != nil || rec.Status != idempotency.StatusAccepted || rec.CorrelationID != corr {
		t.Fatalf("idempotency record = %+v, %v", rec, err)
	}
	if strings.Contains(e.logs.String(), platformKey) || strings.Contains(e.logs.String(), accessToken) {
		t.Fatal("credentials leaked into logs")
	}
}

func TestDuplicateWebhookIsNotQueuedTwice(t *testing.T) {
	e := newEnv(t)
	first := e.post("/webhooks/easyecom", validPayload, authHeaders())
	second := e.post("/webhooks/easyecom", validPayload, authHeaders())
	if first.Code != http.StatusAccepted || second.Code != http.StatusOK {
		t.Fatalf("statuses = %d, %d; want 202 then 200", first.Code, second.Code)
	}
	body := decode(t, second)
	if body["status"] != "duplicate" {
		t.Fatalf("second body = %v", body)
	}
	events := body["events"].([]any)
	if events[0].(map[string]any)["correlation_id"] != first.Header().Get(correlation.HeaderName) {
		t.Fatalf("duplicate should report the original correlation id: %v", events[0])
	}
	if e.queue.Len() != 1 {
		t.Fatalf("queued jobs = %d, want 1", e.queue.Len())
	}
	if !strings.Contains(e.logs.String(), "duplicate webhook ignored") {
		t.Fatal("duplicate not logged")
	}
}

func TestEventTypeFromPathAndBatches(t *testing.T) {
	e := newEnv(t)
	batch := `[{"order_id":1,"warehouse_id":5},{"order_id":2,"warehouse_id":5}]`
	w := e.post("/webhooks/easyecom/order-confirmed", batch, authHeaders())
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	body := decode(t, w)
	events := body["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("events = %v", events)
	}
	firstCorr := events[0].(map[string]any)["correlation_id"].(string)
	secondCorr := events[1].(map[string]any)["correlation_id"].(string)
	if firstCorr != w.Header().Get(correlation.HeaderName) || secondCorr == firstCorr || !strings.HasPrefix(secondCorr, correlation.Prefix) {
		t.Fatalf("batch correlation ids = %s, %s", firstCorr, secondCorr)
	}
	if e.queue.Len() != 2 {
		t.Fatalf("queued = %d, want 2", e.queue.Len())
	}
	if job := e.nextJob(t); job.EventType != webhook.EventOrderConfirmed {
		t.Fatalf("event type = %s", job.EventType)
	}
}

func TestCombinedAccessTokenHeader(t *testing.T) {
	e := newEnv(t)
	w := e.post("/webhooks/easyecom", validPayload, map[string]string{"Access-Token": platformKey + ":" + accessToken})
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
}

func TestWebhookRejections(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		headers    map[string]string
		wantStatus int
		wantReason string
	}{
		{"no credentials", "/webhooks/easyecom", validPayload, nil, http.StatusUnauthorized, "missing platform API key"},
		{"wrong platform for endpoint", "/webhooks/easyecom", validPayload, map[string]string{auth.PlatformKeyHeader: otherKey, "Authorization": "Bearer " + otherToken}, http.StatusForbidden, "may not post easyecom webhooks"},
		{"unknown event segment", "/webhooks/easyecom/order-exploded", validPayload, authHeaders(), http.StatusBadRequest, "unknown event"},
		{"unsupported event", "/webhooks/easyecom/inventory-updated", `{}`, authHeaders(), http.StatusBadRequest, "not supported"},
		{"malformed body", "/webhooks/easyecom", `{"orders": 5}`, authHeaders(), http.StatusBadRequest, "not an EasyEcom order webhook"},
		{"missing warehouse", "/webhooks/easyecom", `[{"order_id":1}]`, authHeaders(), http.StatusBadRequest, "no warehouse_id"},
		{"body too large", "/webhooks/easyecom", "[" + strings.Repeat(`{"order_id":1,"warehouse_id":1},`, 60) + "]", authHeaders(), http.StatusRequestEntityTooLarge, "request body too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			w := e.post(tt.path, tt.body, tt.headers)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tt.wantStatus, w.Body.String())
			}
			body := decode(t, w)
			if reason, _ := body["reason"].(string); !strings.Contains(reason, tt.wantReason) {
				t.Fatalf("reason = %q, want %q", reason, tt.wantReason)
			}
			if e.queue.Len() != 0 {
				t.Fatal("rejected webhook was queued")
			}
		})
	}
}

func TestEmptyPayloadIsAcknowledged(t *testing.T) {
	e := newEnv(t)
	w := e.post("/webhooks/easyecom", `[]`, authHeaders())
	if w.Code != http.StatusOK || decode(t, w)["status"] != "empty" {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
}

func TestQueueOutageReleasesClaimAndReturns503(t *testing.T) {
	e := newEnv(t, func(e *env, _ *webhook.Handler) { e.queue = nil })
	w := e.post("/webhooks/easyecom", validPayload, authHeaders())
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if _, err := e.store.Get(context.Background(), "easyecom:ORDER_CREATED:9876543"); !errors.Is(err, idempotency.ErrNotFound) {
		t.Fatalf("claim should have been released, got %v", err)
	}
	if !strings.Contains(e.logs.String(), "redis down") {
		t.Fatal("outage cause not logged")
	}
}

func TestIdempotencyOutageReturns503(t *testing.T) {
	e := newEnv(t, func(e *env, _ *webhook.Handler) { e.store = nil })
	w := e.post("/webhooks/easyecom", validPayload, authHeaders())
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
}

func TestValidate(t *testing.T) {
	good := webhook.Event{Platform: "easyecom", EventType: webhook.EventOrderCreated, ExternalOrderID: "1", RoutingKey: webhook.RoutingKey{Type: "warehouse_id", Value: "5"}, IdempotencyKey: "k", Payload: []byte(`{}`)}
	if err := webhook.Validate(good); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*webhook.Event)
		want   string
	}{
		{"no platform", func(e *webhook.Event) { e.Platform = "" }, "platform is required"},
		{"no key", func(e *webhook.Event) { e.IdempotencyKey = "" }, "idempotency key is required"},
		{"no payload", func(e *webhook.Event) { e.Payload = nil }, "payload is required"},
		{"no order id", func(e *webhook.Event) { e.ExternalOrderID = "" }, "external order id is required"},
		{"no routing key", func(e *webhook.Event) { e.RoutingKey = webhook.RoutingKey{} }, "routing key is required"},
		{"unknown type", func(e *webhook.Event) { e.EventType = "NOPE" }, "unknown event type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := good
			tt.mutate(&ev)
			err := webhook.Validate(ev)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestEventTypeFromPath(t *testing.T) {
	if got, ok := webhook.EventTypeFromPath(""); !ok || got != webhook.EventOrderCreated {
		t.Fatalf("empty segment = %s, %v", got, ok)
	}
	if got, ok := webhook.EventTypeFromPath("tracking-updated"); !ok || got != webhook.EventTrackingUpdated {
		t.Fatalf("tracking segment = %s, %v", got, ok)
	}
	if _, ok := webhook.EventTypeFromPath("nope"); ok {
		t.Fatal("unknown segment accepted")
	}
}
