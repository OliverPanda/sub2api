package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// CompatibleVideo exposes a common asynchronous video contract through Sub2API.
// Existing provider-native media requests keep their native handlers.
func (h *OpenAIGatewayHandler) CompatibleVideo(c *gin.Context) {
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
	reqLog := requestLogger(c, "handler.compatible_video")
	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}
	c.Header("Cache-Control", "no-store")
	if c.Request.Method == http.MethodGet {
		task, err := h.gatewayService.GetCompatibleVideo(c.Request.Context(), c.Param("request_id"), subject.UserID, key.ID, key.Group.ID)
		if err != nil {
			if errors.Is(err, service.ErrCompatibleVideoNotFound) {
				h.errorResponse(c, 404, "not_found_error", "Video task not found")
			} else {
				h.errorResponse(c, 503, "api_error", "Video task store unavailable")
			}
			return
		}
		body, status, err := h.gatewayService.PollCompatibleVideo(c.Request.Context(), task, key, h.apiKeyService)
		if err != nil {
			reqLog.Error("video.poll_or_settlement_failed", zap.String("task_id", task.ID), zap.Error(err))
			if len(body) > 0 {
				c.Data(status, "application/json", body)
			} else {
				h.errorResponse(c, 502, "api_error", "Failed to retrieve or settle video task")
			}
			return
		}
		if !strings.HasSuffix(c.Request.URL.Path, "/content") {
			c.Data(status, "application/json", body)
			return
		}
		if !task.Settled || task.Status != "completed" {
			h.errorResponse(c, 409, "invalid_request_error", "Video content is not ready")
			return
		}
		// Content requests first poll/settle, so downloading cannot bypass payment.
		resp, err := h.gatewayService.CompatibleVideoContent(c.Request.Context(), task)
		if err != nil {
			h.errorResponse(c, 502, "api_error", "Failed to download video")
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for _, name := range []string{"Content-Type", "Content-Length", "Content-Disposition"} {
			if v := resp.Header.Get(name); v != "" {
				c.Header(name, v)
			}
		}
		c.Status(resp.StatusCode)
		_, _ = io.Copy(c.Writer, resp.Body)
		return
	}
	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, 413, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
		} else {
			h.errorResponse(c, 400, "invalid_request_error", "Failed to read request body")
		}
		return
	}
	info, err := service.ParseCompatibleVideoRequest(c.GetHeader("Content-Type"), body)
	if err != nil {
		h.errorResponse(c, 400, "invalid_request_error", err.Error())
		return
	}
	routingModel := info.Model
	if resolvedModel, ok := service.ResolvedUpstreamModelFromContext(c.Request.Context()); ok {
		routingModel = resolvedModel
	}
	info.Model = clientRequestedModel(c, info.Model)
	setOpsRequestContext(c, info.Model, false)
	setActualUpstreamEndpoint(c, "/v1/videos")
	if !service.GroupAllowsVideoGeneration(key.Group) {
		h.errorResponse(c, 403, "permission_error", service.VideoGenerationPermissionMessage())
		return
	}
	idempotencyKey, err := service.NormalizeCompatibleVideoIdempotencyKey(c.GetHeader("Idempotency-Key"))
	if err != nil {
		h.errorResponse(c, 400, "invalid_request_error", err.Error())
		return
	}
	if decision := h.checkSecurityAudit(c, reqLog, key, subject, service.ContentModerationProtocolOpenAIImages, info.Model, info.ModerationBody()); decision != nil && !decision.AllowNextStage {
		h.openAISecurityAuditError(c, decision)
		return
	}
	sub, _ := middleware.GetSubscriptionFromContext(c)
	if err = h.billingCacheService.CheckBillingEligibility(c.Request.Context(), key.User, key, key.Group, sub, service.QuotaPlatform(c.Request.Context(), key)); err != nil {
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
	mediaRelease, acquired := h.acquireImageGenerationSlot(c, false)
	if !acquired {
		return
	}
	if mediaRelease != nil {
		defer mediaRelease()
	}
	ctx := service.WithOpenAIProfitControlSuppressed(c.Request.Context())
	selection, _, err := h.gatewayService.SelectAccountWithSchedulerForCapability(ctx, key.GroupID, "", "", routingModel, nil, service.OpenAIUpstreamTransportHTTPSSE, service.OpenAIEndpointCapabilityVideoGeneration, false, false, false, service.PlatformOpenAI)
	if err != nil || selection == nil || selection.Account == nil {
		h.errorResponse(c, 503, "video_no_eligible_account", "No eligible video_generation account")
		return
	}
	if selection.Acquired && selection.ReleaseFunc != nil {
		defer selection.ReleaseFunc()
	}
	// Scheduler may return a waiting candidate; use the standard admission path.
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
	upstreamModel := account.GetMappedModel(routingModel)
	body, contentType, err := service.PrepareCompatibleVideoRequest(body, c.GetHeader("Content-Type"), upstreamModel)
	if err != nil {
		h.errorResponse(c, 400, "invalid_request_error", err.Error())
		return
	}
	task, err := h.gatewayService.NewCompatibleVideoTask(ctx, key, sub, account, info, body)
	if err != nil {
		h.errorResponse(c, 400, "video_pricing_error", err.Error())
		return
	}
	task.UpstreamModel = upstreamModel
	task.IdempotencyKey = idempotencyKey
	task.PayloadHash = service.CompatibleVideoRequestFingerprint(info, body, contentType)
	setOpsSelectedAccount(c, account.ID, account.Platform)
	// Submission ownership/binding must outlive a client disconnect, but remains bounded.
	submitCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer done()
	response, status, err := h.gatewayService.SubmitCompatibleVideo(submitCtx, account, task, body, contentType)
	if err != nil {
		if errors.Is(err, service.ErrCompatibleVideoReplay) {
			if id := gjson.GetBytes(response, "id").String(); id != "" {
				c.Header("Location", "/v1/videos/"+id)
			}
			c.Data(status, "application/json", response)
			return
		}
		if errors.Is(err, service.ErrCompatibleVideoIdempotencyConflict) {
			h.errorResponse(c, 409, "idempotency_conflict", "Idempotency-Key was already used with different video parameters")
			return
		}
		reqLog.Error("video.submission_failed", zap.String("task_id", task.ID), zap.Int64("account_id", account.ID), zap.Error(err))
		if errors.Is(err, service.ErrInsufficientBalance) {
			h.errorResponse(c, 402, "insufficient_balance", "Insufficient balance for video reservation")
			return
		}
		if len(response) > 0 {
			c.Data(status, "application/json", response)
		} else {
			c.Data(502, "application/json", service.CompatibleVideoErrorBody(task.ID))
		}
		return
	}
	c.Header("Location", "/v1/videos/"+task.ID)
	c.Header("Retry-After", "5")
	c.Data(status, "application/json", response)
}
