package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ReplicateMusic provides a stable Sub2API music task contract over Replicate predictions.
func (h *OpenAIGatewayHandler) ReplicateMusic(c *gin.Context) {
	streamStarted := false
	defer h.recoverResponsesPanic(c, &streamStarted)
	key, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || key == nil || key.Group == nil {
		h.errorResponse(c, 401, "authentication_error", "Invalid API key")
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, 500, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(c, "handler.replicate_music")
	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}
	if c.Request.Method == http.MethodGet {
		task, err := h.gatewayService.GetReplicateMusic(c.Request.Context(), c.Param("music_id"), subject.UserID, key.ID, key.Group.ID)
		if err != nil {
			if errors.Is(err, service.ErrCompatibleVideoNotFound) {
				h.errorResponse(c, 404, "not_found_error", "Music task not found")
			} else {
				h.errorResponse(c, 503, "api_error", "Music task store unavailable")
			}
			return
		}
		if strings.HasSuffix(c.Request.URL.Path, "/content") {
			if body, status, pollErr := h.gatewayService.PollReplicateMusic(c.Request.Context(), task, key, h.apiKeyService); pollErr != nil {
				if len(body) > 0 {
					c.Data(status, "application/json", body)
				} else {
					h.errorResponse(c, 502, "api_error", "Failed to retrieve music task")
				}
				return
			}
			output, err := replicateMusicOutputURL(task.Output)
			if err != nil {
				h.errorResponse(c, http.StatusConflict, "invalid_request_error", "Music content is not ready")
				return
			}
			u, err := url.Parse(output)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
				h.errorResponse(c, http.StatusBadGateway, "api_error", "Invalid music output URL")
				return
			}
			c.Redirect(http.StatusFound, output)
			return
		}
		body, status, err := h.gatewayService.PollReplicateMusic(c.Request.Context(), task, key, h.apiKeyService)
		if err != nil {
			if len(body) > 0 {
				c.Data(status, "application/json", body)
			} else {
				h.errorResponse(c, 502, "api_error", "Failed to retrieve music task")
			}
			return
		}
		c.Data(status, "application/json", body)
		return
	}
	if c.Request.Method == http.MethodDelete {
		task, err := h.gatewayService.GetReplicateMusic(c.Request.Context(), c.Param("music_id"), subject.UserID, key.ID, key.Group.ID)
		if err != nil {
			if errors.Is(err, service.ErrCompatibleVideoNotFound) {
				h.errorResponse(c, 404, "not_found_error", "Music task not found")
			} else {
				h.errorResponse(c, 503, "api_error", "Music task store unavailable")
			}
			return
		}
		if err = h.gatewayService.CancelReplicateMusic(c.Request.Context(), task); err != nil {
			if errors.Is(err, service.ErrReplicateMusicNotCancelable) {
				h.errorResponse(c, http.StatusConflict, "invalid_request_error", err.Error())
				return
			}
			h.errorResponse(c, 502, "api_error", "Failed to cancel music task")
			return
		}
		c.JSON(http.StatusOK, gin.H{"id": task.ID, "object": "music.task", "status": "canceled"})
		return
	}
	body, err := ioReadMusicBody(c)
	if err != nil {
		h.errorResponse(c, 400, "invalid_request_error", err.Error())
		return
	}
	var request struct {
		Model string          `json:"model"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &request) != nil || strings.TrimSpace(request.Model) == "" || len(request.Input) == 0 || string(request.Input) == "null" {
		h.errorResponse(c, 400, "invalid_request_error", "model and input are required")
		return
	}
	if decision := h.checkSecurityAudit(c, requestLogger(c, "handler.replicate_music"), key, subject, service.ContentModerationProtocolOpenAIImages, request.Model, body); decision != nil && !decision.AllowNextStage {
		h.openAISecurityAuditError(c, decision)
		return
	}
	sub, _ := middleware.GetSubscriptionFromContext(c)
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), key.User, key, key.Group, sub, service.QuotaPlatform(c.Request.Context(), key)); err != nil {
		status, code, msg, _ := billingErrorDetails(err)
		h.errorResponse(c, status, code, msg)
		return
	}
	userRelease, acquired := h.acquireResponsesUserSlot(c, subject.UserID, subject.Concurrency, false, &streamStarted, reqLog)
	if !acquired {
		return
	}
	if userRelease != nil {
		defer userRelease()
	}
	ctx := service.WithOpenAIProfitControlSuppressed(c.Request.Context())
	selection, _, err := h.gatewayService.SelectAccountWithSchedulerForCapability(ctx, key.GroupID, "", "", request.Model, nil, service.OpenAIUpstreamTransportHTTPSSE, service.OpenAIEndpointCapabilityMusicGeneration, false, false, false, service.PlatformOpenAI)
	if err != nil || selection == nil || selection.Account == nil {
		h.errorResponse(c, 503, "music_no_eligible_account", "No eligible Replicate music account")
		return
	}
	if selection.Acquired && selection.ReleaseFunc != nil {
		defer selection.ReleaseFunc()
	}
	if !selection.Acquired {
		release, slot := h.acquireResponsesAccountSlot(c, key.GroupID, "", selection, false, &streamStarted, reqLog)
		if slot != openAISlotAcquireOK {
			return
		}
		if release != nil {
			defer release()
		}
	}
	account := selection.Account
	setOpsSelectedAccount(c, account.ID, account.Platform)
	task, err := service.NewReplicateMusicTask(key, sub, account, request.Model, request.Input, h.cfg)
	if err != nil {
		h.errorResponse(c, 400, "music_pricing_error", err.Error())
		return
	}
	submitCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer done()
	response, status, err := h.gatewayService.SubmitReplicateMusic(submitCtx, account, task)
	if err != nil {
		if errors.Is(err, service.ErrInsufficientBalance) {
			h.errorResponse(c, http.StatusPaymentRequired, "insufficient_balance", "Insufficient balance for music reservation")
			return
		}
		if len(response) > 0 {
			c.Data(status, "application/json", response)
		} else {
			h.errorResponse(c, 502, "api_error", "Music submission failed")
		}
		return
	}
	c.Header("Location", "/v1/music/"+task.ID)
	c.Header("Retry-After", "3")
	c.Data(status, "application/json", response)
}

func replicateMusicOutputURL(raw json.RawMessage) (string, error) {
	var output string
	if json.Unmarshal(raw, &output) == nil && strings.TrimSpace(output) != "" {
		return strings.TrimSpace(output), nil
	}
	var outputs []string
	if json.Unmarshal(raw, &outputs) == nil {
		for _, output := range outputs {
			if strings.TrimSpace(output) != "" {
				return strings.TrimSpace(output), nil
			}
		}
	}
	var object struct {
		URL      string `json:"url"`
		AudioURL string `json:"audio_url"`
	}
	if json.Unmarshal(raw, &object) == nil {
		if strings.TrimSpace(object.AudioURL) != "" {
			return strings.TrimSpace(object.AudioURL), nil
		}
		if strings.TrimSpace(object.URL) != "" {
			return strings.TrimSpace(object.URL), nil
		}
	}
	return "", errors.New("music output URL is unavailable")
}

func ioReadMusicBody(c *gin.Context) ([]byte, error) {
	if c.Request.ContentLength > 4<<20 {
		return nil, errors.New("music request body is too large")
	}
	return io.ReadAll(io.LimitReader(c.Request.Body, 4<<20+1))
}
