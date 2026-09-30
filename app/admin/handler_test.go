package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gluzo/integration-gateway/app/admin"
	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/auth"
	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/httpserver/middleware"
	"github.com/gluzo/integration-gateway/app/intlog"
	"github.com/gluzo/integration-gateway/app/reconcile"
	"github.com/gluzo/integration-gateway/app/workflow"
	"github.com/gluzo/integration-gateway/app/workflowstate"
)

type fakeResumer struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeResumer) Resume(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, id)
	return f.err
}

type env struct {
	router  *gin.Engine
	repo    *workflowstate.MemoryRepository
	resumer *fakeResumer
}

const adminToken = "admin-secret"

func newEnv(t *testing.T) *env {
	t.Helper()
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	ts := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	rec, err := intlog.NewFileRecorder(root, intlog.WithClock(func() time.Time { return ts }))
	if err != nil {
		t.Fatalf("NewFileRecorder: %v", err)
	}
	ctx := context.Background()
	base := intlog.Entry{CorrelationID: "INT-1", Workflow: "ORDER_SYNC", Platform: "easyecom", Integration: "vinculum", ExternalOrderID: "9876543"}
	entries := []intlog.Entry{
		{Timestamp: ts, Action: intlog.ActionWebhookReceived, Status: intlog.StatusSuccess},
		{Timestamp: ts.Add(time.Second), Action: "RESOLVE_INTEGRATION", Status: intlog.StatusSuccess, Attempt: 1},
		{Timestamp: ts.Add(2 * time.Second), Action: "FETCH_ORDER", Status: intlog.StatusSuccess, Attempt: 1, DurationMS: 420},
		{Timestamp: ts.Add(3 * time.Second), Action: "UPDATE_DESTINATION_ORDER", Status: intlog.StatusFailed, Attempt: 1, Error: &apperror.Info{Category: apperror.Timeout, Message: "timeout", Retryable: true}},
		{Timestamp: ts.Add(4 * time.Second), Action: "UPDATE_DESTINATION_ORDER", Status: intlog.StatusSuccess, Attempt: 2},
	}
	for _, e := range entries {
		e.CorrelationID, e.Workflow, e.Platform, e.Integration, e.ExternalOrderID = base.CorrelationID, base.Workflow, base.Platform, base.Integration, base.ExternalOrderID
		rec.Record(ctx, e)
	}
	rec.Record(ctx, intlog.Entry{Timestamp: ts.Add(5 * time.Second), CorrelationID: "INT-2", Platform: "easyecom", ExternalOrderID: "1", Action: intlog.ActionWebhookReceived, Status: intlog.StatusSuccess})
	_ = rec.Close()

	repo := workflowstate.NewMemoryRepository()
	st := workflow.NewState("ORDER_SYNC", event.Event{Platform: "easyecom", EventType: event.OrderCreated, CorrelationID: "INT-1", ExternalOrderID: "9876543", RoutingKey: event.RoutingKey{Type: "warehouse_id", Value: "5"}, IdempotencyKey: "k", Payload: []byte(`{}`)}, "job", ts)
	st.Status = workflow.StatusFailed
	st.Actions = []workflow.ActionRecord{{Name: "RESOLVE_INTEGRATION", Status: workflow.ActionSucceeded, Attempt: 1}, {Name: "FETCH_ORDER", Status: workflow.ActionFailed, Attempt: 3, LastError: &apperror.Info{Category: apperror.ExternalAPI, Message: "503"}}}
	st.CurrentAction, st.LastSuccessfulAction, st.NextAction = 1, "RESOLVE_INTEGRATION", "FETCH_ORDER"
	st.LastError = st.Actions[1].LastError
	st.Route = &workflow.RouteInfo{DestinationPlatform: "vinculum", IntegrationName: "easyecom-vinculum", RouteType: "warehouse_id", RouteValue: "5", DestinationReference: "DEL"}
	st.SetResult(workflow.ResultDestinationOrderID, "SO-1")
	_ = repo.Save(ctx, st)
	done := workflow.NewState("ORDER_SYNC", event.Event{CorrelationID: "INT-3", Payload: []byte(`{}`)}, "job", ts)
	done.Status = workflow.StatusCompleted
	_ = repo.Save(ctx, done)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	resumer := &fakeResumer{}
	h, err := admin.NewHandler(intlog.NewReader(root), repo, resumer, logger)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	r := gin.New()
	r.Use(middleware.Correlation())
	g := r.Group("/admin", auth.RequireAdminToken(adminToken, logger))
	h.Register(g)
	return &env{router: r, repo: repo, resumer: resumer}
}

func (e *env) do(method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

func TestAdminRequiresToken(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/admin/logs", "/admin/logs/search", "/admin/logs/INT-1", "/admin/workflows/INT-1"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		e.router.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s without token: status %d", path, w.Code)
		}
	}
	w := e.do(http.MethodGet, "/admin/logs", map[string]string{"Authorization": "Bearer wrong"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status %d", w.Code)
	}
}

func TestSearchJSON(t *testing.T) {
	e := newEnv(t)
	w := e.do(http.MethodGet, "/admin/logs/search?external_order_id=9876543&date_from=2026-09-01&date_to=2026-09-30", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		Count   int            `json:"count"`
		Entries []intlog.Entry `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != 5 || body.Entries[0].Action != "UPDATE_DESTINATION_ORDER" || body.Entries[0].Attempt != 2 {
		t.Fatalf("search = %+v", body)
	}

	w = e.do(http.MethodGet, "/admin/logs?format=json&errors_only=true&date_from=2026-09-12", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"count":1`) {
		t.Fatalf("errors only: %d %s", w.Code, w.Body.String())
	}
	w = e.do(http.MethodGet, "/admin/logs/search?date_from=2026-13-01", nil)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "date_from") {
		t.Fatalf("bad date: %d %s", w.Code, w.Body.String())
	}
	w = e.do(http.MethodGet, "/admin/logs/search?correlation_id=../etc", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad correlation id: %d", w.Code)
	}
	w = e.do(http.MethodGet, "/admin/logs/search?integration=nobody&date_from=2026-09-01", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"entries":[]`) {
		t.Fatalf("empty result must be an empty array: %d %s", w.Code, w.Body.String())
	}
}

func TestSearchHTML(t *testing.T) {
	e := newEnv(t)
	w := e.do(http.MethodGet, "/admin/logs", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("landing page: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Body.String(), "Available days: 2026-09-12") || w.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("landing page body/headers: %s", w.Body.String())
	}
	w = e.do(http.MethodGet, "/admin/logs?external_order_id=9876543&date_from=2026-09-01", nil)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "UPDATE_DESTINATION_ORDER") || !strings.Contains(body, "✗ FAILED") || !strings.Contains(body, "✓ SUCCESS") {
		t.Fatalf("search page: %d %s", w.Code, body)
	}
	if !strings.Contains(body, `href="/admin/logs/INT-1"`) {
		t.Fatal("search rows must link to the timeline")
	}
}

func TestTimeline(t *testing.T) {
	e := newEnv(t)
	w := e.do(http.MethodGet, "/admin/logs/INT-1?format=json", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	var view struct {
		CorrelationID string          `json:"correlation_id"`
		State         *workflow.State `json:"state"`
		Entries       []intlog.Entry  `json:"entries"`
		CanResume     bool            `json:"can_resume"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(view.Entries) != 5 || view.Entries[0].Action != intlog.ActionWebhookReceived || view.Entries[4].Attempt != 2 {
		t.Fatalf("entries = %+v", view.Entries)
	}
	if view.State == nil || view.State.Status != workflow.StatusFailed || !view.CanResume {
		t.Fatalf("state = %+v canResume = %v", view.State, view.CanResume)
	}

	w = e.do(http.MethodGet, "/admin/logs/INT-1", nil)
	body := w.Body.String()
	for _, want := range []string{"FAILED", "Resume from FETCH_ORDER", "✓ RESOLVE_INTEGRATION", "✗ UPDATE_DESTINATION_ORDER", "SO-1", "warehouse_id=5"} {
		if !strings.Contains(body, want) {
			t.Errorf("timeline page lacks %q", want)
		}
	}

	w = e.do(http.MethodGet, "/admin/logs/INT-2", map[string]string{"Accept": "application/json"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"state":null`) && strings.Contains(w.Body.String(), `"state":{`) {
		t.Fatalf("entries without state: %d %s", w.Code, w.Body.String())
	}
	w = e.do(http.MethodGet, "/admin/logs/INT-none", nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", w.Code)
	}
	w = e.do(http.MethodGet, "/admin/logs/bad%20id", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid id: %d", w.Code)
	}
}

func TestWorkflowStateAndResume(t *testing.T) {
	e := newEnv(t)
	w := e.do(http.MethodGet, "/admin/workflows/INT-1", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"FAILED"`) {
		t.Fatalf("state: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(http.MethodGet, "/admin/workflows/INT-none", nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown state: %d", w.Code)
	}

	w = e.do(http.MethodPost, "/admin/workflows/INT-1/resume", nil)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), "resume_started") {
		t.Fatalf("resume: %d %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		e.resumer.mu.Lock()
		n := len(e.resumer.calls)
		e.resumer.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("resumer was not called")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if w := e.do(http.MethodPost, "/admin/workflows/INT-3/resume", nil); w.Code != http.StatusConflict {
		t.Fatalf("resume finished run: %d", w.Code)
	}
	if w := e.do(http.MethodPost, "/admin/workflows/INT-none/resume", nil); w.Code != http.StatusNotFound {
		t.Fatalf("resume unknown run: %d", w.Code)
	}
	w = e.do(http.MethodPost, "/admin/workflows/INT-1/resume", map[string]string{"Accept": "text/html"})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/logs/INT-1" {
		t.Fatalf("browser resume should redirect: %d %s", w.Code, w.Header().Get("Location"))
	}
}

func TestResumeUnavailableWithoutWorker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := admin.NewHandler(intlog.NewReader(t.TempDir()), workflowstate.NewMemoryRepository(), nil, logger)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	r := gin.New()
	h.Register(r.Group("/admin"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/workflows/INT-1/resume", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", w.Code)
	}
	if _, err := admin.NewHandler(nil, nil, nil, nil); err == nil {
		t.Fatal("nil reader accepted")
	}
	_ = errors.New // keep errors imported for future assertions
}

// The Phase 7 exit criterion, at the layer the criterion names: a stuck order
// must actually appear in the admin viewer.
func TestReconcileViewListsStuckRuns(t *testing.T) {
	ctx := context.Background()
	repo := workflowstate.NewMemoryRepository()
	stuck := workflow.NewState("ORDER_SYNC", event.Event{
		CorrelationID: "INT-STUCK", ExternalOrderID: "9876543", Payload: []byte(`{}`),
	}, "job", time.Now().Add(-2*time.Hour))
	stuck.Status = workflow.StatusFailed
	stuck.NextAction = "SUBMIT_VENDOR_ORDER"
	stuck.UpdatedAt = time.Now().Add(-2 * time.Hour)
	stuck.LastError = &apperror.Info{
		Category: apperror.ExternalAPI, Message: "vinculum rejected the request",
		ExternalCode: "120", ExternalMessage: "location is closed for dispatch",
	}
	stuck.Route = &workflow.RouteInfo{DestinationPlatform: "vinculum", IntegrationName: "easyecom-vinculum"}
	if err := repo.Save(ctx, stuck); err != nil {
		t.Fatalf("Save: %v", err)
	}

	scanner, err := reconcile.NewScanner(repo, reconcile.Options{Threshold: 30 * time.Minute, MaxAutoResumes: 3})
	if err != nil {
		t.Fatalf("NewScanner: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := admin.NewHandler(intlog.NewReader(t.TempDir()), repo, nil, logger, admin.WithScanner(scanner))
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	r := gin.New()
	r.Use(middleware.Correlation())
	h.Register(r.Group("/admin", auth.RequireAdminToken(adminToken, logger)))

	req := httptest.NewRequest(http.MethodGet, "/admin/reconcile", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"INT-STUCK",
		"SUBMIT_VENDOR_ORDER",
		"PERMANENT_FAILURE",
		// The vendor's own words reach the page: "the order did not go
		// through" is not actionable, this is.
		"location is closed for dispatch",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not mention %q", want)
		}
	}
}

// Without a scanner the route must say so rather than render an empty page,
// which would read as "nothing is stuck".
func TestReconcileWithoutAScannerIsUnavailable(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := admin.NewHandler(intlog.NewReader(t.TempDir()), workflowstate.NewMemoryRepository(), nil, logger)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	r := gin.New()
	r.Use(middleware.Correlation())
	h.Register(r.Group("/admin", auth.RequireAdminToken(adminToken, logger)))

	req := httptest.NewRequest(http.MethodGet, "/admin/reconcile", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
