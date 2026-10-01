// Command devstack runs the whole gateway on a developer machine that has no
// Docker, no PostgreSQL and no Redis installed, and then drives one order
// through it end to end so the pipeline can be watched rather than inferred
// from a passing test.
//
// It starts an embedded PostgreSQL and an in-process Redis, stands up stub
// EasyEcom and Vinculum servers, provisions the credentials, route and SKU
// mapping an operator would create with gatewayctl, launches the real
// cmd/server binary against all of it, posts one webhook and waits for the
// order to arrive at the vendor.
//
// The stub vendors are the point, not a shortcut. docs/blockers.md records
// that several request field names are still unverified (B17 above all) and
// that Phase 5 must not run against BCPL production; a dev stack that could
// reach a real warehouse would be a way to ship a parcel by accident. Every
// outbound base URL therefore points at a server this process owns, and the
// generated environment file is passed through ENV_FILE so that a .env
// holding real credentials is not read at all.
//
// Nothing here is imported by the server. It is a developer tool that stops
// existing the moment it is interrupted: the database, the queue and the
// storage directories all live under one temporary directory that is removed
// on the way out.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/alicebob/miniredis/v2"
	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"github.com/gluzo/integration-gateway/app/auth"
	"github.com/gluzo/integration-gateway/app/database/migrations"
	"github.com/gluzo/integration-gateway/app/database/postgres"
	"github.com/gluzo/integration-gateway/app/integrations/easyecom"
	"github.com/gluzo/integration-gateway/app/integrations/vinculum"
	"github.com/gluzo/integration-gateway/app/routing"
	"github.com/gluzo/integration-gateway/app/skumap"
)

// The demonstration order. The identifiers are fixed so that a second run
// shows the duplicate path rather than inventing new work every time.
const (
	devWarehouse      = "777"
	devVendorLocation = "BLR"
	devOriginLocation = "dev-location-key"
	devGluzoSKU       = "GLZ-DEV-1"
	devVendorSKU      = "VIN-DEV-1"
	devInvoiceID      = "INV-DEV"
	devOrderID        = "424242"

	devVinculumOwner = "dev-vinculum-owner"
	devVinculumKey   = "dev-vinculum-key"
	devEasyAPIKey    = "dev-easyecom-api-key"
	devEasyJWT       = "dev-easyecom-jwt"
	devAdminToken    = "dev-admin-token"

	// serverPort is fixed rather than free so that the printed URLs are the
	// ones a developer already has in their browser history.
	serverPort = "8080"
)

// devOrder is the order body the stub EasyEcom returns and the webhook
// carries. One SKU, one unit, enough address to map.
const devOrder = `[{"order_id":424242,"invoice_id":"INV-DEV","reference_code":"REF-DEV",` +
	`"warehouse_id":777,"order_status":"Pending","customer_name":"Asha Verma",` +
	`"contact_num":"9876501234","address_line_1":"12 MG Road","city":"Pune",` +
	`"state":"Maharashtra","pin_code":"411001","total_amount":"250.00","payment_mode":"COD",` +
	`"order_items":[{"suborder_id":1,"sku":"GLZ-DEV-1","suborder_quantity":1,"selling_price":250}]}]`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "devstack:", err)
		os.Exit(1)
	}
}

func run() error {
	// -once tears the stack down as soon as the order has arrived, which is
	// what a smoke test wants; the default leaves it running to be poked at.
	once := flag.Bool("once", false, "exit as soon as the demonstration order reaches the vendor")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root, err := os.MkdirTemp("", "gluzo-devstack-")
	if err != nil {
		return err
	}
	defer func() {
		// Best effort: a PostgreSQL that has only just stopped can still
		// hold a file open on Windows, and a failure to remove a temporary
		// directory is not worth a non-zero exit.
		_ = os.RemoveAll(root)
	}()

	step(1, "starting embedded PostgreSQL")
	dbURL, stopPG, err := startPostgres(root)
	if err != nil {
		return err
	}
	defer stopPG()
	say("   postgres  %s", dbURL)

	step(2, "starting in-process Redis")
	mr, err := miniredis.Run()
	if err != nil {
		return fmt.Errorf("start miniredis: %w", err)
	}
	defer mr.Close()
	redisURL := "redis://" + mr.Addr() + "/0"
	say("   redis     %s", redisURL)

	step(3, "starting stub EasyEcom and Vinculum")
	easyStub, err := startEasyEcomStub()
	if err != nil {
		return err
	}
	defer easyStub.close()
	vinStub, err := startVinculumStub()
	if err != nil {
		return err
	}
	defer vinStub.close()
	say("   easyecom  %s", easyStub.url)
	say("   vinculum  %s  (nothing leaves this machine)", vinStub.url)

	step(4, "provisioning credentials, route and SKU mapping")
	creds, err := provision(ctx, dbURL)
	if err != nil {
		return err
	}
	say("   integration %s", creds.integration)
	say("   route       warehouse_id=%s -> %s", devWarehouse, devVendorLocation)
	say("   sku map     %s -> %s", devGluzoSKU, devVendorSKU)

	step(5, "building and starting the gateway")
	envPath := filepath.Join(root, "devstack.env")
	if err := writeEnvFile(envPath, root, dbURL, redisURL, easyStub.url, vinStub.url); err != nil {
		return err
	}
	binary, err := buildServer(ctx, root)
	if err != nil {
		return err
	}
	server, err := startServer(ctx, binary, envPath)
	if err != nil {
		return err
	}
	defer server.stop()

	base := "http://127.0.0.1:" + serverPort
	if err := waitForReady(ctx, base); err != nil {
		return err
	}
	say("   gateway   %s  (pid %d)", base, server.cmd.Process.Pid)

	step(6, "posting one EasyEcom order webhook")
	corr, err := postWebhook(ctx, base, creds)
	if err != nil {
		return err
	}
	say("   accepted, correlation id %s", corr)

	step(7, "waiting for the order to reach the vendor")
	if err := waitFor(ctx, 60*time.Second, func() bool { return vinStub.count() > 0 }); err != nil {
		return fmt.Errorf("the order never reached the vendor: %w", err)
	}
	doc, _ := vinStub.order(devOrderID)
	pretty, _ := json.MarshalIndent(doc, "   ", "  ")

	fmt.Println()
	fmt.Println("   ✓ the vendor received this order:")
	fmt.Println()
	fmt.Println("  ", string(pretty))
	fmt.Println()
	if *once {
		say("   -once given: tearing the stack down")
		return nil
	}
	banner(base, creds, corr)

	<-ctx.Done()
	fmt.Println()
	say("shutting down")
	return nil
}

// --- infrastructure ---------------------------------------------------------

func startPostgres(root string) (string, func(), error) {
	port, err := freePort()
	if err != nil {
		return "", nil, err
	}
	cfg := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(uint32(port)).
		RuntimePath(filepath.Join(root, "pg-runtime")).
		DataPath(filepath.Join(root, "pg-data")).
		StartTimeout(90 * time.Second).
		Logger(io.Discard)
	db := embeddedpostgres.NewDatabase(cfg)
	if err := db.Start(); err != nil {
		return "", nil, fmt.Errorf("start embedded postgres (the binaries download once, on first run): %w", err)
	}
	url := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
	return url, func() { _ = db.Stop() }, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// stub is a local HTTP server standing in for an external platform.
type stub struct {
	url    string
	server *http.Server

	mu     sync.Mutex
	orders map[string]map[string]any
	calls  atomic.Int32
}

func (s *stub) close() { _ = s.server.Close() }

func (s *stub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.orders)
}

func (s *stub) order(no string) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.orders[no]
	return doc, ok
}

func listen(mux *http.ServeMux) (*stub, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &stub{
		url:    "http://" + l.Addr().String(),
		server: &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second},
		orders: map[string]map[string]any{},
	}
	go func() { _ = s.server.Serve(l) }()
	return s, nil
}

// startEasyEcomStub answers the one read the order workflow makes.
func startEasyEcomStub() (*stub, error) {
	mux := http.NewServeMux()
	var s *stub
	mux.HandleFunc("/orders/V2/getOrderDetails", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != devEasyAPIKey || r.Header.Get("Authorization") != "Bearer "+devEasyJWT {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("invoice_id") != devInvoiceID {
			_, _ = w.Write([]byte(`{"code":200,"message":"Successful","data":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"message":"Successful","data":` + devOrder + `}`))
	})
	var err error
	s, err = listen(mux)
	return s, err
}

// startVinculumStub accepts one order per order number and rejects a repeat,
// which is the response the gateway's idempotency rests on.
func startVinculumStub() (*stub, error) {
	mux := http.NewServeMux()
	var s *stub
	mux.HandleFunc("/RestWS/api/eretail/v4/order/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ApiOwner") != devVinculumOwner || r.Header.Get("ApiKey") != devVinculumKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		orderNo, _ := body["orderNo"].(string)

		s.mu.Lock()
		_, already := s.orders[orderNo]
		if !already {
			s.orders[orderNo] = body
		}
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if already {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"responseCode":    101,
				"responseMessage": "Duplicate order no " + orderNo + " already exists",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"responseCode": 0, "responseMessage": "SUCCESS", "orderNo": orderNo,
		})
	})
	var err error
	s, err = listen(mux)
	return s, err
}

// --- provisioning -----------------------------------------------------------

// credentials are what an operator would note down after running gatewayctl.
type credentials struct {
	integration string
	platformKey string
	accessToken string
}

// header renders the pair as the single webhook credential header.
func (c credentials) header() string { return c.platformKey + ":" + c.accessToken }

func provision(ctx context.Context, dbURL string) (credentials, error) {
	var out credentials

	pool, err := postgres.Connect(ctx, postgres.Config{URL: dbURL, MaxConns: 5, ConnectTimeout: 15 * time.Second})
	if err != nil {
		return out, err
	}
	defer pool.Close()

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := migrations.Apply(ctx, pool, migrations.Files(), quiet); err != nil {
		return out, fmt.Errorf("apply migrations: %w", err)
	}

	store := auth.NewStore(pool)
	_, platformKey, err := store.CreatePlatform(ctx, easyecom.PlatformName, auth.PlatformTypeSource)
	if err != nil {
		return out, fmt.Errorf("create source platform: %w", err)
	}
	if _, _, err := store.CreatePlatform(ctx, vinculum.PlatformName, auth.PlatformTypeDestination); err != nil {
		return out, fmt.Errorf("create vendor platform: %w", err)
	}
	integration, err := store.CreateIntegration(ctx, "devstack", easyecom.PlatformName, vinculum.PlatformName)
	if err != nil {
		return out, fmt.Errorf("create integration: %w", err)
	}
	_, accessToken, err := store.IssueToken(ctx, integration.Name, "devstack webhook", 0)
	if err != nil {
		return out, fmt.Errorf("issue token: %w", err)
	}
	if _, err := routing.NewStore(pool).AddRoute(ctx, integration.Name,
		routing.TypeWarehouse, devWarehouse, devVendorLocation, devOriginLocation); err != nil {
		return out, fmt.Errorf("add route: %w", err)
	}
	if _, err := skumap.NewStore(pool).Add(ctx, integration.Name, devGluzoSKU, devVendorSKU, 0); err != nil {
		return out, fmt.Errorf("add sku mapping: %w", err)
	}

	return credentials{integration: integration.Name, platformKey: platformKey, accessToken: accessToken}, nil
}

// --- the server -------------------------------------------------------------

// writeEnvFile generates a complete environment for the run. It is complete
// on purpose: the server is started with ENV_FILE pointing here so that a
// developer's own .env — which may hold real BCPL or EasyEcom credentials —
// is never read into a process wired to stubs.
func writeEnvFile(path, root, dbURL, redisURL, easyURL, vinURL string) error {
	env := [][2]string{
		{"APP_ENV", "development"},
		{"APP_PORT", serverPort},
		{"LOG_LEVEL", "info"},
		{"APP_SHUTDOWN_TIMEOUT", "15s"},

		{"HTTP_READ_HEADER_TIMEOUT", "5s"},
		{"HTTP_READ_TIMEOUT", "15s"},
		{"HTTP_WRITE_TIMEOUT", "30s"},
		{"HTTP_IDLE_TIMEOUT", "60s"},
		{"HTTP_MAX_BODY_BYTES", "1048576"},

		{"DATABASE_URL", dbURL},
		{"DATABASE_MAX_CONNS", "10"},
		{"DATABASE_CONNECT_TIMEOUT", "5s"},

		{"REDIS_URL", redisURL},
		{"REDIS_PASSWORD", ""},
		{"REDIS_CONNECT_TIMEOUT", "5s"},

		{"QUEUE_STREAM", "gluzo:jobs"},
		{"QUEUE_GROUP", "gateway-workers"},
		{"QUEUE_BATCH_SIZE", "10"},
		{"QUEUE_BLOCK_TIMEOUT", "1s"},
		{"QUEUE_CLAIM_MIN_IDLE", "60s"},
		{"QUEUE_MAX_DELIVERIES", "5"},
		{"QUEUE_MAX_LEN", "100000"},

		{"WORKER_ENABLED", "true"},
		{"WORKER_CONCURRENCY", "4"},
		{"WORKER_MAX_AUTO_RESUMES", "3"},
		{"WORKER_RECOVERY_INTERVAL", "1m"},
		{"WORKER_STALE_RUNNING_AFTER", "5m"},
		{"WORKER_RETRY_FAILED_AFTER", "10s"},

		// The sweeps stay off. This run is about the order path, and a
		// scheduler firing stock and dispatch jobs at the stubs would bury
		// it in unrelated log lines.
		{"SCHEDULER_ENABLED", "false"},
		{"SCHEDULER_POLL_INTERVAL", "30s"},
		{"SCHEDULER_LOCK_TTL", "5m"},
		{"STOCK_SYNC_INTERVAL", "15m"},
		{"STOCK_SYNC_FULL_INTERVAL", "24h"},
		{"SHIPMENT_SYNC_INTERVAL", "15m"},
		{"SHIPMENT_SYNC_MAX_WINDOW", "24h"},
		{"RECONCILE_INTERVAL", "1h"},
		{"RECONCILE_THRESHOLD", "30m"},

		{"VINCULUM_BASE_URL", vinURL},
		{"VINCULUM_API_OWNER", devVinculumOwner},
		{"VINCULUM_API_KEY", devVinculumKey},
		{"VINCULUM_LOCATION", devVendorLocation},
		// Assumption B2 is unresolved, and the adapter refuses to start
		// without a bucket. "Good" is the stub's own value, not a claim
		// about BCPL's.
		{"VINCULUM_SELLABLE_BUCKET", "Good"},
		{"VINCULUM_TIMEOUT", "20s"},
		{"VINCULUM_ORDER_RATE_LIMIT", "80"},
		{"VINCULUM_ORDER_RATE_WINDOW", "5m"},
		{"VINCULUM_DUPLICATE_ORDER_CODES", "101"},

		{"EASYECOM_BASE_URL", easyURL},
		{"EASYECOM_API_KEY", devEasyAPIKey},
		{"EASYECOM_JWT_TOKEN", devEasyJWT},
		{"EASYECOM_EMAIL", ""},
		{"EASYECOM_PASSWORD", ""},
		{"EASYECOM_LOCATION_KEY", devOriginLocation},
		{"EASYECOM_TIMEOUT", "15s"},
		{"EASYECOM_SHIPMENT_STATUS_IDS", ""},

		{"LOG_DIRECTORY", filepath.Join(root, "logs")},
		{"WORKFLOW_DIRECTORY", filepath.Join(root, "workflows")},
		{"LOG_RETENTION_DAYS", "30"},
		{"LOG_RETENTION_INTERVAL", "1h"},

		{"ADMIN_LOG_VIEWER_TOKEN", devAdminToken},
	}

	var b strings.Builder
	b.WriteString("# Generated by cmd/devstack. Temporary; removed on exit.\n")
	for _, kv := range env {
		fmt.Fprintf(&b, "%s=%s\n", kv[0], kv[1])
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func buildServer(ctx context.Context, root string) (string, error) {
	binary := filepath.Join(root, "gateway-server")
	if os.PathSeparator == '\\' {
		binary += ".exe"
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/server")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build cmd/server: %w", err)
	}
	return binary, nil
}

// process is the running gateway.
type process struct {
	cmd  *exec.Cmd
	done chan error
}

func (p *process) stop() {
	if p.cmd.Process == nil {
		return
	}
	// The server installs a signal handler, but Windows has no SIGTERM to
	// send it, so a kill is the portable stop. Every piece of state this run
	// produced is temporary, so there is nothing to lose by not draining.
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
	}
}

func startServer(ctx context.Context, binary, envPath string) (*process, error) {
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "ENV_FILE="+envPath)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start gateway: %w", err)
	}
	p := &process{cmd: cmd, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait() }()
	return p, nil
}

func waitForReady(ctx context.Context, base string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	return waitFor(ctx, 45*time.Second, func() bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/ready", nil)
		if err != nil {
			return false
		}
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode == http.StatusOK
	})
}

func postWebhook(ctx context.Context, base string, creds credentials) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/webhooks/easyecom", strings.NewReader(devOrder))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.CombinedCredentialHeader, creds.header())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("webhook returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var accepted struct {
		CorrelationID string `json:"correlation_id"`
	}
	_ = json.Unmarshal(body, &accepted)
	return accepted.CorrelationID, nil
}

// --- output -----------------------------------------------------------------

func waitFor(ctx context.Context, timeout time.Duration, cond func() bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out after " + timeout.String())
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func step(n int, what string) { fmt.Printf("\n[%d/7] %s\n", n, what) }

func say(format string, args ...any) { fmt.Printf(format+"\n", args...) }

func banner(base string, creds credentials, corr string) {
	fmt.Println("   the stack is up and staying up — press Ctrl+C to tear it all down.")
	fmt.Println()
	fmt.Println("   gateway        ", base)
	fmt.Println("   execution log  ", base+"/admin/logs?correlation_id="+corr)
	fmt.Println("   admin header    Authorization: Bearer " + devAdminToken)
	fmt.Println()
	fmt.Println("   send another order:")
	fmt.Println()
	fmt.Printf("     curl -i -X POST %s/webhooks/easyecom \\\n", base)
	fmt.Printf("       -H 'Content-Type: application/json' \\\n")
	fmt.Printf("       -H '%s: %s' \\\n", auth.CombinedCredentialHeader, creds.header())
	fmt.Printf("       -d '%s'\n", strings.Replace(devOrder, devOrderID, "424243", 1))
	fmt.Println()
	fmt.Println("   the same body twice is deduplicated at intake; a new order_id runs again.")
}
