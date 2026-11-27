package admin

import (
	"math"
	"net/http"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type UpstreamBalanceEntry struct {
	BaseURL        string   `json:"base_url"`
	Balance        *float64 `json:"balance"`
	Quota          *float64 `json:"quota"`
	QuotaUsed      *float64 `json:"quota_used"`
	QuotaUnlimited bool     `json:"quota_unlimited"`
	HasBalance     bool     `json:"has_balance"`
	Groups         []string `json:"groups"`
}

type UpstreamBalancesResponse struct {
	Items []UpstreamBalanceEntry `json:"items"`
}

// GetUpstreamBalances groups configured API-key accounts by their upstream URL.
func (h *AccountHandler) GetUpstreamBalances(c *gin.Context) {
	if h.adminService == nil {
		response.Error(c, http.StatusServiceUnavailable, "account service unavailable")
		return
	}

	type aggregate struct {
		item     UpstreamBalanceEntry
		groups   map[string]struct{}
		seenKeys map[string]struct{}
	}
	byURL := make(map[string]*aggregate)
	var order []string

	const pageSize = 1000
	for page, total := 1, 1; (page-1)*pageSize < total; page++ {
		accounts, count, err := h.adminService.ListAccounts(
			c.Request.Context(), page, pageSize, c.Query("platform"), service.AccountTypeAPIKey, "", "", 0, "", "name", "asc",
		)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		total = int(count)
		for _, account := range accounts {
			baseURL := strings.TrimRight(strings.TrimSpace(account.GetCredential("base_url")), "/")
			if account.Type != service.AccountTypeAPIKey || baseURL == "" {
				continue
			}
			a, ok := byURL[baseURL]
			if !ok {
				a = &aggregate{
					item:     UpstreamBalanceEntry{BaseURL: baseURL},
					groups:   make(map[string]struct{}),
					seenKeys: make(map[string]struct{}),
				}
				byURL[baseURL] = a
				order = append(order, baseURL)
			}
			for _, group := range account.Groups {
				if group != nil && group.Name != "" {
					a.groups[group.Name] = struct{}{}
				}
			}

			apiKey := strings.TrimSpace(account.GetCredential("api_key"))
			if apiKey != "" {
				if _, seen := a.seenKeys[apiKey]; seen {
					continue
				}
				a.seenKeys[apiKey] = struct{}{}
			}
			snapshot := service.DecodeUpstreamBillingProbeSnapshotPublic(account.Extra)
			if snapshot == nil || snapshot.Status != service.UpstreamBillingProbeStatusOK || snapshot.Data == nil {
				continue
			}
			balance, hasBalance := finiteSnapshotNumber(snapshot.Data, "balance")
			quota, hasQuota := finiteSnapshotNumber(snapshot.Data, "quota")
			quotaUsed, hasQuotaUsed := finiteSnapshotNumber(snapshot.Data, "quota_used")
			if !hasBalance && hasQuota {
				used := quotaUsed
				balance = math.Max(quota-used, 0)
				hasBalance = true
			}
			if hasBalance {
				a.item.Balance = &balance
				a.item.HasBalance = true
			}
			if hasQuota {
				a.item.Quota = &quota
				if !hasQuotaUsed {
					quotaUsed = 0
				}
				a.item.QuotaUsed = &quotaUsed
				a.item.QuotaUnlimited, _ = snapshot.Data["quota_unlimited"].(bool)
			}
		}
		if len(accounts) == 0 {
			break
		}
	}

	items := make([]UpstreamBalanceEntry, 0, len(order))
	for _, baseURL := range order {
		a := byURL[baseURL]
		a.item.Groups = make([]string, 0, len(a.groups))
		for name := range a.groups {
			a.item.Groups = append(a.item.Groups, name)
		}
		sort.Strings(a.item.Groups)
		items = append(items, a.item)
	}
	response.Success(c, UpstreamBalancesResponse{Items: items})
}

func finiteSnapshotNumber(data map[string]any, key string) (float64, bool) {
	value, ok := data[key].(float64)
	return value, ok && value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
