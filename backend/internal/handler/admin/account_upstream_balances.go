package admin

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// upstreamBalanceEntry aggregates every API-key account that shares the same
// upstream base URL so operators see one row per provider, not per account.
type upstreamBalanceEntry struct {
	BaseURL     string   `json:"base_url"`
	Accounts    int      `json:"accounts"`
	OKAccounts  int      `json:"ok_accounts"`
	Balance     *float64 `json:"balance,omitempty"`
	Currency    string   `json:"currency,omitempty"`
	HasBalance  bool     `json:"has_balance"`
	ReceivedAt  string   `json:"received_at,omitempty"`
	Stale       bool     `json:"stale"`
	ProbeStatus string   `json:"probe_status,omitempty"`
	LastError   string   `json:"last_error,omitempty"`
}

// upstreamBalanceEntry is one row of UpstreamBalancesResponse.
type UpstreamBalanceEntry = upstreamBalanceEntry

// UpstreamBalancesResponse aggregates wallet balances of every probed upstream
// (grouped by base URL) so operators can see total upstream purchasing power
// (a+b+c) alongside each provider's remaining amount and health.
type UpstreamBalancesResponse struct {
	Items       []UpstreamBalanceEntry `json:"items"`
	Total       float64                `json:"total"`
	HasTotal    bool                   `json:"has_total"`
	ProbeFailed int                    `json:"probe_failed"`
	Accounts    int                    `json:"accounts"`
}

// GetUpstreamBalances returns persisted upstream wallet snapshots for every
// API-key account, aggregated by upstream base URL. It never probes an
// upstream; values come from the last successful billing probe persisted in
// accounts.extra.
//
// GET /api/v1/admin/accounts/upstream-balances
func (h *AccountHandler) GetUpstreamBalances(c *gin.Context) {
	if h.adminService == nil {
		response.Error(c, http.StatusServiceUnavailable, "account service unavailable")
		return
	}

	platform := c.Query("platform")

	accounts, _, err := h.adminService.ListAccounts(
		c.Request.Context(), 1, 1000, platform, "", "", "", 0, "", "name", "asc",
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	type group struct {
		baseURL     string
		entry       upstreamBalanceEntry
		sum         float64
		hasBalance  bool
		receivedAt  string
		okCount     int
		probeFailed int
		statuses    map[string]int
	}
	groups := make(map[string]*group)
	var order []string
	totalAccounts := 0
	probeFailedTotal := 0

	for _, account := range accounts {
		if account.Type != service.AccountTypeAPIKey {
			continue
		}
		totalAccounts++
		baseURL := strings.TrimSpace(account.GetCredential("base_url"))
		if baseURL == "" {
			baseURL = account.Platform
		}
		g, ok := groups[baseURL]
		if !ok {
			g = &group{
				baseURL: baseURL,
				entry:   upstreamBalanceEntry{BaseURL: baseURL},
				statuses: make(map[string]int),
			}
			groups[baseURL] = g
			order = append(order, baseURL)
		}
		g.entry.Accounts++
		snapshot := service.DecodeUpstreamBillingProbeSnapshotPublic(account.Extra)
		if snapshot == nil {
			continue
		}
		g.statuses[snapshot.Status]++
		if snapshot.Status == service.UpstreamBillingProbeStatusOK {
			g.okCount++
		}
		if snapshot.Status == service.UpstreamBillingProbeStatusFailed {
			g.probeFailed++
			probeFailedTotal++
			if g.entry.LastError == "" {
				g.entry.LastError = snapshot.LastError
			}
		}
		if snapshot.ReceivedAt != nil {
			ts := snapshot.ReceivedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
			if ts > g.receivedAt {
				g.receivedAt = ts
			}
		}
		if snapshot.Status == service.UpstreamBillingProbeStatusOK && snapshot.Data != nil {
			if raw, ok := snapshot.Data["balance"]; ok {
				if value, ok := raw.(float64); ok && !math.IsNaN(value) && !math.IsInf(value, 0) {
					g.sum += value
					g.hasBalance = true
				}
			}
		}
	}

	items := make([]UpstreamBalanceEntry, 0, len(groups))
	var total float64
	hasTotal := false
	for _, key := range order {
		g := groups[key]
		entry := g.entry
		entry.OKAccounts = g.okCount
		if g.hasBalance {
			value := g.sum
			entry.Balance = &value
			entry.HasBalance = true
			entry.Currency = "USD"
			entry.ReceivedAt = g.receivedAt
			total += value
			hasTotal = true
		}
		// Derive an aggregate probe status for the upstream.
		switch {
		case g.okCount == entry.Accounts:
			entry.ProbeStatus = service.UpstreamBillingProbeStatusOK
		case g.probeFailed == entry.Accounts:
			entry.ProbeStatus = service.UpstreamBillingProbeStatusFailed
		case g.okCount > 0:
			entry.ProbeStatus = "partial"
		default:
			entry.ProbeStatus = "unknown"
		}
		items = append(items, entry)
	}

	response.Success(c, UpstreamBalancesResponse{
		Items:       items,
		Total:       total,
		HasTotal:    hasTotal,
		ProbeFailed: probeFailedTotal,
		Accounts:    totalAccounts,
	})
}

var _ = sort.Strings
var _ = strconv.Atoi
