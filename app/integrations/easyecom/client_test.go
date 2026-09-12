package easyecom_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gluzo/integration-gateway/app/apperror"
	"github.com/gluzo/integration-gateway/app/httpclient"
	"github.com/gluzo/integration-gateway/app/integrations/easyecom"
	dtoinventory "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/inventory"
	dtoorder "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/order"
	dtotracking "github.com/gluzo/integration-gateway/app/integrations/easyecom/dto/tracking"
)

// fakeEasyEcom simulates the login and order endpoints.
type fakeEasyEcom struct {
	t           *testing.T
	logins      atomic.Int32
	orderCalls  atomic.Int32
	rejectToken atomic.Bool // when set, order calls with the current token get 401 once
	token       string
	srv         *httptest.Server
}

func newFakeEasyEcom(t *testing.T) *fakeEasyEcom {
	f := &fakeEasyEcom{t: t, token: "jwt-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/access/token", func(w http.ResponseWriter, r *http.Request) {
		f.logins.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("X-API-Key") != "key-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["email"] != "ops@gluzo.com" || body["password"] != "pw" || body["location_key"] != "loc1" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"bad credentials"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"message":"Successfully logged in!","data":{"token":{"jwt_token":"` + f.token + `"}}}`))
	})
	mux.HandleFunc("/orders/V2/getOrderDetails", func(w http.ResponseWriter, r *http.Request) {
		f.orderCalls.Add(1)
		if r.Header.Get("X-API-Key") != "key-123" || r.Header.Get("Authorization") != "Bearer "+f.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if f.rejectToken.CompareAndSwap(true, false) {
			f.token = "jwt-2" // the old token is now revoked
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Query().Get("reference_code") {
		case "REF-1":
			_, _ = w.Write([]byte(`{"code":200,"message":"Successful","data":[{"order_id":1,"reference_code":"REF-1","warehouse_id":5,"order_items":[{"sku":"A","suborder_quantity":1,"selling_price":10}]}]}`))
		case "REF-APP-ERR":
			_, _ = w.Write([]byte(`{"code":404,"message":"Order not found","data":[]}`))
		case "REF-SERVER-ERR":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			_, _ = w.Write([]byte(`{"code":200,"message":"Successful","data":[]}`))
		}
	})
	mux.HandleFunc("/getInventoryDetailsV2", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":[{"sku":"` + r.URL.Query().Get("sku") + `","warehouse_id":5,"available_inventory":"42"}]}`))
	})
	mux.HandleFunc("/Carriers/getTrackingDetails", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":[{"order_id":1,"awb_number":"AWB1","carrier":"Delhivery","status":"In Transit"}]}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeEasyEcom) config() easyecom.Config {
	return easyecom.Config{
		BaseURL:     f.srv.URL,
		APIKey:      "key-123",
		Email:       "ops@gluzo.com",
		Password:    "pw",
		LocationKey: "loc1",
		Timeout:     2 * time.Second,
	}
}

func noBackoff() easyecom.Option {
	return easyecom.WithHTTPOptions(httpclient.WithSleep(func(context.Context, time.Duration) error { return nil }))
}

func TestNewClientValidatesConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  easyecom.Config
		ok   bool
	}{
		{"login credentials", easyecom.Config{APIKey: "k", Email: "e", Password: "p", LocationKey: "l"}, true},
		{"static jwt", easyecom.Config{APIKey: "k", JWTToken: "jwt"}, true},
		{"missing api key", easyecom.Config{JWTToken: "jwt"}, false},
		{"missing credentials", easyecom.Config{APIKey: "k", Email: "e"}, false},
		{"negative timeout", easyecom.Config{APIKey: "k", JWTToken: "jwt", Timeout: -1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := easyecom.NewClient(tt.cfg)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, ok = %v", err, tt.ok)
			}
		})
	}
}

func TestLoginIsCachedAcrossCalls(t *testing.T) {
	f := newFakeEasyEcom(t)
	c, err := easyecom.NewClient(f.config(), noBackoff())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	for i := 0; i < 3; i++ {
		resp, err := c.GetOrderDetails(context.Background(), dtoorder.GetOrderDetailsRequest{ReferenceCode: "REF-1"})
		if err != nil {
			t.Fatalf("GetOrderDetails: %v", err)
		}
		if len(resp.Data.Orders) != 1 || resp.Data.Orders[0].ReferenceCode != "REF-1" {
			t.Fatalf("unexpected orders: %+v", resp.Data.Orders)
		}
	}
	if f.logins.Load() != 1 {
		t.Fatalf("logins = %d, want 1 (token should be cached)", f.logins.Load())
	}
}

func TestRevokedTokenIsRefreshedOnce(t *testing.T) {
	f := newFakeEasyEcom(t)
	c, err := easyecom.NewClient(f.config(), noBackoff())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.GetOrderDetails(context.Background(), dtoorder.GetOrderDetailsRequest{ReferenceCode: "REF-1"}); err != nil {
		t.Fatalf("warm-up: %v", err)
	}
	f.rejectToken.Store(true)
	if _, err := c.GetOrderDetails(context.Background(), dtoorder.GetOrderDetailsRequest{ReferenceCode: "REF-1"}); err != nil {
		t.Fatalf("call after revocation should recover: %v", err)
	}
	if f.logins.Load() != 2 {
		t.Fatalf("logins = %d, want 2", f.logins.Load())
	}
}

func TestLoginFailureIsAuthenticationError(t *testing.T) {
	f := newFakeEasyEcom(t)
	cfg := f.config()
	cfg.Password = "wrong"
	c, err := easyecom.NewClient(cfg, noBackoff())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.GetOrderDetails(context.Background(), dtoorder.GetOrderDetailsRequest{ReferenceCode: "REF-1"})
	if apperror.CategoryOf(err) != apperror.Authentication || apperror.IsRetryable(err) {
		t.Fatalf("expected non-retryable authentication error, got %v", err)
	}
	if f.orderCalls.Load() != 0 {
		t.Fatal("order endpoint must not be called without a token")
	}
}

func TestApplicationLevelErrorInEnvelope(t *testing.T) {
	f := newFakeEasyEcom(t)
	c, _ := easyecom.NewClient(f.config(), noBackoff())
	_, err := c.GetOrderDetails(context.Background(), dtoorder.GetOrderDetailsRequest{ReferenceCode: "REF-APP-ERR"})
	var aerr *apperror.Error
	if !errors.As(err, &aerr) {
		t.Fatalf("expected *apperror.Error, got %v", err)
	}
	if aerr.Category != apperror.ExternalAPI || aerr.Retryable || aerr.ExternalCode != "404" || aerr.ExternalMessage != "Order not found" || aerr.Operation != "GetOrderDetails" {
		t.Fatalf("unexpected error: %+v", aerr)
	}
}

func TestServerErrorsAreRetriedThenSurfaced(t *testing.T) {
	f := newFakeEasyEcom(t)
	c, _ := easyecom.NewClient(f.config(), noBackoff())
	_, err := c.GetOrderDetails(context.Background(), dtoorder.GetOrderDetailsRequest{ReferenceCode: "REF-SERVER-ERR"})
	if !apperror.IsRetryable(err) || apperror.CategoryOf(err) != apperror.ExternalAPI {
		t.Fatalf("expected retryable external error, got %v", err)
	}
	if f.orderCalls.Load() != int32(httpclient.DefaultMaxAttempts) {
		t.Fatalf("order calls = %d, want %d attempts", f.orderCalls.Load(), httpclient.DefaultMaxAttempts)
	}
}

func TestInvalidRequestsFailBeforeCalling(t *testing.T) {
	f := newFakeEasyEcom(t)
	c, _ := easyecom.NewClient(f.config(), noBackoff())
	ctx := context.Background()
	if _, err := c.GetOrderDetails(ctx, dtoorder.GetOrderDetailsRequest{}); apperror.CategoryOf(err) != apperror.Validation {
		t.Fatalf("expected validation error, got %v", err)
	}
	if _, err := c.GetInventoryDetails(ctx, dtoinventory.GetInventoryDetailsRequest{}); apperror.CategoryOf(err) != apperror.Validation {
		t.Fatalf("expected validation error, got %v", err)
	}
	if _, err := c.GetTrackingDetails(ctx, dtotracking.GetTrackingDetailsRequest{}); apperror.CategoryOf(err) != apperror.Validation {
		t.Fatalf("expected validation error, got %v", err)
	}
	if f.orderCalls.Load() != 0 {
		t.Fatal("invalid request reached the server")
	}
}

func TestInventoryAndTrackingEndpoints(t *testing.T) {
	f := newFakeEasyEcom(t)
	c, _ := easyecom.NewClient(f.config(), noBackoff())
	ctx := context.Background()

	inv, err := c.GetInventoryDetails(ctx, dtoinventory.GetInventoryDetailsRequest{SKU: "DAB-1", WarehouseID: "5"})
	if err != nil {
		t.Fatalf("GetInventoryDetails: %v", err)
	}
	if len(inv.Data) != 1 || inv.Data[0].SKU != "DAB-1" || int64(inv.Data[0].AvailableInventory) != 42 {
		t.Fatalf("unexpected inventory: %+v", inv.Data)
	}

	trk, err := c.GetTrackingDetails(ctx, dtotracking.GetTrackingDetailsRequest{AWBNumber: "AWB1"})
	if err != nil {
		t.Fatalf("GetTrackingDetails: %v", err)
	}
	if len(trk.Data) != 1 || trk.Data[0].AWBNumber != "AWB1" || trk.Data[0].Carrier != "Delhivery" {
		t.Fatalf("unexpected tracking: %+v", trk.Data)
	}
}

func TestStaticTokenIsUsedDirectly(t *testing.T) {
	f := newFakeEasyEcom(t)
	cfg := easyecom.Config{BaseURL: f.srv.URL, APIKey: "key-123", JWTToken: "jwt-1"}
	c, err := easyecom.NewClient(cfg, noBackoff())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.GetOrderDetails(context.Background(), dtoorder.GetOrderDetailsRequest{ReferenceCode: "REF-1"}); err != nil {
		t.Fatalf("GetOrderDetails: %v", err)
	}
	if f.logins.Load() != 0 {
		t.Fatal("static token source must not log in")
	}
}

func TestLoginRespectsJWTExpiry(t *testing.T) {
	f := newFakeEasyEcom(t)
	// A JWT whose exp claim is one minute in the future: the source must
	// re-login on the next call rather than trusting its 24h cache.
	claims, _ := json.Marshal(map[string]int64{"exp": time.Now().Add(time.Minute).Unix()})
	f.token = "h." + base64.RawURLEncoding.EncodeToString(claims) + ".s"

	c, _ := easyecom.NewClient(f.config(), noBackoff())
	for i := 0; i < 2; i++ {
		if _, err := c.GetOrderDetails(context.Background(), dtoorder.GetOrderDetailsRequest{ReferenceCode: "REF-1"}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if f.logins.Load() != 2 {
		t.Fatalf("logins = %d, want 2 (token inside refresh margin must not be reused)", f.logins.Load())
	}
}
