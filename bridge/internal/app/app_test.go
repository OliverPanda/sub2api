package app

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OliverPanda/sub2api/bridge/internal/config"
	"github.com/OliverPanda/sub2api/bridge/internal/domain"
	"github.com/OliverPanda/sub2api/bridge/internal/dujiao"
	"github.com/OliverPanda/sub2api/bridge/internal/sub2api"
)

func testApp() *App {
	return New(
		&config.Config{AdminToken: "test-admin-token"},
		nil,
		nil,
		nil,
		log.New(io.Discard, "", 0),
	)
}

func serve(t *testing.T, app *App, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHealthEndpoint(t *testing.T) {
	rec := serve(t, testApp(), http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content type = %q, want JSON", got)
	}
	if got := rec.Body.String(); !strings.Contains(got, `"status":"ok"`) {
		t.Fatalf("unexpected health body: %s", got)
	}
}

func TestStaticPageUsesSameOriginAssets(t *testing.T) {
	rec := serve(t, testApp(), http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("index status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, want := range []string{`href="app.css"`, `src="app.js"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("index is missing %q", want)
		}
	}
	for _, unwanted := range []string{`href="/app.css"`, `src="/app.js"`} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("index must not use root-absolute asset %q", unwanted)
		}
	}
	for _, want := range []string{`id="console-link"`, `href="/"`, `id="support-link"`, `返回 XDD API 控制台`} {
		if !strings.Contains(body, want) {
			t.Fatalf("index is missing post-payment navigation %q", want)
		}
	}
}

func TestSecurityHeadersAndFrameAncestors(t *testing.T) {
	rec := serve(t, testApp(), http.MethodGet, "/healthz")
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Fatalf("CSP does not restrict framing to self: %q", csp)
	}
	if strings.Contains(csp, "api.xddapi.org.uk") {
		t.Fatalf("CSP hard-codes a deployment hostname: %q", csp)
	}
}

func TestProtectedEndpointsRequireAuthentication(t *testing.T) {
	for _, path := range []string{"/api/catalog", "/api/payment-channels", "/api/orders", "/admin/orders"} {
		t.Run(path, func(t *testing.T) {
			rec := serve(t, testApp(), http.MethodGet, path)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s status = %d, want %d", path, rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestFrontendNeverReadsJWTFromURL(t *testing.T) {
	rec := serve(t, testApp(), http.MethodGet, "/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("app.js status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `localStorage.getItem('auth_token')`) {
		t.Fatal("frontend does not load the same-origin auth_token")
	}
	for _, unwanted := range []string{"URLSearchParams", "params.get('token')", "ui_mode", "embeddedUserId"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("frontend still contains legacy URL-token code %q", unwanted)
		}
	}
	for _, unwanted := range []string{"api('/api/", "api(`/api/", "fetch('/api/", "fetch(`/api/"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("frontend still uses root-absolute API path %q", unwanted)
		}
	}
	for _, want := range []string{"暂未配置充值套餐", "支付通道维护中"} {
		if !strings.Contains(body, want) {
			t.Fatalf("frontend is missing recovery state %q", want)
		}
	}
}

func TestCatalogAllowsEmptySKUsAndUsesConfiguredSupport(t *testing.T) {
	sub2apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/me" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"id":7,"email":"user@example.com","username":"user","balance":12.5}}`)
	}))
	defer sub2apiServer.Close()

	cfg := &config.Config{
		AdminToken: "test-admin-token", Sub2APIBaseURL: sub2apiServer.URL,
		SupportURL: "https://support.example/help", SupportLabel: "提交工单",
		RequestTimeout: time.Second, SKUs: map[string]domain.SKU{},
	}
	app := New(cfg, nil, nil, sub2api.New(sub2apiServer.URL, "admin-key", nil), log.New(io.Discard, "", 0))
	req := httptest.NewRequest(http.MethodGet, "/api/catalog?support_url=https://attacker.example", nil)
	req.Header.Set("Authorization", "Bearer user-token")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		User struct {
			ID int64 `json:"id"`
		} `json:"user"`
		SKUs         []any  `json:"skus"`
		SupportURL   string `json:"support_url"`
		SupportLabel string `json:"support_label"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.User.ID != 7 || len(payload.SKUs) != 0 {
		t.Fatalf("unexpected catalog payload: %+v", payload)
	}
	if payload.SupportURL != cfg.SupportURL || payload.SupportLabel != cfg.SupportLabel {
		t.Fatalf("support config must come from server config, got %+v", payload)
	}
}

func TestPaymentChannelsEmptyIsSuccessful(t *testing.T) {
	sub2apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"data":{"id":7,"email":"user@example.com","balance":12.5}}`)
	}))
	defer sub2apiServer.Close()
	dujiaoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/channel/payment-channels" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status_code":0,"data":{"items":[]}}`)
	}))
	defer dujiaoServer.Close()

	cfg := &config.Config{
		AdminToken: "test-admin-token", Sub2APIBaseURL: sub2apiServer.URL,
		RequestTimeout: time.Second,
		DujiaoBaseURL:  dujiaoServer.URL, DujiaoChannelKey: "key", DujiaoChannelSecret: "secret",
	}
	app := New(cfg, nil, dujiao.New(dujiaoServer.URL, "key", "secret", nil), sub2api.New(sub2apiServer.URL, "admin-key", nil), log.New(io.Discard, "", 0))
	req := httptest.NewRequest(http.MethodGet, "/api/payment-channels", nil)
	req.Header.Set("Authorization", "Bearer user-token")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"channels":[]`) {
		t.Fatalf("empty channels response = %d %s", rec.Code, rec.Body.String())
	}
}
