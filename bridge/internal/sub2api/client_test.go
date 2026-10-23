package sub2api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OliverPanda/sub2api/bridge/internal/money"
)

func TestVerifyUser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/me" || r.Header.Get("Authorization") != "Bearer user-token" {
			t.Fatalf("unexpected request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"id":7,"email":"u@example.com","balance":12.5}}`))
	}))
	defer server.Close()
	user, err := New(server.URL, "admin-key", server.Client()).VerifyUser(context.Background(), "user-token")
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != 7 || user.Balance.Int64() != 1250 {
		t.Fatalf("unexpected user: %+v", user)
	}
}

func TestRedeemBalanceSendsAdminAuthAndIdempotency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "admin-key" || r.Header.Get("Idempotency-Key") != "idem-1" {
			t.Fatalf("missing headers")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["user_id"].(float64) != 7 || body["value"].(float64) != 10 {
			t.Fatalf("unexpected body: %#v", body)
		}
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"redeem_code":{}}}`))
	}))
	defer server.Close()
	err := New(server.URL, "admin-key", server.Client()).RedeemBalance(
		context.Background(), 7, money.FromInt64(1000), "xddshop-ORDER-1", "note", "idem-1",
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRedeemBalanceReturnsBusinessError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":409,"message":"redeem code already used by another user","data":null}`))
	}))
	defer server.Close()
	err := New(server.URL, "admin-key", server.Client()).RedeemBalance(
		context.Background(), 7, money.FromInt64(1000), "code", "note", "idem-1",
	)
	if err == nil {
		t.Fatal("expected error")
	}
}
