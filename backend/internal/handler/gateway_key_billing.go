package handler

import (
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const keyBillingInfoSchemaVersion = 1

type keyBillingInfoResponse struct {
	Object                  string   `json:"object"`
	SchemaVersion           int      `json:"schema_version"`
	BillingScope            string   `json:"billing_scope"`
	GroupRateMultiplier     float64  `json:"group_rate_multiplier"`
	UserRateMultiplier      *float64 `json:"user_rate_multiplier,omitempty"`
	ResolvedRateMultiplier  float64  `json:"resolved_rate_multiplier"`
	PeakRateEnabled         bool     `json:"peak_rate_enabled"`
	PeakStart               *string  `json:"peak_start,omitempty"`
	PeakEnd                 *string  `json:"peak_end,omitempty"`
	PeakRateMultiplier      *float64 `json:"peak_rate_multiplier,omitempty"`
	AppliedPeakMultiplier   *float64 `json:"applied_peak_multiplier,omitempty"`
	EffectiveRateMultiplier float64  `json:"effective_rate_multiplier"`
	// Balance is the authenticated user's wallet balance. Only present for
	// balance-mode keys so probes of subscription/quota-only upstreams omit it.
	Balance    *float64  `json:"balance,omitempty"`
	Quota      *float64  `json:"quota,omitempty"`
	QuotaUsed  *float64  `json:"quota_used,omitempty"`
	Timezone   *string   `json:"timezone,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

// KeyBillingInfo returns the token billing multiplier effective for the authenticated API key.
// GET /v1/sub2api/billing
func (h *GatewayHandler) KeyBillingInfo(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	if h.cfg != nil && h.cfg.RunMode == config.RunModeSimple {
		h.errorResponse(c, http.StatusNotFound, "not_found_error", "Billing information is not supported in simple mode")
		return
	}
	if apiKey.GroupID == nil {
		h.errorResponse(c, http.StatusForbidden, "permission_error", "API key is not assigned to a group")
		return
	}
	if apiKey.Group == nil {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "Billing information is unavailable")
		return
	}

	resolvedRate, ok := h.resolveKeyBillingRate(c, apiKey)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "Billing information is unavailable")
		return
	}

	// Balance/quota enrichment is best-effort: the billing rate is the
	// contract, wallet/quota figures are additive and must not fail the call.
	subject, hasSubject := middleware2.GetAuthSubjectFromContext(c)
	var balance *float64
	if hasSubject && h.userService != nil {
		if user, err := h.userService.GetByID(c.Request.Context(), subject.UserID); err == nil {
			balance = &user.Balance
		}
	}
	var quota, quotaUsed *float64
	if apiKey.Quota > 0 {
		quota = &apiKey.Quota
		quotaUsed = &apiKey.QuotaUsed
	}

	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, buildKeyBillingInfo(apiKey, resolvedRate, balance, quota, quotaUsed, timezone.Now()))
}

func (h *GatewayHandler) resolveKeyBillingRate(c *gin.Context, apiKey *service.APIKey) (float64, bool) {
	groupRate := apiKey.Group.RateMultiplier
	switch apiKey.Group.Platform {
	case service.PlatformOpenAI, service.PlatformGrok:
		if h.openAIGatewayService == nil {
			return 0, false
		}
		return h.openAIGatewayService.ResolveUserGroupRateMultiplier(c.Request.Context(), apiKey.UserID, *apiKey.GroupID, groupRate), true
	default:
		if h.gatewayService == nil {
			return 0, false
		}
		return h.gatewayService.ResolveUserGroupRateMultiplier(c.Request.Context(), apiKey.UserID, *apiKey.GroupID, groupRate), true
	}
}

func buildKeyBillingInfo(apiKey *service.APIKey, resolvedRate float64, balance, quota, quotaUsed *float64, now time.Time) keyBillingInfoResponse {
	groupRate := apiKey.Group.RateMultiplier
	var userRate *float64
	if resolvedRate != groupRate {
		userRate = &resolvedRate
	}
	appliedPeak := apiKey.Group.PeakMultiplierAt(now)

	response := keyBillingInfoResponse{
		Object:                  "sub2api.key_billing",
		SchemaVersion:           keyBillingInfoSchemaVersion,
		BillingScope:            "token",
		GroupRateMultiplier:     groupRate,
		UserRateMultiplier:      userRate,
		ResolvedRateMultiplier:  resolvedRate,
		PeakRateEnabled:         apiKey.Group.PeakRateEnabled,
		EffectiveRateMultiplier: resolvedRate * appliedPeak,
		Balance:                 balance,
		Quota:                   quota,
		QuotaUsed:               quotaUsed,
		ObservedAt:              now.UTC(),
	}
	if apiKey.Group.PeakRateEnabled {
		response.PeakStart = &apiKey.Group.PeakStart
		response.PeakEnd = &apiKey.Group.PeakEnd
		response.PeakRateMultiplier = &apiKey.Group.PeakRateMultiplier
		response.AppliedPeakMultiplier = &appliedPeak
		tz := timezone.Location().String()
		response.Timezone = &tz
	}
	return response
}
