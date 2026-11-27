package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGetUpstreamBalancesReturnsConfiguredURLsBalancesAndGroups(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStubAdminService()
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	svc.accounts = []service.Account{
		{Type: service.AccountTypeAPIKey, Platform: service.PlatformOpenAI, Credentials: map[string]any{"base_url": " https://api.example/ "}, Groups: []*service.Group{{Name: "alpha"}, {Name: "shared"}}, Extra: map[string]any{service.UpstreamBillingProbeExtraKey: map[string]any{"status": "ok", "received_at": older, "data": map[string]any{"balance": 12.5}}}},
		{Type: service.AccountTypeAPIKey, Platform: service.PlatformOpenAI, Credentials: map[string]any{"base_url": "https://api.example"}, Groups: []*service.Group{{Name: "beta"}, {Name: "shared"}}, Extra: map[string]any{service.UpstreamBillingProbeExtraKey: map[string]any{"status": "ok", "received_at": newer, "data": map[string]any{"balance": 7.5}}}},
		{Type: service.AccountTypeAPIKey, Platform: service.PlatformOpenAI, Credentials: map[string]any{}, Groups: []*service.Group{{Name: "ignored"}}},
		{Type: service.AccountTypeOAuth, Platform: service.PlatformOpenAI, Credentials: map[string]any{"base_url": "https://oauth.example"}, Groups: []*service.Group{{Name: "ignored"}}},
	}
	router := gin.New()
	router.GET("/admin/accounts/upstream-balances", NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil).GetUpstreamBalances)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/accounts/upstream-balances", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Data UpstreamBalancesResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)
	item := payload.Data.Items[0]
	require.Equal(t, "https://api.example", item.BaseURL)
	require.NotNil(t, item.Balance)
	require.Equal(t, 7.5, *item.Balance)
	require.Equal(t, []string{"alpha", "beta", "shared"}, item.Groups)
	require.Equal(t, service.AccountTypeAPIKey, svc.lastListAccounts.accountType)
	require.Equal(t, 1, svc.lastListAccounts.calls)
}

func TestGetUpstreamBalancesDerivesRemainingBalanceFromQuota(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStubAdminService()
	svc.accounts = []service.Account{{
		Type:        service.AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "key-a", "base_url": "https://api.example"},
		Extra: map[string]any{service.UpstreamBillingProbeExtraKey: map[string]any{
			"status": "ok", "data": map[string]any{"quota": 100.0, "quota_used": 35.0},
		}},
	}}
	router := gin.New()
	router.GET("/admin/accounts/upstream-balances", NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil).GetUpstreamBalances)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/accounts/upstream-balances", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Data UpstreamBalancesResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)
	require.NotNil(t, payload.Data.Items[0].Balance)
	require.Equal(t, 65.0, *payload.Data.Items[0].Balance)
	require.Equal(t, 100.0, *payload.Data.Items[0].Quota)
	require.Equal(t, 35.0, *payload.Data.Items[0].QuotaUsed)
}

func TestGetUpstreamBalancesLoadsAllAccountPages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newStubAdminService()
	svc.accounts = make([]service.Account, 1001)
	for i := range svc.accounts {
		svc.accounts[i] = service.Account{
			Type:        service.AccountTypeAPIKey,
			Credentials: map[string]any{"base_url": "https://api.example"},
			Groups:      []*service.Group{{Name: "shared"}},
		}
	}
	router := gin.New()
	router.GET("/admin/accounts/upstream-balances", NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil).GetUpstreamBalances)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/accounts/upstream-balances", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 2, svc.lastListAccounts.calls)
}
