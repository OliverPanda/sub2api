package admin

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// UpstreamBalanceItem is one account's last-known upstream wallet snapshot.
// Balance derives from the upstream billing probe's optional wallet fields;
// accounts without a fresh probe report HasBalance=false instead of zero.
type UpstreamBalanceItem struct {
	AccountID   int64    `json:"account_id"`
	Name        string   `json:"name"`
	Platform    string   `json:"platform"`
	Status      string   `json:"status"`
	Balance     *float64 `json:"balance,omitempty"`
	Currency    string   `json:"currency,omitempty"`
	HasBalance  bool     `json:"has_balance"`
	ReceivedAt  string   `json:"received_at,omitempty"`
	Stale       bool     `json:"stale"`
	ProbeStatus string   `json:"probe_status,omitempty"`
	LastError   string   `json:"last_error,omitempty"`
}

// UpstreamBalancesResponse aggregates the wallet balances of every probed
// upstream account so operators can see total upstream purchasing power
// (a+b+c) alongside each provider's individual remaining amount.
type UpstreamBalancesResponse struct {
	Items       []UpstreamBalanceItem `json:"items"`
	Total       float64               `json:"total"`
	HasTotal    bool                  `json:"has_total"`
	ProbeFailed int                   `json:"probe_failed"`
}

// GetUpstreamBalances returns persisted upstream wallet snapshots for every
// API-key account. It never probes an upstream; values come from the last
// successful billing probe persisted in accounts.extra.
//
// GET /api/v1/admin/accounts/upstream-balances
func (h *AccountHandler) GetUpstreamBalances(c *gin.Context) {
	if h.adminService == nil {
		response.Error(c, http.StatusServiceUnavailable, "account service unavailable")
		return
	}

	page, pageSize := response.ParsePagination(c)
	platform := c.Query("platform")
	accountType := c.Query("type")
	status := c.Query("status")
	search := strings.TrimSpace(c.Query("search"))
	if len(search) > 100 {
		search = search[:100]
	}
	privacyMode := strings.TrimSpace(c.Query("privacy_mode"))
	sortBy := c.DefaultQuery("sort_by", "name")
	sortOrder := c.DefaultQuery("sort_order", "asc")

	var groupID int64
	if groupQuery := c.Query("group"); groupQuery != "" {
		if groupQuery == accountListGroupUngroupedQueryValue {
			groupID = service.AccountListGroupUngrouped
		} else {
			parsed, err := strconv.ParseInt(groupQuery, 10, 64)
			if err != nil || parsed < 0 {
				response.BadRequest(c, "invalid group filter")
				return
			}
			groupID = parsed
		}
	}

	accounts, _, err := h.adminService.ListAccounts(
		c.Request.Context(), page, pageSize, platform, accountType, status,
		search, groupID, privacyMode, sortBy, sortOrder,
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	items := make([]UpstreamBalanceItem, 0, len(accounts))
	var sum float64
	hasTotal := false
	probeFailed := 0
	for _, account := range accounts {
		item := UpstreamBalanceItem{
			AccountID: account.ID,
			Name:      account.Name,
			Platform:  account.Platform,
			Status:    account.Status,
		}
		if account.Type != service.AccountTypeAPIKey {
			items = append(items, item)
			continue
		}
		snapshot := service.DecodeUpstreamBillingProbeSnapshotPublic(account.Extra)
		if snapshot == nil {
			items = append(items, item)
			continue
		}
		item.ProbeStatus = snapshot.Status
		item.LastError = snapshot.LastError
		if snapshot.ReceivedAt != nil {
			item.ReceivedAt = snapshot.ReceivedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		if snapshot.Status == service.UpstreamBillingProbeStatusFailed {
			probeFailed++
		}
		if snapshot.Status == service.UpstreamBillingProbeStatusOK && snapshot.Data != nil {
			if raw, ok := snapshot.Data["balance"]; ok {
				if value, ok := raw.(float64); ok && !math.IsNaN(value) && !math.IsInf(value, 0) {
					item.Balance = &value
					item.HasBalance = true
					item.Currency = "USD"
					sum += value
					hasTotal = true
				}
			}
		}
		items = append(items, item)
	}

	response.Success(c, UpstreamBalancesResponse{
		Items:       items,
		Total:       sum,
		HasTotal:    hasTotal,
		ProbeFailed: probeFailed,
	})
}
