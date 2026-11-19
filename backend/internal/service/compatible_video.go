package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const OpenAIEndpointCapabilityVideoGeneration OpenAIEndpointCapability = "video_generation"
const CompatibleVideoIDPrefix = "mvid_"

var ErrCompatibleVideoNotFound = errors.New("video task not found")

// CompatibleVideoTask is an immutable pricing/ownership snapshot plus upstream state.
// The repository atomically reserves, releases and captures the balance hold.
type CompatibleVideoTask struct {
	ID                                                                   string
	UserID, APIKeyID, GroupID, AccountID                                 int64
	UpstreamID, Status                                                   string
	Settled                                                              bool
	HoldAmount, TotalCost, ActualCost, RateMultiplier                    float64
	Model, UpstreamModel, Resolution, PayloadHash                        string
	Seconds                                                              int
	SubscriptionID                                                       *int64
	SimpleMode, SimpleWindowOnly                                         bool
	AccountRateMultiplier                                                float64
	RequestBody                                                          []byte `json:"request_body,omitempty"`
	ContentType                                                          string `json:"content_type,omitempty"`
	APIKeyQuota, APIKeyRateLimit5h, APIKeyRateLimit1d, APIKeyRateLimit7d float64
	CreatedAt                                                            time.Time
}

// Kept separate from UsageBillingRepository to preserve existing billing adapters.
type CompatibleVideoRepository interface {
	CreateCompatibleVideo(context.Context, *CompatibleVideoTask) error
	GetCompatibleVideo(context.Context, string) (*CompatibleVideoTask, error)
	AcceptCompatibleVideo(context.Context, string, string) error
	ReleaseCompatibleVideo(context.Context, string, string) error
	ListPendingCompatibleVideos(context.Context, int) ([]*CompatibleVideoTask, error)
}

type compatibleVideoSubmissionStateRepository interface {
	MarkCompatibleVideoSubmissionUnknown(context.Context, string) error
}

func (s *OpenAIGatewayService) compatibleVideoRepo() (CompatibleVideoRepository, error) {
	r, ok := s.usageBillingRepo.(CompatibleVideoRepository)
	if !ok {
		return nil, errors.New("compatible video repository is unavailable")
	}
	return r, nil
}

func markCompatibleVideoSubmissionUnknown(ctx context.Context, repo CompatibleVideoRepository, id string) {
	if stateRepo, ok := repo.(compatibleVideoSubmissionStateRepository); ok {
		_ = stateRepo.MarkCompatibleVideoSubmissionUnknown(context.WithoutCancel(ctx), id)
	}
}

type CompatibleVideoRequestInfo struct {
	GrokMediaRequestInfo
	ExplicitResolution bool
}

// ParseCompatibleVideoRequest validates billing parameters without applying xAI's 15s clamp.
// Provider-specific quality tiers are finalized before reservation in PrepareCompatibleVideoRequest.
func ParseCompatibleVideoRequest(contentType string, body []byte) (CompatibleVideoRequestInfo, error) {
	var info CompatibleVideoRequestInfo
	fields := map[string]string{}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return info, fmt.Errorf("invalid Content-Type")
	}
	switch mediaType {
	case "application/json":
		if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
			return info, fmt.Errorf("request body must be a JSON object")
		}
		seen := map[string]bool{}
		var fieldErr error
		gjson.ParseBytes(body).ForEach(func(k, v gjson.Result) bool {
			name := k.String()
			if seen[name] {
				fieldErr = fmt.Errorf("duplicate field %s", name)
				return false
			}
			seen[name] = true
			switch name {
			case "model", "prompt", "size", "seconds", "resolution", "input_reference":
				if v.Type != gjson.String && !(name == "seconds" && v.Type == gjson.Number) {
					fieldErr = fmt.Errorf("invalid %s", name)
					return false
				}
				fields[name] = v.String()
			default:
				if !compatibleVideoAllowedField(name) {
					fieldErr = fmt.Errorf("unsupported video field %s; use top-level seconds, size, resolution and input_reference", name)
					return false
				}
			}
			return true
		})
		if fieldErr != nil {
			return info, fieldErr
		}
		if v := gjson.GetBytes(body, "input_reference"); v.Type == gjson.String && v.String() != "" {
			info.InputImageURLs = []string{v.String()}
		}
	case "multipart/form-data":
		if params["boundary"] == "" {
			return info, fmt.Errorf("multipart boundary is required")
		}
		seen := map[string]bool{}
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, e := reader.NextPart()
			if e == io.EOF {
				break
			}
			if e != nil {
				return info, e
			}
			name := part.FormName()
			if !compatibleVideoAllowedField(name) {
				_ = part.Close()
				return info, fmt.Errorf("unsupported video field %s", name)
			}
			if seen[name] {
				_ = part.Close()
				return info, fmt.Errorf("duplicate field %s", name)
			}
			seen[name] = true
			if part.FileName() == "" {
				data, e := io.ReadAll(io.LimitReader(part, (1<<20)+1))
				if e != nil {
					return info, e
				}
				if len(data) > 1<<20 {
					return info, fmt.Errorf("video field is too large")
				}
				fields[name] = string(data)
			} else if name == "input_reference" {
				data, e := io.ReadAll(part)
				if e != nil {
					return info, e
				}
				contentType := http.DetectContentType(data)
				if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
					return info, fmt.Errorf("input_reference must be an image")
				}
				info.Uploads = append(info.Uploads, OpenAIImagesUpload{FieldName: name, FileName: part.FileName(), ContentType: contentType, Data: data})
			} else {
				return info, fmt.Errorf("%s must be a form field", name)
			}
			_ = part.Close()
		}
	default:
		return info, fmt.Errorf("video requests require application/json or multipart/form-data")
	}
	info.Model, info.Prompt = strings.TrimSpace(fields["model"]), strings.TrimSpace(fields["prompt"])
	if info.Model == "" || info.Prompt == "" {
		return info, fmt.Errorf("model and prompt are required")
	}
	info.DurationSeconds, err = strconv.Atoi(fields["seconds"])
	if err != nil || info.DurationSeconds < 1 || info.DurationSeconds > 600 {
		return info, fmt.Errorf("seconds must be an integer between 1 and 600")
	}
	widthHeight := strings.Split(strings.ToLower(strings.TrimSpace(fields["size"])), "x")
	if len(widthHeight) != 2 {
		return info, fmt.Errorf("size must be WIDTHxHEIGHT")
	}
	w, e1 := strconv.Atoi(widthHeight[0])
	h, e2 := strconv.Atoi(widthHeight[1])
	if e1 != nil || e2 != nil || w <= 0 || h <= 0 || w > 8192 || h > 8192 {
		return info, fmt.Errorf("invalid size")
	}
	if rawResolution, explicit := fields["resolution"]; explicit {
		var ok bool
		info.Resolution, ok = LookupCompatibleVideoBillingResolution(rawResolution)
		if !ok {
			return info, fmt.Errorf("resolution must be 480p, 720p, 768p, 1080p or 2k")
		}
		info.ExplicitResolution = true
	} else {
		switch min(w, h) {
		case 480:
			info.Resolution = "480p"
		case 720:
			info.Resolution = "720p"
		case 1080:
			info.Resolution = "1080p"
		default:
			return info, fmt.Errorf("size must have a 480, 720 or 1080 pixel short edge")
		}
	}
	if ref := fields["input_reference"]; ref != "" {
		info.InputImageURLs = []string{ref}
	}
	info.N = 1
	return info, nil
}

func (s *OpenAIGatewayService) NewCompatibleVideoTask(ctx context.Context, key *APIKey, sub *UserSubscription, account *Account, info CompatibleVideoRequestInfo, body []byte) (*CompatibleVideoTask, error) {
	if key == nil || key.Group == nil || account == nil {
		return nil, errors.New("video group/account is required")
	}
	price := key.Group.GetVideoPriceForModel(info.Model, info.Resolution)
	if price == nil || *price < 0 || math.IsNaN(*price) || math.IsInf(*price, 0) {
		return nil, errors.New("video price is not configured for this model and resolution")
	}
	rate := key.Group.RateMultiplier
	if s.userGroupRateResolver != nil {
		rate = s.userGroupRateResolver.Resolve(ctx, key.UserID, key.Group.ID, rate)
	}
	rate = resolveVideoRateMultiplier(key, rate)
	if rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return nil, errors.New("invalid video rate multiplier")
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	task := &CompatibleVideoTask{ID: CompatibleVideoIDPrefix + hex.EncodeToString(id), UserID: key.UserID, APIKeyID: key.ID, GroupID: key.Group.ID, AccountID: account.ID,
		Model: info.Model, UpstreamModel: account.GetMappedModel(info.Model), Resolution: info.Resolution, Seconds: info.DurationSeconds, RateMultiplier: rate,
		PayloadHash: HashUsageRequestPayload(body), Status: "submitting", CreatedAt: time.Now().UTC(), AccountRateMultiplier: account.BillingRateMultiplier()}
	task.APIKeyQuota, task.APIKeyRateLimit5h, task.APIKeyRateLimit1d, task.APIKeyRateLimit7d = key.Quota, key.RateLimit5h, key.RateLimit1d, key.RateLimit7d
	task.TotalCost = QuantizeUsageBillingAmount(*price * float64(info.DurationSeconds))
	task.ActualCost = QuantizeUsageBillingAmount(task.TotalCost * rate)
	if math.IsInf(task.TotalCost, 0) || math.IsNaN(task.TotalCost) || task.TotalCost > 1e10 || math.IsInf(task.ActualCost, 0) || math.IsNaN(task.ActualCost) || task.ActualCost > 1e10 {
		return nil, errors.New("video price exceeds supported amount")
	}
	if sub != nil && key.Group.IsSubscriptionType() {
		id := sub.ID
		task.SubscriptionID = &id
	}
	task.SimpleMode = s.cfg != nil && s.cfg.RunMode == config.RunModeSimple
	task.SimpleWindowOnly = simpleModeKeyRateLimitBillingEnabled(s.cfg, key)
	if task.SubscriptionID == nil && !task.SimpleMode {
		task.HoldAmount = task.ActualCost
	}
	return task, nil
}

func (s *OpenAIGatewayService) GetCompatibleVideo(ctx context.Context, id string, userID, keyID, groupID int64) (*CompatibleVideoTask, error) {
	repo, err := s.compatibleVideoRepo()
	if err != nil {
		return nil, err
	}
	task, err := repo.GetCompatibleVideo(ctx, id)
	if err != nil {
		return nil, err
	}
	if task.UserID != userID || task.APIKeyID != keyID || task.GroupID != groupID {
		return nil, ErrCompatibleVideoNotFound
	}
	return task, nil
}

func (s *OpenAIGatewayService) compatibleVideoHTTP(ctx context.Context, account *Account, method, upstreamID string, content bool, body []byte, contentType, idempotencyKey string) (*http.Response, error) {
	if account == nil {
		return nil, errors.New("video account is unavailable")
	}
	if method == http.MethodPost && !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityVideoGeneration) {
		return nil, errors.New("account does not support video_generation")
	}
	base, err := s.validateUpstreamBaseURL(account.GetCredential("base_url"))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(account.GetCredential("api_key")) == "" {
		return nil, errors.New("video API key is required")
	}
	target, err := buildCompatibleVideoURL(base, upstreamID, content)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+account.GetCredential("api_key"))
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	account.ApplyHeaderOverrides(req.Header)
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	return s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
}

// Submit once. A transport error is ambiguous: retain the hold and local task ID
// for operator reconciliation rather than resubmitting a potentially billable job.
func (s *OpenAIGatewayService) SubmitCompatibleVideo(ctx context.Context, account *Account, task *CompatibleVideoTask, body []byte, contentType string) ([]byte, int, error) {
	repo, err := s.compatibleVideoRepo()
	if err != nil {
		return nil, 0, err
	}
	// Reject local configuration/body errors before freezing funds. No upstream
	// request has been sent yet, so these outcomes never need reconciliation.
	if account == nil || !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityVideoGeneration) || strings.TrimSpace(account.GetCredential("api_key")) == "" {
		return nil, 0, errors.New("invalid video account configuration")
	}
	if _, err = s.validateUpstreamBaseURL(account.GetCredential("base_url")); err != nil {
		return nil, 0, err
	}
	body, contentType, err = PrepareCompatibleVideoRequest(body, contentType, task.UpstreamModel)
	if err != nil {
		return nil, 0, err
	}
	task.RequestBody = append(task.RequestBody[:0], body...)
	task.ContentType = contentType
	if task.Status != "submission_unknown" {
		if err = repo.CreateCompatibleVideo(ctx, task); err != nil {
			return nil, 0, err
		}
	}
	if s.billingCacheService != nil {
		_ = s.billingCacheService.InvalidateUserBalance(ctx, task.UserID)
		defer func() { _ = s.billingCacheService.InvalidateUserBalance(context.WithoutCancel(ctx), task.UserID) }()
	}
	resp, err := s.compatibleVideoHTTP(ctx, account, http.MethodPost, "", false, body, contentType, task.ID)
	if err != nil {
		markCompatibleVideoSubmissionUnknown(ctx, repo, task.ID)
		return CompatibleVideoErrorBody(task.ID), http.StatusBadGateway, fmt.Errorf("video submission outcome unknown; task %s requires reconciliation: %w", task.ID, err)
	}
	defer resp.Body.Close()
	responseBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, nil, openAITooLargeError)
	if err != nil {
		markCompatibleVideoSubmissionUnknown(ctx, repo, task.ID)
		return CompatibleVideoErrorBody(task.ID), http.StatusBadGateway, fmt.Errorf("video submission response unreadable; task %s requires reconciliation: %w", task.ID, err)
	}
	if resp.StatusCode >= 300 {
		// Only an explicit validation/auth/rate-limit rejection proves no task was accepted.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 408 {
			if err = repo.ReleaseCompatibleVideo(context.WithoutCancel(ctx), task.ID, "failed"); err != nil {
				return nil, 0, err
			}
		}
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout {
			markCompatibleVideoSubmissionUnknown(ctx, repo, task.ID)
			return CompatibleVideoErrorBody(task.ID), http.StatusBadGateway, fmt.Errorf("video submission outcome unknown; task %s requires reconciliation", task.ID)
		}
		return responseBody, resp.StatusCode, fmt.Errorf("video upstream returned %d", resp.StatusCode)
	}
	id := strings.TrimSpace(gjson.GetBytes(responseBody, "id").String())
	if id == "" {
		markCompatibleVideoSubmissionUnknown(ctx, repo, task.ID)
		return CompatibleVideoErrorBody(task.ID), http.StatusBadGateway, fmt.Errorf("video create response missing id; reconcile %s", task.ID)
	}
	if err = validateCompatibleVideoUpstreamID(id); err != nil {
		markCompatibleVideoSubmissionUnknown(ctx, repo, task.ID)
		return CompatibleVideoErrorBody(task.ID), http.StatusBadGateway, err
	}
	// Commit binding before returning success; retry the DB write (never the create).
	err = repo.AcceptCompatibleVideo(context.WithoutCancel(ctx), task.ID, id)
	if err != nil {
		err = repo.AcceptCompatibleVideo(context.WithoutCancel(ctx), task.ID, id)
	}
	if err != nil {
		markCompatibleVideoSubmissionUnknown(ctx, repo, task.ID)
		return nil, 0, fmt.Errorf("video %s accepted as %s but binding failed: %w", task.ID, id, err)
	}
	task.UpstreamID = id
	task.Status = "queued"
	responseBody, err = compatibleVideoResponse(responseBody, task, normalizeCompatibleVideoStatus(gjson.GetBytes(responseBody, "status").String()))
	return responseBody, resp.StatusCode, err
}

func (s *OpenAIGatewayService) PollCompatibleVideo(ctx context.Context, task *CompatibleVideoTask, key *APIKey, updater APIKeyQuotaUpdater) ([]byte, int, error) {
	if task == nil {
		return nil, 0, ErrCompatibleVideoNotFound
	}
	if task.Settled {
		body, _ := json.Marshal(map[string]any{"id": task.ID, "model": task.Model, "status": task.Status})
		return body, http.StatusOK, nil
	}
	if task.UpstreamID == "" {
		status := "submission_unknown"
		if task.Settled {
			status = task.Status
		}
		b, _ := json.Marshal(map[string]any{"id": task.ID, "status": status})
		return b, http.StatusAccepted, nil
	}
	account, err := s.accountRepo.GetByID(ctx, task.AccountID)
	if err != nil {
		return nil, 0, err
	}
	resp, err := s.compatibleVideoHTTP(ctx, account, http.MethodGet, task.UpstreamID, false, nil, "", "")
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, nil, openAITooLargeError)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode >= 300 {
		return body, resp.StatusCode, fmt.Errorf("video status upstream returned %d", resp.StatusCode)
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, 0, errors.New("invalid video status response")
	}
	status := normalizeCompatibleVideoStatus(gjson.GetBytes(body, "status").String())
	repo, err := s.compatibleVideoRepo()
	if err != nil {
		return nil, 0, err
	}
	switch status {
	case "completed":
		if err = s.settleCompatibleVideo(ctx, task, key, account, updater); err != nil {
			return nil, 0, err
		}
	case "failed", "cancelled":
		if err = repo.ReleaseCompatibleVideo(ctx, task.ID, status); err != nil {
			return nil, 0, err
		}
		if s.billingCacheService != nil {
			_ = s.billingCacheService.InvalidateUserBalance(ctx, task.UserID)
		}
	}
	task.Status = status
	if status == "completed" || status == "failed" || status == "cancelled" {
		task.Settled = true
	}
	body, err = compatibleVideoResponse(body, task, status)
	return body, resp.StatusCode, err
}

func (s *OpenAIGatewayService) settleCompatibleVideo(ctx context.Context, task *CompatibleVideoTask, key *APIKey, account *Account, updater APIKeyQuotaUpdater) error {
	if task.Settled {
		return nil
	}
	if key == nil || key.User == nil || key.ID != task.APIKeyID || key.User.ID != task.UserID {
		if s.userRepo == nil {
			return errors.New("video owner user is unavailable")
		}
		user, err := s.userRepo.GetByIDIncludeDeleted(ctx, task.UserID)
		if err != nil || user == nil {
			return errors.New("video owner user is unavailable")
		}
		groupID := task.GroupID
		key = &APIKey{ID: task.APIKeyID, UserID: task.UserID, GroupID: &groupID, User: user,
			Quota: task.APIKeyQuota, RateLimit5h: task.APIKeyRateLimit5h, RateLimit1d: task.APIKeyRateLimit1d, RateLimit7d: task.APIKeyRateLimit7d}
	}
	snapshotKey := *key
	snapshotKey.GroupID = &task.GroupID
	key = &snapshotKey
	actual, total := task.ActualCost, task.TotalCost
	if task.SimpleMode && !task.SimpleWindowOnly {
		actual, total = 0, 0
	}
	// Freeze monetary inputs at submission; later admin price/rate edits cannot alter this task.
	duration := int(time.Since(task.CreatedAt).Milliseconds())
	log := &UsageLog{RequestID: "compatible-video:" + task.ID, UserID: task.UserID, APIKeyID: task.APIKeyID, GroupID: &task.GroupID, AccountID: task.AccountID,
		Model: task.Model, RequestedModel: task.Model, UpstreamModel: &task.UpstreamModel, VideoCount: 1, VideoResolution: &task.Resolution, VideoDurationSeconds: &task.Seconds,
		TotalCost: total, ActualCost: actual, RateMultiplier: task.RateMultiplier, DurationMs: &duration, BillingType: BillingTypeBalance, CreatedAt: task.CreatedAt}
	mode := string(BillingModeVideo)
	log.BillingMode = &mode
	var sub *UserSubscription
	if task.SubscriptionID != nil {
		sub = &UserSubscription{ID: *task.SubscriptionID}
		log.SubscriptionID = task.SubscriptionID
		log.BillingType = BillingTypeSubscription
	}
	_, err := applyUsageBilling(ctx, log.RequestID, log, &postUsageBillingParams{Cost: &CostBreakdown{TotalCost: total, ActualCost: actual, BillingMode: mode},
		User: key.User, APIKey: key, Account: account, Subscription: sub, IsSubscriptionBill: sub != nil && !task.SimpleMode,
		RequestPayloadHash: task.PayloadHash, AccountRateMultiplier: task.AccountRateMultiplier, APIKeyService: updater, Platform: PlatformOpenAI,
		SimpleModeKeyRateLimitOnly: task.SimpleWindowOnly, CompatibleVideoTaskID: task.ID}, s.billingDeps(), s.usageBillingRepo)
	if err != nil {
		return err
	}
	writeUsageLogBestEffort(ctx, s.usageLogRepo, log, "service.compatible_video")
	return nil
}

func (s *OpenAIGatewayService) CompatibleVideoContent(ctx context.Context, task *CompatibleVideoTask) (*http.Response, error) {
	if task.UpstreamID == "" || !task.Settled || task.Status != "completed" {
		return nil, errors.New("video content is only available after successful settlement")
	}
	account, err := s.accountRepo.GetByID(ctx, task.AccountID)
	if err != nil {
		return nil, err
	}
	return s.compatibleVideoHTTP(ctx, account, http.MethodGet, task.UpstreamID, true, nil, "", "")
}

// StartCompatibleVideoWorker recovers accepted jobs after restart and settles
// them even when the client never polls. SQL billing dedup makes replicas safe.
func (s *OpenAIGatewayService) StartCompatibleVideoWorker(keys *APIKeyService) {
	if _, err := s.compatibleVideoRepo(); err != nil {
		return
	}
	s.compatibleVideoWorkerOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.compatibleVideoWorkerCancel = cancel
		go func() {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					repo, _ := s.compatibleVideoRepo()
					tasks, err := repo.ListPendingCompatibleVideos(ctx, 100)
					if err != nil {
						logger.LegacyPrintf("service.compatible_video", "pending scan failed: %v", err)
						continue
					}
					for _, task := range tasks {
						if ctx.Err() != nil {
							return
						}
						runCtx, done := context.WithTimeout(ctx, 30*time.Second)
						if task.UpstreamID == "" {
							if len(task.RequestBody) > 0 {
								account, accountErr := s.accountRepo.GetByID(runCtx, task.AccountID)
								if accountErr == nil {
									_, _, e := s.SubmitCompatibleVideo(runCtx, account, task, task.RequestBody, task.ContentType)
									if e != nil {
										logger.LegacyPrintf("service.compatible_video", "task %s resubmission failed: %v", task.ID, e)
									}
								} else {
									logger.LegacyPrintf("service.compatible_video", "task %s account lookup failed: %v", task.ID, accountErr)
								}
							} else {
								logger.LegacyPrintf("service.compatible_video", "task %s has no persisted request body; manual reconciliation required", task.ID)
							}
						} else {
							_, _, e := s.PollCompatibleVideo(runCtx, task, nil, keys)
							if e != nil {
								logger.LegacyPrintf("service.compatible_video", "task %s reconciliation failed: %v", task.ID, e)
							}
						}
						done()
					}
				}
			}
		}()
	})
}

// Used by handlers when returning a safe JSON error alongside a retained local ID.
func CompatibleVideoErrorBody(id string) []byte {
	b, _ := json.Marshal(map[string]any{"id": id, "error": map[string]string{"type": "api_error", "message": "Video submission or settlement requires reconciliation"}})
	return b
}

// LiteLLM IDs may contain a base64-encoded provider ID. Treat the whole opaque
// value as one URL segment; never interpret it as a path or query string.
func validateCompatibleVideoUpstreamID(id string) error {
	if id == "" || len(id) > 8192 || id == "." || id == ".." {
		return errors.New("invalid video ID")
	}
	for _, c := range []byte(id) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_-.+/=", rune(c))) {
			return errors.New("invalid video ID")
		}
	}
	return nil
}

func buildCompatibleVideoURL(base, id string, content bool) (string, error) {
	endpoint := buildOpenAIEndpointURL(base, "/v1/videos")
	if id == "" {
		if content {
			return "", errors.New("video ID is required")
		}
		return endpoint, nil
	}
	if err := validateCompatibleVideoUpstreamID(id); err != nil {
		return "", err
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	escaped := strings.TrimRight(u.EscapedPath(), "/") + "/" + url.PathEscape(id)
	if content {
		escaped += "/content"
	}
	u.Path, err = url.PathUnescape(escaped)
	if err != nil {
		return "", err
	}
	u.RawPath = escaped
	u.RawQuery = ""
	return u.String(), nil
}

func normalizeCompatibleVideoStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "succeeded", "success", "done":
		return "completed"
	case "failed", "error":
		return "failed"
	case "cancelled", "canceled":
		return "cancelled"
	case "processing", "running", "in_progress":
		return "in_progress"
	case "queued", "pending", "submitted":
		return "queued"
	default:
		return status
	}
}

func compatibleVideoResponse(body []byte, task *CompatibleVideoTask, status string) ([]byte, error) {
	var err error
	body, err = sjson.SetBytes(body, "id", task.ID)
	if err != nil {
		return nil, err
	}
	body, err = sjson.SetBytes(body, "model", task.Model)
	if err != nil {
		return nil, err
	}
	if status != "" {
		body, err = sjson.SetBytes(body, "status", status)
		if err != nil {
			return nil, err
		}
	}
	for _, path := range []string{"download_url", "video_url", "url", "video.url"} {
		if gjson.GetBytes(body, path).Exists() {
			body, err = sjson.SetBytes(body, path, "/v1/videos/"+task.ID+"/content")
			if err != nil {
				return nil, err
			}
		}
	}
	return body, nil
}

// Rewrite only the model; preserve vendor parameters and uploaded file bytes.
func rewriteCompatibleVideoModel(body []byte, contentType, model string) ([]byte, string, error) {
	return rewriteCompatibleVideoFields(body, contentType, map[string]string{"model": model})
}

// PrepareCompatibleVideoRequest enforces the provider-qualified alias contract.
// Resolution is a quality tier; size supplies aspect ratio for these adapters.
// No default resolution may silently disagree with the billing snapshot.
func PrepareCompatibleVideoRequest(body []byte, contentType, upstreamModel string) ([]byte, string, error) {
	info, err := ParseCompatibleVideoRequest(contentType, body)
	if err != nil {
		return nil, "", err
	}
	model := strings.ToLower(upstreamModel)
	minimax := strings.HasPrefix(model, "minimax/")
	seedance := strings.HasPrefix(model, "volcengine/")
	if (minimax || seedance) && !info.ExplicitResolution {
		return nil, "", errors.New("resolution is required for MiniMax and Seedance models")
	}
	if info.ExplicitResolution && !minimax && !seedance {
		return nil, "", errors.New("explicit resolution requires a provider-qualified minimax/ or volcengine/ model mapping; other models use size")
	}
	fields := map[string]string{"model": upstreamModel}
	if info.ExplicitResolution {
		resolution := info.Resolution
		if seedance && resolution != "480p" && resolution != "720p" && resolution != "1080p" {
			return nil, "", errors.New("Seedance resolution must be 480p, 720p or 1080p; availability depends on the model")
		}
		if minimax {
			if model == "minimax/minimax-h3" && resolution != "768p" && resolution != "2k" {
				return nil, "", errors.New("MiniMax H3 resolution must be 768p or 2k")
			}
			if model == "minimax/minimax-h3-max" && resolution != "480p" && resolution != "768p" {
				return nil, "", errors.New("MiniMax H3-Max resolution must be 480p or 768p")
			}
			resolution = strings.ToUpper(resolution)
		}
		fields["resolution"] = resolution
	}
	return rewriteCompatibleVideoFields(body, contentType, fields)
}

// Only reviewed common options are accepted. Native containers can override
// duration/resolution or introduce unmoderated references and are not forwarded.
func compatibleVideoAllowedField(name string) bool {
	switch name {
	case "model", "prompt", "seconds", "size", "resolution", "input_reference", "seed", "watermark", "generate_audio", "camera_fixed", "negative_prompt", "user":
		return true
	default:
		return false
	}
}

func rewriteCompatibleVideoFields(body []byte, contentType string, fields map[string]string) ([]byte, string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, "", err
	}
	if mediaType == "application/json" {
		rewritten := body
		for key, value := range fields {
			rewritten, err = sjson.SetBytes(rewritten, key, value)
			if err != nil {
				return nil, "", err
			}
		}
		return rewritten, contentType, nil
	}
	if mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil, "", errors.New("invalid video content type")
	}
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		target, err := writer.CreatePart(part.Header)
		if err != nil {
			_ = part.Close()
			return nil, "", err
		}
		if value, ok := fields[part.FormName()]; ok && part.FileName() == "" {
			_, err = io.WriteString(target, value)
		} else {
			_, err = io.Copy(target, part)
		}
		_ = part.Close()
		if err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}
