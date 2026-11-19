package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
)

const OpenAIEndpointCapabilityMusicGeneration OpenAIEndpointCapability = "music_generation"
const ReplicateMusicIDPrefix = "rmus_"

var ErrReplicateMusicNotCancelable = errors.New("music task is no longer cancelable")

type ReplicateMusicTask struct {
	ID                    string          `json:"id"`
	UserID                int64           `json:"user_id"`
	APIKeyID              int64           `json:"api_key_id"`
	GroupID               int64           `json:"group_id"`
	AccountID             int64           `json:"account_id"`
	UpstreamID            string          `json:"upstream_id"`
	Status                string          `json:"status"`
	Settled               bool            `json:"settled"`
	HoldAmount            float64         `json:"hold_amount"`
	Output                json.RawMessage `json:"output,omitempty"`
	Model                 string          `json:"model"`
	Input                 json.RawMessage `json:"input"`
	Price                 float64         `json:"price"`
	Rate                  float64         `json:"rate"`
	TotalCost             float64         `json:"total_cost"`
	CreatedAt             time.Time       `json:"created_at"`
	SubscriptionID        *int64          `json:"subscription_id,omitempty"`
	SimpleMode            bool            `json:"simple_mode,omitempty"`
	SimpleWindowOnly      bool            `json:"simple_window_only,omitempty"`
	AccountRateMultiplier float64         `json:"account_rate_multiplier"`
	APIKeyQuota           float64         `json:"api_key_quota"`
	APIKeyRateLimit5h     float64         `json:"api_key_rate_limit_5h"`
	APIKeyRateLimit1d     float64         `json:"api_key_rate_limit_1d"`
	APIKeyRateLimit7d     float64         `json:"api_key_rate_limit_7d"`
}

type ReplicateMusicRepository interface {
	CreateReplicateMusic(context.Context, *ReplicateMusicTask) error
	GetReplicateMusic(context.Context, string) (*ReplicateMusicTask, error)
	AcceptReplicateMusic(context.Context, string, string) error
	ReleaseReplicateMusic(context.Context, string, string) error
	ListPendingReplicateMusic(context.Context, int) ([]*ReplicateMusicTask, error)
}

type replicateMusicSubmissionStateRepository interface {
	MarkReplicateMusicSubmissionUnknown(context.Context, string) error
}

func configuredMusicPrice(group *Group, model string) (float64, error) {
	if group == nil {
		return 0, errors.New("music group is required")
	}
	for i := range group.ModelPricing {
		p := &group.ModelPricing[i]
		if p.BillingMode != BillingModePerRequest || p.PerRequestPrice == nil {
			continue
		}
		for _, pattern := range p.Models {
			matched, _ := path.Match(strings.ToLower(pattern), strings.ToLower(model))
			if matched {
				price := *p.PerRequestPrice
				if price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
					return 0, errors.New("invalid music price")
				}
				return price, nil
			}
		}
	}
	return 0, fmt.Errorf("per-request price is not configured for music model %s", model)
}

func newReplicateMusicTask(key *APIKey, sub *UserSubscription, account *Account, model string, input json.RawMessage, cfg *config.Config) (*ReplicateMusicTask, error) {
	if key == nil || key.Group == nil || key.User == nil || account == nil {
		return nil, errors.New("music owner or account is unavailable")
	}
	price, err := configuredMusicPrice(key.Group, model)
	if err != nil {
		return nil, err
	}
	rate := resolveVideoRateMultiplier(key, key.Group.RateMultiplier)
	total := QuantizeUsageBillingAmount(price)
	actual := QuantizeUsageBillingAmount(total * rate)
	if actual < 0 || actual > 1e10 || math.IsNaN(actual) || math.IsInf(actual, 0) {
		return nil, errors.New("music price exceeds supported amount")
	}
	rawID := make([]byte, 16)
	if _, err = rand.Read(rawID); err != nil {
		return nil, err
	}
	task := &ReplicateMusicTask{ID: ReplicateMusicIDPrefix + hex.EncodeToString(rawID), UserID: key.UserID, APIKeyID: key.ID, GroupID: key.Group.ID,
		AccountID: account.ID, Model: model, Input: append(json.RawMessage(nil), input...), Price: price, Rate: rate, TotalCost: actual,
		Status: "submitting", CreatedAt: time.Now().UTC(), AccountRateMultiplier: account.BillingRateMultiplier(), APIKeyQuota: key.Quota, APIKeyRateLimit5h: key.RateLimit5h, APIKeyRateLimit1d: key.RateLimit1d, APIKeyRateLimit7d: key.RateLimit7d,
		SimpleMode: cfg != nil && cfg.RunMode == config.RunModeSimple, SimpleWindowOnly: simpleModeKeyRateLimitBillingEnabled(cfg, key)}
	if sub != nil && key.Group.IsSubscriptionType() {
		id := sub.ID
		task.SubscriptionID = &id
	}
	if task.SubscriptionID == nil && !task.SimpleMode {
		task.HoldAmount = actual
	}
	return task, nil
}

func NewReplicateMusicTask(key *APIKey, sub *UserSubscription, account *Account, model string, input json.RawMessage, cfg *config.Config) (*ReplicateMusicTask, error) {
	return newReplicateMusicTask(key, sub, account, model, input, cfg)
}

func (s *OpenAIGatewayService) replicateMusicRepo() (ReplicateMusicRepository, error) {
	r, ok := s.usageBillingRepo.(ReplicateMusicRepository)
	if !ok {
		return nil, errors.New("music task repository is unavailable")
	}
	return r, nil
}

func markReplicateMusicSubmissionUnknown(ctx context.Context, repo ReplicateMusicRepository, id string) {
	if stateRepo, ok := repo.(replicateMusicSubmissionStateRepository); ok {
		_ = stateRepo.MarkReplicateMusicSubmissionUnknown(context.WithoutCancel(ctx), id)
	}
}

func replicateMusicSubmissionError(id string) []byte {
	b, _ := json.Marshal(map[string]any{
		"id":     id,
		"object": "music.task",
		"status": "submission_unknown",
		"error": map[string]string{
			"type":    "api_error",
			"message": "Music submission outcome is unknown; retry the task query while it is reconciled",
		},
	})
	return b
}

func (s *OpenAIGatewayService) submitReplicateMusic(ctx context.Context, account *Account, task *ReplicateMusicTask) ([]byte, int, error) {
	repo, err := s.replicateMusicRepo()
	if err != nil {
		return nil, 0, err
	}
	if account == nil || !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityMusicGeneration) {
		return nil, 0, errors.New("account does not support music_generation")
	}
	if strings.TrimSpace(account.GetCredential("api_key")) == "" {
		return nil, 0, errors.New("music API key is required")
	}
	base, err := replicateBaseURL(s, account.GetCredential("base_url"))
	if err != nil {
		return nil, 0, err
	}
	parts := strings.Split(task.Model, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, 0, errors.New("model must be an owner/model Replicate identifier")
	}
	endpoint := strings.TrimRight(base, "/") + "/v1/models/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/predictions"
	if task.Status != "submission_unknown" {
		if err = repo.CreateReplicateMusic(ctx, task); err != nil {
			return nil, 0, err
		}
	}
	s.invalidateReplicateMusicBalance(ctx, task.UserID)
	payload, err := json.Marshal(map[string]any{"input": json.RawMessage(task.Input)})
	if err != nil {
		if releaseErr := repo.ReleaseReplicateMusic(context.WithoutCancel(ctx), task.ID, "failed"); releaseErr == nil {
			s.invalidateReplicateMusicBalance(ctx, task.UserID)
		}
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		if releaseErr := repo.ReleaseReplicateMusic(context.WithoutCancel(ctx), task.ID, "failed"); releaseErr == nil {
			s.invalidateReplicateMusicBalance(ctx, task.UserID)
		}
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+account.GetCredential("api_key"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", task.ID)
	account.ApplyHeaderOverrides(req.Header)
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	if err != nil {
		markReplicateMusicSubmissionUnknown(ctx, repo, task.ID)
		return replicateMusicSubmissionError(task.ID), http.StatusBadGateway, fmt.Errorf("music submission outcome unknown: %w", err)
	}
	defer resp.Body.Close()
	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, nil, openAITooLargeError)
	if err != nil {
		markReplicateMusicSubmissionUnknown(ctx, repo, task.ID)
		return replicateMusicSubmissionError(task.ID), http.StatusBadGateway, fmt.Errorf("music submission response unreadable: %w", err)
	}
	if resp.StatusCode >= 300 {
		// An explicit HTTP rejection proves Replicate did not accept the job.
		// Release the reservation so failed creates cannot strand balance.
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout {
			markReplicateMusicSubmissionUnknown(ctx, repo, task.ID)
			return replicateMusicSubmissionError(task.ID), http.StatusBadGateway, fmt.Errorf("music submission outcome unknown: upstream returned %d", resp.StatusCode)
		}
		if err = repo.ReleaseReplicateMusic(context.WithoutCancel(ctx), task.ID, "failed"); err == nil {
			s.invalidateReplicateMusicBalance(ctx, task.UserID)
		}
		return body, resp.StatusCode, fmt.Errorf("Replicate create returned %d", resp.StatusCode)
	}
	id := strings.TrimSpace(gjson.GetBytes(body, "id").String())
	if id == "" {
		markReplicateMusicSubmissionUnknown(ctx, repo, task.ID)
		return replicateMusicSubmissionError(task.ID), http.StatusBadGateway, errors.New("Replicate response is missing prediction id")
	}
	if err = repo.AcceptReplicateMusic(context.WithoutCancel(ctx), task.ID, id); err != nil {
		if retryErr := repo.AcceptReplicateMusic(context.WithoutCancel(ctx), task.ID, id); retryErr == nil {
			err = nil
		}
	}
	if err != nil {
		// The upstream prediction exists; keep the hold for worker/operator
		// reconciliation instead of issuing a second prediction.
		markReplicateMusicSubmissionUnknown(ctx, repo, task.ID)
		return replicateMusicSubmissionError(task.ID), http.StatusBadGateway, err
	}
	task.UpstreamID, task.Status = id, normalizeReplicateMusicStatus(gjson.GetBytes(body, "status").String())
	return replicateMusicResponse(body, task), resp.StatusCode, nil
}

func (s *OpenAIGatewayService) SubmitReplicateMusic(ctx context.Context, account *Account, task *ReplicateMusicTask) ([]byte, int, error) {
	return s.submitReplicateMusic(ctx, account, task)
}

func (s *OpenAIGatewayService) pollReplicateMusic(ctx context.Context, task *ReplicateMusicTask, key *APIKey, updater APIKeyQuotaUpdater) ([]byte, int, error) {
	repo, err := s.replicateMusicRepo()
	if err != nil {
		return nil, 0, err
	}
	if task.Settled {
		b, _ := json.Marshal(map[string]any{"id": task.ID, "object": "music.task", "model": task.Model, "status": task.Status, "output": json.RawMessage(task.Output)})
		return b, http.StatusOK, nil
	}
	if strings.TrimSpace(task.UpstreamID) == "" {
		b := replicateMusicSubmissionError(task.ID)
		return b, http.StatusAccepted, nil
	}
	account, err := s.accountRepo.GetByID(ctx, task.AccountID)
	if err != nil {
		return nil, 0, err
	}
	base, err := replicateBaseURL(s, account.GetCredential("base_url"))
	if err != nil {
		return nil, 0, err
	}
	target := strings.TrimRight(base, "/") + "/v1/predictions/" + url.PathEscape(task.UpstreamID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+account.GetCredential("api_key"))
	account.ApplyHeaderOverrides(req.Header)
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, nil, openAITooLargeError)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode >= 300 {
		return body, resp.StatusCode, fmt.Errorf("Replicate status returned %d", resp.StatusCode)
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return nil, 0, errors.New("invalid Replicate status response")
	}
	status := normalizeReplicateMusicStatus(gjson.GetBytes(body, "status").String())
	if status == "succeeded" {
		task.Output = json.RawMessage(gjson.GetBytes(body, "output").Raw)
		if output, outputErr := musicOutputURL(task.Output); outputErr != nil || !isSafeMusicOutputURL(output) {
			if releaseErr := repo.ReleaseReplicateMusic(ctx, task.ID, "failed"); releaseErr != nil {
				return nil, 0, releaseErr
			}
			s.invalidateReplicateMusicBalance(ctx, task.UserID)
			task.Status, task.Settled = "failed", true
			return replicateMusicResponse(body, task), resp.StatusCode, nil
		}
		if err = s.settleReplicateMusic(ctx, task, key, account, updater); err != nil {
			return nil, 0, err
		}
		task.Settled = true
	} else if status == "failed" || status == "canceled" {
		if err = repo.ReleaseReplicateMusic(ctx, task.ID, status); err != nil {
			return nil, 0, err
		}
		s.invalidateReplicateMusicBalance(ctx, task.UserID)
		task.Settled = true
	}
	task.Status = status
	return replicateMusicResponse(body, task), resp.StatusCode, nil
}

func (s *OpenAIGatewayService) StartReplicateMusicWorker(keys *APIKeyService) {
	if _, err := s.replicateMusicRepo(); err != nil {
		return
	}
	s.replicateMusicWorkerOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.replicateMusicWorkerCancel = cancel
		go func() {
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
				repo, err := s.replicateMusicRepo()
				if err != nil {
					continue
				}
				tasks, err := repo.ListPendingReplicateMusic(ctx, 100)
				if err != nil {
					continue
				}
				for _, task := range tasks {
					runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
					if task.UpstreamID == "" {
						account, accountErr := s.accountRepo.GetByID(runCtx, task.AccountID)
						if accountErr == nil {
							_, _, submitErr := s.SubmitReplicateMusic(runCtx, account, task)
							if submitErr != nil {
								logger.LegacyPrintf("service.replicate_music", "task %s resubmission failed: %v", task.ID, submitErr)
							}
						} else {
							logger.LegacyPrintf("service.replicate_music", "task %s account lookup failed: %v", task.ID, accountErr)
						}
					} else {
						_, _, pollErr := s.PollReplicateMusic(runCtx, task, nil, keys)
						if pollErr != nil {
							logger.LegacyPrintf("service.replicate_music", "task %s reconciliation failed: %v", task.ID, pollErr)
						}
					}
					cancel()
				}
			}
		}()
	})
}

func replicateBaseURL(s *OpenAIGatewayService, raw string) (string, error) {
	base, err := s.validateUpstreamBaseURL(raw)
	if err != nil {
		return "", err
	}
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(strings.ToLower(base), "/v1") {
		base = strings.TrimSuffix(base, "/v1")
	}
	return base, nil
}

func (s *OpenAIGatewayService) PollReplicateMusic(ctx context.Context, task *ReplicateMusicTask, key *APIKey, updater APIKeyQuotaUpdater) ([]byte, int, error) {
	return s.pollReplicateMusic(ctx, task, key, updater)
}

func (s *OpenAIGatewayService) CancelReplicateMusic(ctx context.Context, task *ReplicateMusicTask) error {
	if task == nil || task.UpstreamID == "" {
		return ErrReplicateMusicNotCancelable
	}
	if task.Settled || task.Status == "succeeded" || task.Status == "failed" || task.Status == "canceled" {
		return ErrReplicateMusicNotCancelable
	}
	repo, err := s.replicateMusicRepo()
	if err != nil {
		return err
	}
	account, err := s.accountRepo.GetByID(ctx, task.AccountID)
	if err != nil {
		return err
	}
	base, err := replicateBaseURL(s, account.GetCredential("base_url"))
	if err != nil {
		return err
	}
	target := strings.TrimRight(base, "/") + "/v1/predictions/" + url.PathEscape(task.UpstreamID) + "/cancel"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+account.GetCredential("api_key"))
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("Replicate cancel returned %d", resp.StatusCode)
	}
	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, nil, openAITooLargeError)
	if err != nil {
		return err
	}
	if normalizeReplicateMusicStatus(gjson.GetBytes(body, "status").String()) != "canceled" {
		return errors.New("Replicate did not confirm that the music task was canceled")
	}
	if err = repo.ReleaseReplicateMusic(ctx, task.ID, "canceled"); err == nil {
		s.invalidateReplicateMusicBalance(ctx, task.UserID)
	}
	return err
}

func (s *OpenAIGatewayService) invalidateReplicateMusicBalance(ctx context.Context, userID int64) {
	if s.billingCacheService != nil {
		_ = s.billingCacheService.InvalidateUserBalance(ctx, userID)
	}
}

func (s *OpenAIGatewayService) settleReplicateMusic(ctx context.Context, task *ReplicateMusicTask, key *APIKey, account *Account, updater APIKeyQuotaUpdater) error {
	if key == nil || key.User == nil || key.ID != task.APIKeyID || key.User.ID != task.UserID {
		if s.userRepo == nil {
			return errors.New("music owner is unavailable")
		}
		user, err := s.userRepo.GetByIDIncludeDeleted(ctx, task.UserID)
		if err != nil {
			return err
		}
		gid := task.GroupID
		key = &APIKey{ID: task.APIKeyID, UserID: task.UserID, GroupID: &gid, User: user}
	}
	keyCopy := *key
	gid := task.GroupID
	keyCopy.GroupID = &gid
	keyCopy.Quota = task.APIKeyQuota
	keyCopy.RateLimit5h = task.APIKeyRateLimit5h
	keyCopy.RateLimit1d = task.APIKeyRateLimit1d
	keyCopy.RateLimit7d = task.APIKeyRateLimit7d
	cost := task.TotalCost
	if task.SimpleMode && !task.SimpleWindowOnly {
		cost = 0
	}
	log := &UsageLog{RequestID: "replicate-music:" + task.ID, UserID: task.UserID, APIKeyID: task.APIKeyID, GroupID: &task.GroupID, AccountID: task.AccountID,
		Model: task.Model, RequestedModel: task.Model, TotalCost: cost, ActualCost: cost, RateMultiplier: task.Rate, BillingType: BillingTypeBalance, CreatedAt: task.CreatedAt}
	mode := string(BillingModePerRequest)
	log.BillingMode = &mode
	var sub *UserSubscription
	if task.SubscriptionID != nil {
		sub = &UserSubscription{ID: *task.SubscriptionID}
		log.SubscriptionID = task.SubscriptionID
		log.BillingType = BillingTypeSubscription
	}
	applied, err := applyUsageBilling(ctx, log.RequestID, log, &postUsageBillingParams{ReplicateMusicTaskID: task.ID, ReplicateMusicOutput: task.Output, Cost: &CostBreakdown{TotalCost: cost, ActualCost: cost, BillingMode: mode}, BalanceAlreadyReserved: task.HoldAmount > 0,
		User: keyCopy.User, APIKey: &keyCopy, Account: account, Subscription: sub, IsSubscriptionBill: sub != nil && !task.SimpleMode, RequestPayloadHash: HashUsageRequestPayload(task.Input), AccountRateMultiplier: task.AccountRateMultiplier,
		APIKeyService: updater, Platform: PlatformOpenAI, SimpleModeKeyRateLimitOnly: task.SimpleWindowOnly}, s.billingDeps(), s.usageBillingRepo)
	if err == nil && applied {
		if s.billingCacheService != nil {
			_ = s.billingCacheService.InvalidateUserBalance(ctx, task.UserID)
		}
		writeUsageLogBestEffort(ctx, s.usageLogRepo, log, "service.replicate_music")
	}
	return err
}

func normalizeReplicateMusicStatus(status string) string {
	switch strings.ToLower(status) {
	case "starting", "processing":
		return "processing"
	case "succeeded":
		return "succeeded"
	case "failed":
		return "failed"
	case "canceled", "cancelled":
		return "canceled"
	default:
		return "starting"
	}
}

func replicateMusicResponse(body []byte, task *ReplicateMusicTask) []byte {
	var upstream map[string]any
	_ = json.Unmarshal(body, &upstream)
	status := task.Status
	if status == "" {
		status = normalizeReplicateMusicStatus(gjson.GetBytes(body, "status").String())
	}
	result := map[string]any{"id": task.ID, "object": "music.task", "model": task.Model, "status": status, "output": upstream["output"]}
	if e := upstream["error"]; e != nil {
		result["error"] = e
	}
	b, _ := json.Marshal(result)
	return b
}

func musicOutputURL(raw json.RawMessage) (string, error) {
	var output string
	if json.Unmarshal(raw, &output) == nil && strings.TrimSpace(output) != "" {
		return strings.TrimSpace(output), nil
	}
	var outputs []string
	if json.Unmarshal(raw, &outputs) == nil {
		for _, value := range outputs {
			if strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value), nil
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

func isSafeMusicOutputURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(u.Scheme, "https") && u.Host != "" && u.User == nil
}

func getReplicateMusicTask(ctx context.Context, s *OpenAIGatewayService, id string, userID, keyID, groupID int64) (*ReplicateMusicTask, error) {
	repo, err := s.replicateMusicRepo()
	if err != nil {
		return nil, err
	}
	task, err := repo.GetReplicateMusic(ctx, id)
	if err != nil {
		return nil, err
	}
	if task.UserID != userID || task.APIKeyID != keyID || task.GroupID != groupID {
		return nil, ErrCompatibleVideoNotFound
	}
	return task, nil
}

func (s *OpenAIGatewayService) GetReplicateMusic(ctx context.Context, id string, userID, keyID, groupID int64) (*ReplicateMusicTask, error) {
	return getReplicateMusicTask(ctx, s, id, userID, keyID, groupID)
}
