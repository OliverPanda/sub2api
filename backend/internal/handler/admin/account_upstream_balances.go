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
	BaseURL    string   `json:"base_url"`
	Balance    *float64 `json:"balance"`
	HasBalance bool     `json:"has_balance"`
	Groups     []string `json:"groups"`
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
		item             UpstreamBalanceEntry
		groups           map[string]struct{}
		latestSnapshotAt int64
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
			if account.Type != service.AccountTypeAPIKey {
				continue
			}
			baseURL := strings.TrimRight(strings.TrimSpace(account.GetCredential("base_url")), "/")
			if baseURL == "" {
				continue
			}
			a, ok := byURL[baseURL]
			if !ok {
				a = &aggregate{item: UpstreamBalanceEntry{BaseURL: baseURL}, groups: make(map[string]struct{})}
				byURL[baseURL] = a
				order = append(order, baseURL)
			}
			for _, group := range account.Groups {
				if group != nil && group.Name != "" {
					a.groups[group.Name] = struct{}{}
				}
			}
			snapshot := service.DecodeUpstreamBillingProbeSnapshotPublic(account.Extra)
			if snapshot == nil || snapshot.Status != service.UpstreamBillingProbeStatusOK || snapshot.Data == nil || snapshot.ReceivedAt == nil {
				continue
			}
			balance, ok := snapshot.Data["balance"].(float64)
			if !ok || math.IsNaN(balance) || math.IsInf(balance, 0) || snapshot.ReceivedAt.UnixNano() <= a.latestSnapshotAt {
				continue
			}
			a.item.Balance = &balance
			a.item.HasBalance = true
			a.latestSnapshotAt = snapshot.ReceivedAt.UnixNano()
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
