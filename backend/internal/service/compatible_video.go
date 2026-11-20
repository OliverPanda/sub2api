package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
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
const compatibleVideoMaxUploadBytes = 40 << 20

var ErrCompatibleVideoNotFound = errors.New("video task not found")
var ErrCompatibleVideoIdempotencyConflict = errors.New("video idempotency key was reused with different parameters")
var ErrCompatibleVideoReplay = errors.New("video idempotency replay")

// CompatibleVideoTask is an immutable pricing/ownership snapshot plus upstream state.
// The repository atomically reserves, releases and captures the balance hold.
type CompatibleVideoTask struct {
	ID                                                                   string
	UserID, APIKeyID, GroupID, AccountID                                 int64
	UpstreamID, Status                                                   string
	IdempotencyKey                                                       string
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
	GetCompatibleVideoByIdempotency(context.Context, int64, string) (*CompatibleVideoTask, error)
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

func NormalizeCompatibleVideoIdempotencyKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if key == "" {
		return "", nil
	}
	if len(key) > 255 || strings.ContainsAny(key, "\r\n") {
		return "", errors.New("Idempotency-Key must be 255 characters or fewer")
	}
	return key, nil
}

type CompatibleVideoRequestInfo struct {
	GrokMediaRequestInfo
}

// ParseCompatibleVideoRequest validates request and billing parameters before reservation.
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
			case "model", "prompt", "size", "seconds", "duration", "length", "durationSeconds", "duration_seconds", "video_duration", "resolution", "res", "dimensions", "quality", "aspect_ratio", "ratio", "aspectRatio", "input_reference":
				if v.Type != gjson.String && !isCompatibleVideoNumericField(name, v.Type) {
					fieldErr = fmt.Errorf("invalid %s", name)
					return false
				}
				canonical := name
				switch name {
				case "duration", "length", "durationSeconds", "duration_seconds", "video_duration":
					canonical = "seconds"
				case "res", "dimensions", "quality":
					canonical = "resolution"
				case "ratio", "aspectRatio":
					canonical = "aspect_ratio"
				}
				if _, exists := fields[canonical]; exists {
					fieldErr = fmt.Errorf("duplicate field %s", canonical)
					return false
				}
				fields[canonical] = v.String()
			case "images":
				if !v.IsArray() {
					fieldErr = fmt.Errorf("%s must be an array", name)
					return false
				}
				for _, item := range v.Array() {
					if item.Type == gjson.String {
						info.InputImageURLs = append(info.InputImageURLs, item.String())
						continue
					}
					if item.IsObject() {
						for _, path := range []string{"url", "image_url.url"} {
							if imageURL := item.Get(path).String(); imageURL != "" {
								info.InputImageURLs = append(info.InputImageURLs, imageURL)
								break
							}
						}
						continue
					}
					fieldErr = fmt.Errorf("invalid %s item", name)
					return false
				}
			case "videos", "audios":
				fieldErr = fmt.Errorf("%s references are not supported by the gateway moderation policy", name)
				return false
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
			if seen[name] && name != "images" {
				_ = part.Close()
				return info, fmt.Errorf("duplicate field %s", name)
			}
			seen[name] = true
			if part.FileName() == "" {
				if name == "videos" || name == "audios" {
					_ = part.Close()
					return info, fmt.Errorf("%s references are not supported by the gateway moderation policy", name)
				}
				data, e := io.ReadAll(io.LimitReader(part, (1<<20)+1))
				if e != nil {
					return info, e
				}
				if len(data) > 1<<20 {
					return info, fmt.Errorf("video field is too large")
				}
				canonical := name
				switch name {
				case "duration", "length", "durationSeconds", "duration_seconds", "video_duration":
					canonical = "seconds"
				case "res", "dimensions", "quality":
					canonical = "resolution"
				case "ratio", "aspectRatio":
					canonical = "aspect_ratio"
				}
				if _, exists := fields[canonical]; exists {
					return info, fmt.Errorf("duplicate field %s", canonical)
				}
				fields[canonical] = string(data)
			} else if name == "input_reference" || name == "images" {
				data, e := io.ReadAll(io.LimitReader(part, compatibleVideoMaxUploadBytes+1))
				if e != nil {
					return info, e
				}
				if len(data) > compatibleVideoMaxUploadBytes {
					return info, fmt.Errorf("%s file is too large", name)
				}
				contentType := http.DetectContentType(data)
				if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
					return info, fmt.Errorf("%s must be an image", name)
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
	info.DurationSeconds, err = parseCompatibleVideoSeconds(fields["seconds"])
	if err != nil || info.DurationSeconds < 1 || info.DurationSeconds > 3600 {
		return info, fmt.Errorf("seconds must be an integer between 1 and 3600")
	}
	if rawResolution := fields["resolution"]; rawResolution != "" {
		var ok bool
		info.Resolution, ok = LookupCompatibleVideoBillingResolution(rawResolution)
		if !ok {
			return info, fmt.Errorf("resolution must be 480p, 720p, 768p, 1080p, 2k or 4k")
		}
	} else if inferred := compatibleVideoResolutionFromSize(fields["size"]); inferred != "" {
		info.Resolution = inferred
	}
	if size := strings.TrimSpace(fields["size"]); size != "" {
		info.Size = size
		if strings.Contains(size, ":") {
			info.Size = size
		} else if inferred := compatibleVideoResolutionFromSize(size); inferred == "" {
			widthHeight := strings.Split(strings.ToLower(size), "x")
			if len(widthHeight) != 2 {
				return info, fmt.Errorf("size must be WIDTHxHEIGHT or a resolution tier")
			}
			w, e1 := strconv.Atoi(widthHeight[0])
			h, e2 := strconv.Atoi(widthHeight[1])
			if e1 != nil || e2 != nil || w <= 0 || h <= 0 || w > 8192 || h > 8192 {
				return info, fmt.Errorf("invalid size")
			}
		}
	}
	info.AspectRatio = strings.TrimSpace(fields["aspect_ratio"])
	if ref := fields["input_reference"]; ref != "" {
		info.InputImageURLs = []string{ref}
	}
	info.N = 1
	return info, nil
}

func CompatibleVideoRequestFingerprint(info CompatibleVideoRequestInfo, body []byte, contentType string) string {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if mediaType == "application/json" {
		return HashUsageRequestPayload(body)
	}
	parts := []string{info.Model, info.Prompt, strconv.Itoa(info.DurationSeconds), info.Resolution, info.Size, info.AspectRatio, strings.Join(info.InputImageURLs, "\x00")}
	for _, upload := range info.Uploads {
		sum := sha256.Sum256(upload.Data)
		parts = append(parts, upload.FieldName, upload.FileName, hex.EncodeToString(sum[:]))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func parseCompatibleVideoSeconds(raw string) (int, error) {
	raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(raw, "秒"), "s"))
	if raw == "" {
		return 8, nil
	}
	return strconv.Atoi(raw)
}

func isCompatibleVideoNumericField(name string, valueType gjson.Type) bool {
	switch name {
	case "seconds", "duration", "length", "durationSeconds", "duration_seconds", "video_duration":
		return valueType == gjson.Number
	default:
		return false
	}
}

func compatibleVideoResolutionFromSize(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if tier, ok := LookupCompatibleVideoBillingResolution(raw); ok {
		return tier
	}
	parts := strings.Split(raw, "x")
	if len(parts) != 2 {
		return ""
	}
	w, e1 := strconv.Atoi(parts[0])
	h, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil {
		return ""
	}
	switch {
	case w == 854 && h == 480 || w == 480 && h == 854:
		return "480p"
	case w == 1280 && h == 720 || w == 720 && h == 1280:
		return "720p"
	case w == 1366 && h == 768 || w == 768 && h == 1366:
		return "768p"
	case w == 1920 && h == 1080 || w == 1080 && h == 1920:
		return "1080p"
	case w == 2560 && h == 1440 || w == 1440 && h == 2560:
		return "2k"
	case w == 3840 && h == 2160 || w == 2160 && h == 3840:
		return "4k"
	default:
		return ""
	}
}

func defaultCompatibleVideoResolution(group *Group, model string) string {
	for _, tier := range []string{"4k", "2k", "1080p", "768p", "720p", "480p"} {
		if group.GetVideoPriceForModel(model, tier) != nil {
			return tier
		}
	}
	return ""
}

func (s *OpenAIGatewayService) NewCompatibleVideoTask(ctx context.Context, key *APIKey, sub *UserSubscription, account *Account, info CompatibleVideoRequestInfo, body []byte) (*CompatibleVideoTask, error) {
	if key == nil || key.Group == nil || account == nil {
		return nil, errors.New("video group/account is required")
	}
	if info.Resolution == "" {
		info.Resolution = defaultCompatibleVideoResolution(key.Group, info.Model)
		if info.Resolution == "" {
			return nil, errors.New("video price is not configured for this model")
		}
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

func (s *OpenAIGatewayService) GetCompatibleVideoByIdempotency(ctx context.Context, idempotencyKey string, userID, keyID, groupID int64) (*CompatibleVideoTask, error) {
	key, err := NormalizeCompatibleVideoIdempotencyKey(idempotencyKey)
	if err != nil || key == "" {
		return nil, ErrCompatibleVideoNotFound
	}
	repo, err := s.compatibleVideoRepo()
	if err != nil {
		return nil, err
	}
	task, err := repo.GetCompatibleVideoByIdempotency(ctx, keyID, key)
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
	task.ContentType = contentType
	if task.Status != "submission_unknown" {
		if err = repo.CreateCompatibleVideo(ctx, task); err != nil {
			return nil, 0, err
		}
		if task.IdempotencyKey != "" {
			existing, lookupErr := repo.GetCompatibleVideoByIdempotency(ctx, task.APIKeyID, task.IdempotencyKey)
			if lookupErr != nil {
				return nil, 0, lookupErr
			}
			if existing.ID != task.ID {
				body, status := CompatibleVideoReplayResponse(existing)
				return body, status, ErrCompatibleVideoReplay
			}
		}
	}
	if s.billingCacheService != nil {
		_ = s.billingCacheService.InvalidateUserBalance(ctx, task.UserID)
		defer func() { _ = s.billingCacheService.InvalidateUserBalance(context.WithoutCancel(ctx), task.UserID) }()
	}
	idempotencyKey := task.IdempotencyKey
	if idempotencyKey == "" {
		idempotencyKey = task.ID
	}
	resp, err := s.compatibleVideoHTTP(ctx, account, http.MethodPost, "", false, body, contentType, idempotencyKey)
	if err != nil {
		markCompatibleVideoSubmissionUnknown(ctx, repo, task.ID)
		return CompatibleVideoErrorBody(task.ID), http.StatusBadGateway, fmt.Errorf("video submission outcome unknown; task %s requires reconciliation: %w", task.ID, err)
	}
	defer func() { _ = resp.Body.Close() }()
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
		response := map[string]any{"id": task.ID, "model": task.Model, "status": task.Status}
		if task.Status == "completed" {
			response["video_url"] = "/v1/videos/" + task.ID + "/content"
		}
		body, _ := json.Marshal(response)
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
	defer func() { _ = resp.Body.Close() }()
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
							logger.LegacyPrintf("service.compatible_video", "task %s has no upstream ID; manual reconciliation required", task.ID)
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

func CompatibleVideoReplayResponse(task *CompatibleVideoTask) ([]byte, int) {
	if task == nil {
		return CompatibleVideoErrorBody(""), http.StatusBadGateway
	}
	response := map[string]any{"id": task.ID, "model": task.Model, "status": task.Status}
	status := http.StatusAccepted
	if task.Settled {
		status = http.StatusOK
	}
	if task.Status == "completed" {
		response["video_url"] = "/v1/videos/" + task.ID + "/content"
	}
	body, _ := json.Marshal(response)
	return body, status
}

// Upstream IDs are opaque. Treat the whole value as one URL segment rather than
// interpreting it as a path or query string.
func validateCompatibleVideoUpstreamID(id string) error {
	if id == "" || len(id) > 8192 || id == "." || id == ".." {
		return errors.New("invalid video ID")
	}
	for _, c := range []byte(id) {
		allowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_-.+/=", rune(c))
		if !allowed {
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
	rewroteURL := false
	for _, path := range []string{"download_url", "video_url", "url", "video.url"} {
		if gjson.GetBytes(body, path).Exists() {
			body, err = sjson.SetBytes(body, path, "/v1/videos/"+task.ID+"/content")
			if err != nil {
				return nil, err
			}
			rewroteURL = true
		}
	}
	if status == "completed" && !rewroteURL {
		updated, setErr := sjson.SetBytes(body, "video_url", "/v1/videos/"+task.ID+"/content")
		if setErr != nil {
			return nil, setErr
		}
		body = updated
	}
	return body, nil
}

// Rewrite only the model; preserve vendor parameters and uploaded file bytes.
func rewriteCompatibleVideoModel(body []byte, contentType, model string) ([]byte, string, error) {
	return rewriteCompatibleVideoFields(body, contentType, map[string]string{"model": model})
}

// PrepareCompatibleVideoRequest applies the account model mapping while retaining
// the upstream's common video request fields.
func PrepareCompatibleVideoRequest(body []byte, contentType, upstreamModel string) ([]byte, string, error) {
	return rewriteCompatibleVideoFields(body, contentType, map[string]string{"model": upstreamModel})
}

// Only reviewed common options are accepted. Provider-specific containers cannot
// override billed parameters or introduce unmoderated references.
func compatibleVideoAllowedField(name string) bool {
	switch name {
	case "model", "prompt", "seconds", "duration", "length", "durationSeconds", "duration_seconds", "video_duration", "size", "resolution", "res", "dimensions", "quality", "aspect_ratio", "ratio", "aspectRatio", "input_reference", "images", "videos", "audios", "seed", "watermark", "generate_audio", "camera_fixed", "negative_prompt", "user":
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
