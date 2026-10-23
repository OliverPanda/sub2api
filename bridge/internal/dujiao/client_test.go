package dujiao

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSignRequestMatchesDujiaoContract(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	got := SignRequest("secret", "POST", "/api/v1/channel/orders", "1700000000", body)
	bodyMD5 := md5.Sum(body)
	signString := "POST\n/api/v1/channel/orders\n1700000000\n" + hex.EncodeToString(bodyMD5[:])
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write([]byte(signString))
	want := hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("signature mismatch: got %s want %s", got, want)
	}
}

func TestClientTreatsHTTP200BusinessErrorAsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(headerChannelKey) != "key" || r.Header.Get(headerChannelSignature) == "" {
			t.Fatal("missing channel authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status_code":400,"msg":"bad order","error_code":"validation_error","request_id":"req-1"}`))
	}))
	defer server.Close()
	client := New(server.URL, "key", "secret", server.Client())
	_, err := client.GetOrder(context.Background(), "sub2api-user-1", "ORDER-1")
	if err == nil || !strings.Contains(err.Error(), "bad order") {
		t.Fatalf("expected business error, got %v", err)
	}
}

func TestVerifyCallback(t *testing.T) {
	now := time.Unix(1700000000, 0)
	client := New("http://example.invalid", "key", "secret", nil)
	client.now = func() time.Time { return now }
	body := []byte(`{"order_id":123,"telegram_user_id":"sub2api-user-1"}`)
	timestamp := "1700000000"
	signature := SignRequest("secret", "POST", "/internal/order-paid", timestamp, body)
	if err := client.VerifyCallback("key", "POST", "/internal/order-paid", timestamp, signature, body, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := client.VerifyCallback("key", "POST", "/internal/order-paid", timestamp, strings.Repeat("0", 64), body, time.Minute); err == nil {
		t.Fatal("expected invalid signature")
	}
}

func TestCallbackTimestampOutsideWindow(t *testing.T) {
	client := New("http://example.invalid", "key", "secret", nil)
	client.now = func() time.Time { return time.Unix(1700001000, 0) }
	body := []byte(`{}`)
	signature := SignRequest("secret", "POST", "/internal/order-paid", "1700000000", body)
	if err := client.VerifyCallback("key", "POST", "/internal/order-paid", "1700000000", signature, body, time.Minute); err == nil {
		t.Fatal("expected timestamp error")
	}
}

func TestCreateOrderDecodesOrderID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/channel/orders" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["channel_user_id"] != "sub2api-user-7" {
			t.Fatalf("channel_user_id = %#v", body["channel_user_id"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status_code":0,"msg":"ok","data":{"order_id":123,"order_no":"ORD-123","status":"pending_payment","currency":"CNY","total_amount":"10.00"}}`))
	}))
	defer server.Close()

	client := New(server.URL, "key", "secret", server.Client())
	order, err := client.CreateOrder(context.Background(), "sub2api-user-7", 11, 22, 1)
	if err != nil {
		t.Fatal(err)
	}
	if order.ID != 123 || order.OrderNo != "ORD-123" {
		t.Fatalf("unexpected order: %+v", order)
	}
}

func TestPaymentChannelsFiltersByOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/channel/payment-channels" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("order_no"); got != "ORD-123" {
			t.Fatalf("order_no = %q", got)
		}
		if got := r.URL.Query().Get("channel_user_id"); got != "sub2api-user-7" {
			t.Fatalf("channel_user_id = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status_code":0,"msg":"ok","data":{"items":[{"id":3,"name":"微信支付","provider_type":"wechatpay","channel_type":"wechatpay","interaction_mode":"qr"}]}}`))
	}))
	defer server.Close()

	client := New(server.URL, "key", "secret", server.Client())
	channels, err := client.PaymentChannels(context.Background(), "ORD-123", "sub2api-user-7")
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0].ID != 3 {
		t.Fatalf("unexpected channels: %+v", channels)
	}
}

func TestGetOrderSendsChannelUserIDAndSignsPathOnly(t *testing.T) {
	const timestamp = "1700000000"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/channel/orders/by-order-no/ORD-123" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("channel_user_id"); got != "sub2api-user-7" {
			t.Fatalf("channel_user_id = %q", got)
		}
		wantSignature := SignRequest("secret", http.MethodGet, "/api/v1/channel/orders/by-order-no/ORD-123", timestamp, nil)
		if got := r.Header.Get(headerChannelSignature); got != wantSignature {
			t.Fatalf("signature = %q, want %q", got, wantSignature)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status_code":0,"msg":"ok","data":{"order_id":123,"order_no":"ORD-123","status":"pending_payment","currency":"CNY","total_amount":"10.00","online_paid_amount":"0.00","paid_amount":"0.00"}}`))
	}))
	defer server.Close()

	client := New(server.URL, "key", "secret", server.Client())
	client.now = func() time.Time { return time.Unix(1700000000, 0) }
	order, err := client.GetOrder(context.Background(), "sub2api-user-7", "ORD-123")
	if err != nil {
		t.Fatal(err)
	}
	if order.ID != 123 {
		t.Fatalf("order id = %d", order.ID)
	}
}

func TestCancelOrderSendsIdentityAndReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/channel/orders/123/cancel" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if body["channel_user_id"] != "sub2api-user-7" || body["reason"] != "price changed" {
			t.Fatalf("body = %s", raw)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status_code":0,"msg":"ok","data":{"order_id":123,"status":"canceled"}}`))
	}))
	defer server.Close()

	client := New(server.URL, "key", "secret", server.Client())
	if err := client.CancelOrder(context.Background(), "sub2api-user-7", 123, "price changed"); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyCallbackUsesConfiguredWindow(t *testing.T) {
	now := time.Unix(1700000000, 0)
	client := New("http://example.invalid", "key", "secret", nil)
	client.now = func() time.Time { return now.Add(2 * time.Minute) }
	body := []byte(`{"order_id":123,"telegram_user_id":"sub2api-user-7"}`)
	signature := SignRequest("secret", "POST", "/internal/order-paid", "1700000000", body)
	if err := client.VerifyCallback("key", "POST", "/internal/order-paid", "1700000000", signature, body, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := client.VerifyCallback("key", "POST", "/internal/order-paid", "1700000000", signature, body, time.Minute); err == nil {
		t.Fatal("expected configured one-minute window to reject two-minute-old callback")
	}
}

func TestParseCallback(t *testing.T) {
	orderID, userID, err := ParseCallback([]byte(`{"order_id":123,"telegram_user_id":"sub2api-user-7"}`))
	if err != nil {
		t.Fatal(err)
	}
	if orderID != 123 || userID != "sub2api-user-7" {
		t.Fatalf("got order=%d user=%q", orderID, userID)
	}
}
