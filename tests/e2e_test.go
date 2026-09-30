package tests

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/gluzo/integration-gateway/app/admin"
	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/auth"
	"github.com/gluzo/integration-gateway/app/domain/order"
	"github.com/gluzo/integration-gateway/app/event"
	"github.com/gluzo/integration-gateway/app/health"
	"github.com/gluzo/integration-gateway/app/httpclient"
	"github.com/gluzo/integration-gateway/app/httpserver"
	"github.com/gluzo/integration-gateway/app/idempotency"
	"github.com/gluzo/integration-gateway/app/integrations/easyecom"
	"github.com/gluzo/integration-gateway/app/intlog"
	"github.com/gluzo/integration-gateway/app/queue/redisqueue"
	"github.com/gluzo/integration-gateway/app/routing"
	"github.com/gluzo/integration-gateway/app/vendor"
	"github.com/gluzo/integration-gateway/app/webhook"
	"github.com/gluzo/integration-gateway/app/worker"
	"github.com/gluzo/integration-gateway/app/workflow"
	"github.com/gluzo/integration-gateway/app/workflow/ordersync"
	"github.com/gluzo/integration-gateway/app/workflowstate"
)

const (
	e2eOrderID   = "424242"
	e2eWarehouse = "777"
	e2eLocation  = "BLR"
	// The origin-side location the vendor's stock is written under. Separate
	// from the vendor-side one because they name different ends of the route.
	e2eOriginLocation = "bcpl-location-key"
	e2eVendor         = "vinculum"
	e2eAdminToken     = "admin-e2e-token"
	e2eEasyJWT        = "e2e-easyecom-jwt-token"
	e2eEasyAPIKey     = "e2e-easyecom-api-key"
	e2eWebhookOrders  = `[{"order_id":424242,"invoice_id":"INV-E2E","reference_code":"REF-E2E","warehouse_id":777,"order_status":"Pending","customer_name":"Asha Verma","contact_num":"9876501234","address_line_1":"12 MG Road","city":"Pune","state":"Maharashtra","pin_code":"411001","total_amount":"250.00","payment_mode":"COD","order_items":[{"suborder_id":1,"sku":"BCPL-E2E","suborder_quantity":1,"selling_price":250}]}]`
)

// fakeEasyEcomAPI answers the calls the origin adapter makes.
type fakeEasyEcomAPI struct {
	srv        *httptest.Server
	orderCalls atomic.Int32
}

func newFakeEasyEcomAPI(t *testing.T) *fakeEasyEcomAPI {
	f := &fakeEasyEcomAPI{}
	mux := http.NewServeMux()
	authed := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("X-API-Key") != e2eEasyAPIKey || r.Header.Get("Authorization") != "Bearer "+e2eEasyJWT {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("/orders/V2/getOrderDetails", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		f.orderCalls.Add(1)
		if r.URL.Query().Get("invoice_id") != "INV-E2E" {
			_, _ = w.Write([]byte(`{"code":200,"message":"Successful","data":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"message":"Successful","data":` + e2eWebhookOrders + `}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// stubVendor stands in for a fulfilment partner until the Vinculum adapter
// exists. It implements all three vendor roles so that the registry's
// capability discovery is exercised, and fails order submission as many
// times as failSubmits says.
//
// Phase 5 replaces this with a fake Vinculum HTTP server, which restores
// transport-level coverage of the real adapter.
type stubVendor struct {
	failSubmits atomic.Int32
	submitCalls atomic.Int32
	created     atomic.Bool
	location    atomic.Value
}

func (s *stubVendor) Platform() string { return e2eVendor }

func (s *stubVendor) PrepareOrder(_ context.Context, o order.Order, route vendor.Route) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"orderNo":       o.ExternalID,
		"orderLocation": route.VendorReference,
		"orderAmount":   []map[string]any{{"lineno": 1, "sku": o.Items[0].SKU, "orderQty": o.Items[0].Quantity}},
	})
}

func (s *stubVendor) SubmitOrder(_ context.Context, prepared json.RawMessage, o order.Order, route vendor.Route) (vendor.OrderAck, error) {
	s.submitCalls.Add(1)
	s.location.Store(route.VendorReference)
	if s.failSubmits.Load() > 0 {
		s.failSubmits.Add(-1)
		err := apperror.New(apperror.ExternalAPI, "vendor unavailable")
		err.Retryable = true
		return vendor.OrderAck{}, err
	}
	var doc struct {
		OrderNo string `json:"orderNo"`
	}
	_ = json.Unmarshal(prepared, &doc)
	s.created.Store(true)
	return vendor.OrderAck{VendorOrderID: doc.OrderNo, Created: true}, nil
}

func (s *stubVendor) FetchStock(context.Context, vendor.Route, vendor.StockCursor) (vendor.StockPage, error) {
	return vendor.StockPage{}, nil
}

func (s *stubVendor) FetchShipments(context.Context, vendor.Route, vendor.Window) (vendor.ShipmentPage, error) {
	return vendor.ShipmentPage{}, nil
}

// ensurePlatform creates the named platform or, when it already exists in a
// shared database, rotates its key so the test has a usable credential.
func ensurePlatform(t *testing.T, creds *auth.Store, name, platformType string) string {
	t.Helper()
	ctx := context.Background()
	_, key, err := creds.CreatePlatform(ctx, name, platformType)
	if errors.Is(err, auth.ErrAlreadyExists) {
		if platformType == auth.PlatformTypeDestination {
			return ""
		}
		key, err = creds.RotatePlatformKey(ctx, name)
	}
	if err != nil {
		t.Fatalf("ensure platform %s: %v", name, err)
	}
	return key
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func noSleep(context.Context, time.Duration) error { return nil }

func errString(err error) string {
	if err == nil {
		return "nil"
	}
	return err.Error()
}

// TestEndToEndOrderSync exercises the definition-of-done scenario: an
// EasyEcom webhook is authenticated, deduplicated and queued; the worker
// routes, fetches, maps and submits the order to the fulfilment vendor; the
// vendor fails, the action retries and exhausts its budget; the process
// "restarts"; recovery resumes from the failed action and the run completes;
// the whole trace is visible in the log viewer; and retention prunes old
// logs.
func TestEndToEndOrderSync(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if os.Getenv("E2E_DEBUG") != "" {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}

	// Provisioning, as an operator would do with gatewayctl.
	creds := auth.NewStore(pool)
	platformKey := ensurePlatform(t, creds, easyecom.PlatformName, auth.PlatformTypeSource)
	ensurePlatform(t, creds, e2eVendor, auth.PlatformTypeDestination)
	integ, err := creds.CreateIntegration(ctx, "e2e-"+uuid.NewString()[:8], easyecom.PlatformName, e2eVendor)
	if err != nil {
		t.Fatalf("CreateIntegration: %v", err)
	}
	_, accessToken, err := creds.IssueToken(ctx, integ.Name, "e2e webhook", 0)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	routes := routing.NewStore(pool)
	if _, err := routes.AddRoute(ctx, integ.Name, routing.TypeWarehouse, e2eWarehouse, e2eLocation, e2eOriginLocation); err != nil {
		t.Fatalf("AddRoute: %v", err)
	}

	// Infrastructure: embedded PostgreSQL (TestMain), miniredis, temp storage.
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	jobQueue, err := redisqueue.New(rdb, redisqueue.Options{Stream: "e2e:jobs", Group: "e2e", BlockTimeout: 100 * time.Millisecond, ClaimMinIdle: time.Minute}, logger)
	if err != nil {
		t.Fatalf("redisqueue.New: %v", err)
	}
	logRoot, stateRoot := t.TempDir(), t.TempDir()
	recorder, err := intlog.NewFileRecorder(logRoot, intlog.WithLogger(logger))
	if err != nil {
		t.Fatalf("NewFileRecorder: %v", err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	stateRepo, err := workflowstate.NewFileRepository(stateRoot)
	if err != nil {
		t.Fatalf("NewFileRepository: %v", err)
	}
	idem := idempotency.NewPostgresStore(pool)

	// External platforms.
	easyAPI := newFakeEasyEcomAPI(t)
	easyClient, err := easyecom.NewClient(easyecom.Config{BaseURL: easyAPI.srv.URL, APIKey: e2eEasyAPIKey, JWTToken: e2eEasyJWT},
		easyecom.WithLogger(logger), easyecom.WithHTTPOptions(httpclient.WithSleep(noSleep)))
	if err != nil {
		t.Fatalf("easyecom.NewClient: %v", err)
	}
	vnd := &stubVendor{}
	vendors := vendor.NewRegistry()
	if err := vendors.Register(vnd); err != nil {
		t.Fatalf("register vendor: %v", err)
	}

	// Workflow with a small submit budget so the outage exhausts it quickly.
	policies := ordersync.DefaultPolicies()
	for _, p := range []*workflow.Policy{&policies.Resolve, &policies.Fetch, &policies.Map, &policies.Submit} {
		p.BaseDelay, p.MaxDelay = time.Millisecond, time.Millisecond
	}
	policies.Submit.MaxAttempts = 2
	orderSync, err := ordersync.New(ordersync.Dependencies{
		Resolver: routes,
		Origins:  map[string]vendor.Origin{easyecom.PlatformName: easyecom.NewSource(easyClient, logger)},
		Vendors:  vendors,
		Policies: &policies,
		Logger:   logger,
	})
	if err != nil {
		t.Fatalf("ordersync.New: %v", err)
	}
	registry := workflow.NewRegistry()
	if err := registry.Register(orderSync, event.OrderCreated, event.OrderConfirmed); err != nil {
		t.Fatalf("Register: %v", err)
	}
	executor := workflow.NewExecutor(stateRepo, workflow.WithRecorder(recorder), workflow.WithLogger(logger), workflow.WithSleep(noSleep))
	newWorker := func() *worker.Worker {
		w, err := worker.New(worker.Dependencies{
			Queue: jobQueue, Registry: registry, Executor: executor, States: stateRepo, Idempotency: idem, Recorder: recorder, Logger: logger,
		}, worker.Config{Concurrency: 2, MaxAutoResumes: 3, RecoveryInterval: time.Hour, RetryFailedAfter: time.Millisecond})
		if err != nil {
			t.Fatalf("worker.New: %v", err)
		}
		return w
	}

	// HTTP surface: webhook intake plus the admin viewer.
	adminHandler, err := admin.NewHandler(intlog.NewReader(logRoot), stateRepo, nil, logger)
	if err != nil {
		t.Fatalf("admin.NewHandler: %v", err)
	}
	router := httpserver.NewRouter(httpserver.Dependencies{
		Logger:       logger,
		Health:       health.NewHandler("svc", "e2e", time.Second, logger),
		MaxBodyBytes: 1 << 20,
		PlatformKeys: creds,
		AccessTokens: creds,
		Webhooks:     []*webhook.Handler{webhook.NewHandler(easyecom.WebhookParser{}, idem, jobQueue, logger, webhook.WithRecorder(recorder))},
		Admin:        adminHandler,
		AdminToken:   e2eAdminToken,
	})
	post := func(path, body string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	get := func(path string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// 1. EasyEcom sends ORDER_CREATED, twice (a redelivery).
	vnd.failSubmits.Store(2) // the vendor is down for the first two attempts
	combined := map[string]string{auth.CombinedCredentialHeader: platformKey + ":" + accessToken}
	first := post("/webhooks/easyecom", e2eWebhookOrders, combined)
	if first.Code != http.StatusAccepted {
		t.Fatalf("webhook: %d %s", first.Code, first.Body.String())
	}
	var accepted struct {
		CorrelationID string `json:"correlation_id"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &accepted)
	corr := accepted.CorrelationID
	if !strings.HasPrefix(corr, "INT-") {
		t.Fatalf("no correlation id in %s", first.Body.String())
	}
	if dup := post("/webhooks/easyecom", e2eWebhookOrders, combined); dup.Code != http.StatusOK || !strings.Contains(dup.Body.String(), "duplicate") {
		t.Fatalf("duplicate webhook: %d %s", dup.Code, dup.Body.String())
	}

	// 2. The worker runs the workflow; the vendor outage exhausts the submit
	//    budget and the run is persisted as FAILED.
	ctx1, cancel1 := context.WithCancel(ctx)
	worker1 := newWorker()
	done1 := make(chan error, 1)
	go func() { done1 <- worker1.Run(ctx1) }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		st, err := stateRepo.Get(ctx, corr)
		if err == nil && st.Status == workflow.StatusFailed {
			break
		}
		if time.Now().After(deadline) {
			status := "no state (" + errString(err) + ")"
			if err == nil {
				status = string(st.Status) + " at " + st.NextAction
				if st.LastError != nil {
					status += " last error " + st.LastError.String()
				}
			}
			cancel1()
			t.Fatalf("timed out waiting for the run to fail; current: %s; worker: %v", status, <-done1)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel1() // simulate the application stopping
	if err := <-done1; err != nil {
		t.Fatalf("worker 1 returned %v", err)
	}

	failed, _ := stateRepo.Get(ctx, corr)
	if failed.NextAction != ordersync.ActionSubmitVendorOrder || failed.LastSuccessfulAction != ordersync.ActionMapOrder {
		t.Fatalf("failed run position: next=%s last=%s", failed.NextAction, failed.LastSuccessfulAction)
	}
	if failed.Actions[3].Attempt != 2 || failed.LastError == nil || !failed.LastError.Retryable {
		t.Fatalf("failed action record: %+v lastError=%+v", failed.Actions[3], failed.LastError)
	}
	if failed.Order == nil || failed.Order.ExternalID != e2eOrderID || failed.Route == nil || failed.Route.DestinationReference != e2eLocation {
		t.Fatalf("state data: order=%+v route=%+v", failed.Order, failed.Route)
	}
	if rec, err := idem.Get(ctx, "easyecom:ORDER_CREATED:"+e2eOrderID); err != nil || rec.Status != idempotency.StatusFailed {
		t.Fatalf("idempotency after failure: %+v %v", rec, err)
	}
	if vnd.submitCalls.Load() != 2 || easyAPI.orderCalls.Load() != 1 {
		t.Fatalf("calls before restart: submit=%d fetch=%d", vnd.submitCalls.Load(), easyAPI.orderCalls.Load())
	}

	// 3. The application restarts: startup recovery resumes the run from the
	//    failed action; the vendor is back.
	worker2 := newWorker()
	worker2.Recover(ctx, true)

	completed, err := stateRepo.Get(ctx, corr)
	if err != nil {
		t.Fatalf("state after recovery: %v", err)
	}
	if completed.Status != workflow.StatusCompleted || completed.ResumeCount != 1 {
		t.Fatalf("state after recovery: status=%s resumes=%d lastError=%+v", completed.Status, completed.ResumeCount, completed.LastError)
	}
	if completed.Actions[3].Status != workflow.ActionSucceeded || completed.Actions[3].Attempt != 1 {
		t.Fatalf("submit record after resume: %+v", completed.Actions[3])
	}
	if completed.Result(workflow.ResultDestinationOrderID) != e2eOrderID || completed.Result(ordersync.ResultVendorOrderCreated) != "true" {
		t.Fatalf("results: %v", completed.Results)
	}
	if vnd.submitCalls.Load() != 3 || easyAPI.orderCalls.Load() != 1 {
		t.Fatalf("calls after restart: submit=%d fetch=%d (resume must not replay FETCH_ORDER)", vnd.submitCalls.Load(), easyAPI.orderCalls.Load())
	}
	if got, _ := vnd.location.Load().(string); got != e2eLocation {
		t.Fatalf("vendor location = %q, want the route's vendor reference", got)
	}
	if rec, err := idem.Get(ctx, "easyecom:ORDER_CREATED:"+e2eOrderID); err != nil || rec.Status != idempotency.StatusCompleted {
		t.Fatalf("idempotency after completion: %+v %v", rec, err)
	}
	if _, err := os.Stat(filepath.Join(stateRoot, workflowstate.CompletedDir, corr+".json")); err != nil {
		t.Fatalf("completed state file: %v", err)
	}
	if active, _ := stateRepo.ListActive(ctx); len(active) != 0 {
		t.Fatalf("active runs after completion: %v", active)
	}

	// 4. The complete trace is visible through the protected log viewer.
	if w := get("/admin/logs/"+corr, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("admin without token: %d", w.Code)
	}
	adminHeaders := map[string]string{"Authorization": "Bearer " + e2eAdminToken}
	tl := get("/admin/logs/"+corr+"?format=json", adminHeaders)
	if tl.Code != http.StatusOK {
		t.Fatalf("timeline: %d %s", tl.Code, tl.Body.String())
	}
	var timeline struct {
		Entries []intlog.Entry  `json:"entries"`
		State   *workflow.State `json:"state"`
	}
	if err := json.Unmarshal(tl.Body.Bytes(), &timeline); err != nil {
		t.Fatalf("timeline decode: %v", err)
	}
	var steps []string
	for _, e := range timeline.Entries {
		steps = append(steps, e.Action+":"+e.Status)
	}
	trace := strings.Join(steps, " ")
	for _, want := range []string{
		"WEBHOOK_RECEIVED:SUCCESS", "WEBHOOK_RECEIVED:DUPLICATE", "WORKFLOW_STARTED:STARTED",
		"RESOLVE_INTEGRATION:SUCCESS", "FETCH_ORDER:SUCCESS", "MAP_ORDER:SUCCESS",
		"SUBMIT_VENDOR_ORDER:FAILED SUBMIT_VENDOR_ORDER:FAILED WORKFLOW_FAILED:FAILED",
		"WORKFLOW_RESUMED:STARTED SUBMIT_VENDOR_ORDER:SUCCESS", "WORKFLOW_COMPLETED:SUCCESS",
	} {
		if !strings.Contains(trace, want) {
			t.Errorf("trace lacks %q:\n%s", want, trace)
		}
	}
	if timeline.State == nil || timeline.State.Status != workflow.StatusCompleted {
		t.Fatalf("timeline state: %+v", timeline.State)
	}
	search := get("/admin/logs/search?external_order_id="+e2eOrderID+"&status=FAILED", adminHeaders)
	if search.Code != http.StatusOK || !strings.Contains(search.Body.String(), `"count":3`) {
		t.Fatalf("search: %d %s", search.Code, search.Body.String())
	}
	page := get("/admin/logs/"+corr, adminHeaders)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "COMPLETED") || !strings.Contains(page.Body.String(), "✗ SUBMIT_VENDOR_ORDER") {
		t.Fatalf("timeline page: %d", page.Code)
	}

	// 5. The JSONL files are valid, append-only records that carry no secrets.
	logFile := filepath.Join(intlog.DayDir(logRoot, time.Now()), intlog.IntegrationFile)
	f, err := os.Open(logFile)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()
	lines := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines++
		if !json.Valid(scanner.Bytes()) {
			t.Fatalf("invalid JSONL line: %s", scanner.Text())
		}
		for _, secret := range []string{platformKey, accessToken, e2eEasyJWT, e2eEasyAPIKey, e2eAdminToken} {
			if strings.Contains(scanner.Text(), secret) {
				t.Fatalf("secret written to the integration log: %s", scanner.Text())
			}
		}
	}
	if lines < len(timeline.Entries) {
		t.Fatalf("log has %d lines but the timeline shows %d entries", lines, len(timeline.Entries))
	}
	if errFile, err := os.ReadFile(filepath.Join(intlog.DayDir(logRoot, time.Now()), intlog.ErrorFile)); err != nil || strings.Count(string(errFile), "\n") != 3 {
		t.Fatalf("error.jsonl should hold the three failed entries: %v\n%s", err, errFile)
	}

	// 6. Retention removes expired days and leaves today alone.
	oldDay := filepath.Join(logRoot, "2020-01-01")
	if err := os.MkdirAll(oldDay, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_ = os.WriteFile(filepath.Join(oldDay, intlog.IntegrationFile), []byte("{}\n"), 0o640)
	retention, err := intlog.NewRetention(logRoot, 30, logger)
	if err != nil {
		t.Fatalf("NewRetention: %v", err)
	}
	deleted, err := retention.RunOnce(ctx)
	if err != nil || len(deleted) != 1 || deleted[0] != "2020-01-01" {
		t.Fatalf("retention: %v %v", deleted, err)
	}
	if _, err := os.Stat(logFile); err != nil {
		t.Fatalf("today's log was removed: %v", err)
	}
}
