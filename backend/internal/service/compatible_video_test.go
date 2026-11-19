package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type compatibleVideoTestRepo struct {
	UsageBillingRepository
	tasks                    map[string]*CompatibleVideoTask
	commands                 []*UsageBillingCommand
	applyErr                 error
	acceptFailures, releases int
}

func (r *compatibleVideoTestRepo) CreateCompatibleVideo(_ context.Context, task *CompatibleVideoTask) error {
	if r.tasks == nil {
		r.tasks = map[string]*CompatibleVideoTask{}
	}
	copied := *task
	r.tasks[task.ID] = &copied
	return nil
}
func (r *compatibleVideoTestRepo) GetCompatibleVideo(_ context.Context, id string) (*CompatibleVideoTask, error) {
	task := r.tasks[id]
	if task == nil {
		return nil, ErrCompatibleVideoNotFound
	}
	copied := *task
	return &copied, nil
}
func (r *compatibleVideoTestRepo) AcceptCompatibleVideo(_ context.Context, id, upstream string) error {
	if r.acceptFailures > 0 {
		r.acceptFailures--
		return errors.New("database temporarily unavailable")
	}
	r.tasks[id].UpstreamID = upstream
	r.tasks[id].Status = "queued"
	return nil
}
func (r *compatibleVideoTestRepo) ReleaseCompatibleVideo(_ context.Context, id, status string) error {
	task := r.tasks[id]
	if !task.Settled {
		r.releases++
		task.Settled = true
		task.Status = status
	}
	return nil
}
func (r *compatibleVideoTestRepo) ListPendingCompatibleVideos(context.Context, int) ([]*CompatibleVideoTask, error) {
	return nil, nil
}
func (r *compatibleVideoTestRepo) Apply(_ context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	if r.applyErr != nil {
		return nil, r.applyErr
	}
	task := r.tasks[cmd.CompatibleVideoTaskID]
	if task.Settled {
		return &UsageBillingApplyResult{}, nil
	}
	r.commands = append(r.commands, cmd)
	task.Settled = true
	task.Status = "completed"
	return &UsageBillingApplyResult{Applied: true}, nil
}

type compatibleVideoTestAccounts struct {
	AccountRepository
	account *Account
	ids     []int64
}

func (r *compatibleVideoTestAccounts) GetByID(_ context.Context, id int64) (*Account, error) {
	r.ids = append(r.ids, id)
	if r.account.ID != id {
		return nil, errors.New("wrong bound account")
	}
	return r.account, nil
}

type compatibleVideoTestHTTP struct {
	HTTPUpstream
	client *http.Client
	ids    []int64
}

func (h *compatibleVideoTestHTTP) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	h.ids = append(h.ids, id)
	return h.client.Do(req)
}
func compatibleVideoTestKey() *APIKey {
	groupID := int64(3)
	return &APIKey{ID: 2, UserID: 1, User: &User{ID: 1}, GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformOpenAI, RateMultiplier: 2, VideoModelPrices: map[string]map[string]float64{"video-alias": {"720p": 0.25}}}}
}
func compatibleVideoTestAccount(base string) *Account {
	return &Account{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": base, "api_key": "internal-key", "openai_capabilities": []any{"video_generation"}, "model_mapping": map[string]any{"video-alias": "vendor-model"}}}
}

const compatibleVideoTestBody = `{"model":"video-alias","prompt":"waves","seconds":"8","size":"1280x720","seed":123}`

func TestCompatibleVideoParseAndModelRewrite(t *testing.T) {
	body := []byte(compatibleVideoTestBody)
	info, err := ParseCompatibleVideoRequest("application/json", body)
	require.NoError(t, err)
	require.Equal(t, 8, info.DurationSeconds)
	require.Equal(t, "720p", info.Resolution)
	rewritten, ct, err := rewriteCompatibleVideoModel(body, "application/json", "vendor-model")
	require.NoError(t, err)
	require.Equal(t, "application/json", ct)
	require.Equal(t, "vendor-model", gjson.GetBytes(rewritten, "model").String())
	require.Equal(t, int64(123), gjson.GetBytes(rewritten, "seed").Int())
	for _, invalid := range []string{
		`{"model":"v","prompt":"p","seconds":"8","seconds":"60","size":"1280x720"}`,
		`{"model":"v","prompt":"p","seconds":8.5,"size":"1280x720"}`,
		`{"model":"v","prompt":"p","seconds":8,"size":"1024x1024"}`,
		`{"model":"v","prompt":"p","size":"1280x720"}`,
		`{"model":{},"prompt":"p","seconds":8,"size":"1280x720"}`,
	} {
		_, err = ParseCompatibleVideoRequest("application/json", []byte(invalid))
		require.Error(t, err, invalid)
	}
	var nonImage bytes.Buffer
	nonImageWriter := multipart.NewWriter(&nonImage)
	for k, v := range map[string]string{"model": "v", "prompt": "p", "seconds": "5", "size": "1280x720"} {
		require.NoError(t, nonImageWriter.WriteField(k, v))
	}
	videoPart, err := nonImageWriter.CreateFormFile("input_reference", "reference.mp4")
	require.NoError(t, err)
	_, err = videoPart.Write([]byte("not an image"))
	require.NoError(t, err)
	require.NoError(t, nonImageWriter.Close())
	_, err = ParseCompatibleVideoRequest(nonImageWriter.FormDataContentType(), nonImage.Bytes())
	require.ErrorContains(t, err, "must be an image")
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	for k, v := range map[string]string{"model": "video-alias", "prompt": "waves", "seconds": "20", "size": "720x1280", "seed": "123"} {
		require.NoError(t, writer.WriteField(k, v))
	}
	part, err := writer.CreateFormFile("input_reference", "source.png")
	require.NoError(t, err)
	file := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	_, err = part.Write(file)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	info, err = ParseCompatibleVideoRequest(writer.FormDataContentType(), buffer.Bytes())
	require.NoError(t, err)
	require.Equal(t, 20, info.DurationSeconds)
	require.Equal(t, file, info.Uploads[0].Data)
	rewritten, ct, err = rewriteCompatibleVideoModel(buffer.Bytes(), writer.FormDataContentType(), "vendor-model")
	require.NoError(t, err)
	_, params, err := mime.ParseMediaType(ct)
	require.NoError(t, err)
	form, err := multipart.NewReader(bytes.NewReader(rewritten), params["boundary"]).ReadForm(1024)
	require.NoError(t, err)
	defer form.RemoveAll()
	require.Equal(t, []string{"vendor-model"}, form.Value["model"])
	require.Equal(t, []string{"123"}, form.Value["seed"])
	upload, err := form.File["input_reference"][0].Open()
	require.NoError(t, err)
	defer upload.Close()
	got, err := io.ReadAll(upload)
	require.NoError(t, err)
	require.Equal(t, file, got)
	var duplicate bytes.Buffer
	dupWriter := multipart.NewWriter(&duplicate)
	require.NoError(t, dupWriter.WriteField("seconds", "8"))
	require.NoError(t, dupWriter.WriteField("seconds", "60"))
	require.NoError(t, dupWriter.Close())
	_, err = ParseCompatibleVideoRequest(dupWriter.FormDataContentType(), duplicate.Bytes())
	require.ErrorContains(t, err, "duplicate")
}

func TestCompatibleVideoOpaqueIDURL(t *testing.T) {
	id := "video_" + strings.Repeat("abc", 60) + "/+=="
	for _, base := range []string{"https://gateway.test", "https://gateway.test/v1", "https://gateway.test/v1/videos"} {
		target, err := buildCompatibleVideoURL(base, id, true)
		require.NoError(t, err)
		require.Equal(t, "https://gateway.test/v1/videos/"+strings.ReplaceAll(id, "/", "%2F")+"/content", target)
	}
	for _, id := range []string{"..", ".", "x?query=bad", "x#frag", "%2e%2e", "x\\y", "\n", strings.Repeat("a", 8193)} {
		_, err := buildCompatibleVideoURL("https://gateway.test", id, false)
		require.Error(t, err, id)
	}
}

func TestCompatibleVideoPricingSnapshotsAndModes(t *testing.T) {
	info, err := ParseCompatibleVideoRequest("application/json", []byte(compatibleVideoTestBody))
	require.NoError(t, err)
	key := compatibleVideoTestKey()
	account := compatibleVideoTestAccount("https://gateway.test")
	svc := &OpenAIGatewayService{}
	task, err := svc.NewCompatibleVideoTask(context.Background(), key, nil, account, info, []byte(compatibleVideoTestBody))
	require.NoError(t, err)
	require.Equal(t, 2.0, task.TotalCost)
	require.Equal(t, 4.0, task.ActualCost)
	require.Equal(t, 4.0, task.HoldAmount)
	require.Equal(t, "vendor-model", task.UpstreamModel)
	key.Group.VideoModelPrices = nil
	_, err = svc.NewCompatibleVideoTask(context.Background(), key, nil, account, info, nil)
	require.ErrorContains(t, err, "not configured")
	fallback := 0.5
	key.Group.VideoPrice720P = &fallback
	task, err = svc.NewCompatibleVideoTask(context.Background(), key, nil, account, info, nil)
	require.NoError(t, err)
	require.Equal(t, 8.0, task.ActualCost)
	key.Group.SubscriptionType = SubscriptionTypeSubscription
	task, err = svc.NewCompatibleVideoTask(context.Background(), key, &UserSubscription{ID: 7}, account, info, nil)
	require.NoError(t, err)
	require.Zero(t, task.HoldAmount)
	require.Equal(t, int64(7), *task.SubscriptionID)
	svc.cfg = &config.Config{RunMode: config.RunModeSimple}
	task, err = svc.NewCompatibleVideoTask(context.Background(), key, nil, account, info, nil)
	require.NoError(t, err)
	require.Zero(t, task.HoldAmount)
	require.True(t, task.SimpleMode)
}

func TestCompatibleVideoHTTPContractAndBoundOwnership(t *testing.T) {
	upstreamID := "video_" + strings.Repeat("YWJj", 40) + "/+=="
	status := "processing"
	contentCalls := 0
	postCalls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer internal-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			postCalls++
			require.Equal(t, "/v1/videos", r.RequestURI)
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Equal(t, "vendor-model", gjson.GetBytes(body, "model").String())
			require.Equal(t, "8", gjson.GetBytes(body, "seconds").String())
			fmt.Fprintf(w, `{"id":%q,"status":"queued","model":"vendor-model","download_url":"https://upstream.invalid/private"}`, upstreamID)
		case strings.HasSuffix(r.RequestURI, "/content"):
			contentCalls++
			require.Contains(t, r.RequestURI, "%2F")
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video-bytes"))
		default:
			require.Equal(t, "/v1/videos/"+strings.ReplaceAll(upstreamID, "/", "%2F"), r.RequestURI)
			fmt.Fprintf(w, `{"id":%q,"status":%q,"video_url":"https://upstream.invalid/private"}`, upstreamID, status)
		}
	}))
	defer server.Close()
	repo := &compatibleVideoTestRepo{acceptFailures: 1}
	account := compatibleVideoTestAccount(server.URL)
	accounts := &compatibleVideoTestAccounts{account: account}
	transport := &compatibleVideoTestHTTP{client: server.Client()}
	svc := &OpenAIGatewayService{usageBillingRepo: repo, accountRepo: accounts, httpUpstream: transport, deferredService: &DeferredService{}}
	key := compatibleVideoTestKey()
	info, err := ParseCompatibleVideoRequest("application/json", []byte(compatibleVideoTestBody))
	require.NoError(t, err)
	task, err := svc.NewCompatibleVideoTask(context.Background(), key, nil, account, info, []byte(compatibleVideoTestBody))
	require.NoError(t, err)
	created, code, err := svc.SubmitCompatibleVideo(context.Background(), account, task, []byte(compatibleVideoTestBody), "application/json")
	require.NoError(t, err)
	require.Equal(t, 200, code)
	require.Equal(t, 1, postCalls)
	require.Equal(t, task.ID, gjson.GetBytes(created, "id").String())
	require.NotContains(t, string(created), upstreamID)
	require.NotContains(t, string(created), "upstream.invalid")
	require.Equal(t, upstreamID, repo.tasks[task.ID].UpstreamID)
	for _, owner := range [][3]int64{{9, 2, 3}, {1, 9, 3}, {1, 2, 9}} {
		_, err = svc.GetCompatibleVideo(context.Background(), task.ID, owner[0], owner[1], owner[2])
		require.ErrorIs(t, err, ErrCompatibleVideoNotFound)
	}
	task, err = svc.GetCompatibleVideo(context.Background(), task.ID, 1, 2, 3)
	require.NoError(t, err)
	polled, _, err := svc.PollCompatibleVideo(context.Background(), task, key, nil)
	require.NoError(t, err)
	require.Equal(t, "in_progress", gjson.GetBytes(polled, "status").String())
	require.Empty(t, repo.commands)
	_, err = svc.CompatibleVideoContent(context.Background(), task)
	require.Error(t, err)
	require.Zero(t, contentCalls)
	status = "succeeded"
	repo.applyErr = errors.New("billing unavailable")
	_, _, err = svc.PollCompatibleVideo(context.Background(), task, key, nil)
	require.Error(t, err)
	require.False(t, task.Settled)
	_, err = svc.CompatibleVideoContent(context.Background(), task)
	require.Error(t, err)
	repo.applyErr = nil
	key.Group.VideoModelPrices["video-alias"]["720p"] = 100
	_, _, err = svc.PollCompatibleVideo(context.Background(), task, key, nil)
	require.NoError(t, err)
	require.True(t, task.Settled)
	_, _, err = svc.PollCompatibleVideo(context.Background(), task, key, nil)
	require.NoError(t, err)
	require.Len(t, repo.commands, 1)
	require.Equal(t, 4.0, repo.commands[0].BalanceCost)
	require.Equal(t, "compatible-video:"+task.ID, repo.commands[0].RequestID)
	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "reused-client-id")
	require.Equal(t, "compatible-video:"+task.ID, resolveUsageBillingRequestID(ctx, repo.commands[0].RequestID))
	response, err := svc.CompatibleVideoContent(context.Background(), task)
	require.NoError(t, err)
	defer response.Body.Close()
	video, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, "video-bytes", string(video))
	require.Equal(t, 1, contentCalls)
	for _, id := range append(accounts.ids, transport.ids...) {
		require.Equal(t, int64(4), id)
	}
}

func TestCompatibleVideoFailureAndAmbiguousSubmission(t *testing.T) {
	for _, code := range []int{400, 429, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				_, _ = w.Write([]byte(`{"error":"rejected"}`))
			}))
			defer server.Close()
			repo := &compatibleVideoTestRepo{}
			account := compatibleVideoTestAccount(server.URL)
			svc := &OpenAIGatewayService{usageBillingRepo: repo, httpUpstream: &compatibleVideoTestHTTP{client: server.Client()}}
			task := &CompatibleVideoTask{ID: "mvid_failure", UpstreamModel: "vendor-model"}
			body, status, err := svc.SubmitCompatibleVideo(context.Background(), account, task, []byte(compatibleVideoTestBody), "application/json")
			require.Error(t, err)
			require.Equal(t, code < 500, repo.tasks[task.ID].Settled)
			if code >= 500 {
				require.Equal(t, http.StatusBadGateway, status)
				require.Equal(t, task.ID, gjson.GetBytes(body, "id").String())
				require.Contains(t, string(body), "reconciliation")
			}
		})
	}
	for _, status := range []string{"failed", "canceled", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"id":"provider-id","status":%q}`, status)
			}))
			defer server.Close()
			task := &CompatibleVideoTask{ID: "mvid_failed", UpstreamID: "provider-id", AccountID: 4}
			repo := &compatibleVideoTestRepo{tasks: map[string]*CompatibleVideoTask{task.ID: task}}
			svc := &OpenAIGatewayService{usageBillingRepo: repo, accountRepo: &compatibleVideoTestAccounts{account: compatibleVideoTestAccount(server.URL)}, httpUpstream: &compatibleVideoTestHTTP{client: server.Client()}}
			for i := 0; i < 2; i++ {
				_, _, err := svc.PollCompatibleVideo(context.Background(), task, compatibleVideoTestKey(), nil)
				require.NoError(t, err)
			}
			require.Empty(t, repo.commands)
			require.Equal(t, 1, repo.releases)
			require.True(t, task.Settled)
		})
	}
}

func TestCompatibleVideoSettledPollUsesLocalState(t *testing.T) {
	task := &CompatibleVideoTask{ID: "mvid_done", Model: "video-alias", Status: "completed", Settled: true}
	svc := &OpenAIGatewayService{}
	body, status, err := svc.PollCompatibleVideo(context.Background(), task, nil, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "completed", gjson.GetBytes(body, "status").String())
	require.Equal(t, task.ID, gjson.GetBytes(body, "id").String())
}

func TestReplicateMusicOutputURLRequiresSafeHTTPS(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		ok   bool
	}{
		{"https", `"https://cdn.example/audio.mp3"`, true},
		{"http", `"http://cdn.example/audio.mp3"`, false},
		{"userinfo", `"https://user:pass@cdn.example/audio.mp3"`, false},
		{"missing", `null`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, err := musicOutputURL([]byte(tc.raw))
			require.Equal(t, tc.ok, err == nil && isSafeMusicOutputURL(value))
		})
	}
}

func TestReplicateMusicResponseUsesPersistedTaskStatus(t *testing.T) {
	task := &ReplicateMusicTask{ID: "rmus_test", Model: "owner/model", Status: "failed"}
	body := replicateMusicResponse([]byte(`{"status":"succeeded","output":"https://cdn.example/audio.mp3"}`), task)
	require.Equal(t, "failed", gjson.GetBytes(body, "status").String())
}

func TestGroupAllowsVideoGenerationDoesNotDependOnImageEntitlement(t *testing.T) {
	group := &Group{AllowImageGeneration: false}
	require.False(t, GroupAllowsVideoGeneration(group))
	price := .1
	group.VideoPrice720P = &price
	require.True(t, GroupAllowsVideoGeneration(group))
	require.False(t, GroupAllowsImageGeneration(group))
}

func TestCompatibleVideoCapabilityMustBeExplicit(t *testing.T) {
	account := compatibleVideoTestAccount("https://gateway.test")
	require.True(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityVideoGeneration))
	delete(account.Credentials, "openai_capabilities")
	require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityVideoGeneration))
	account.Credentials["openai_capabilities"] = []string{"video_generation"}
	account.Type = AccountTypeOAuth
	require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityVideoGeneration))
	account.Type = AccountTypeAPIKey
	account.Credentials["base_url"] = ""
	require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityVideoGeneration))
}

func TestCompatibleVideoSettlementHonorsSubmissionBillingMode(t *testing.T) {
	for _, mode := range []string{"subscription", "simple", "simple_windows"} {
		t.Run(mode, func(t *testing.T) {
			key := compatibleVideoTestKey()
			account := compatibleVideoTestAccount("https://gateway.test")
			repo := &compatibleVideoTestRepo{}
			svc := &OpenAIGatewayService{usageBillingRepo: repo, deferredService: &DeferredService{}, billingCacheService: &BillingCacheService{}}
			// These tests exercise billing-command construction without involving any cache backend.
			task := &CompatibleVideoTask{ID: "mvid_" + mode, UserID: key.User.ID, APIKeyID: key.ID, GroupID: 3, AccountID: account.ID, TotalCost: 2, ActualCost: 4, RateMultiplier: 2}
			switch mode {
			case "subscription":
				id := int64(7)
				task.SubscriptionID = &id

			case "simple":
				task.SimpleMode = true
			case "simple_windows":
				task.SimpleMode = true
				task.SimpleWindowOnly = true
				key.RateLimit5h = 10
			}
			require.NoError(t, repo.CreateCompatibleVideo(context.Background(), task))
			// With no cache backend, simple window invalidation returns harmlessly.
			require.NoError(t, svc.settleCompatibleVideo(context.Background(), task, key, account, nil))
			require.Len(t, repo.commands, 1)
			cmd := repo.commands[0]
			require.Zero(t, cmd.BalanceCost)
			require.Zero(t, cmd.AccountQuotaCost)
			require.Zero(t, cmd.APIKeyQuotaCost)
			if mode == "simple_windows" {
				require.Equal(t, 4.0, cmd.APIKeyRateLimitCost)
			} else {
				require.Zero(t, cmd.APIKeyRateLimitCost)
			}
			if mode == "subscription" {
				require.NotNil(t, cmd.SubscriptionID)
				require.Equal(t, 4.0, cmd.SubscriptionCost)
			}
		})
	}
}

func TestCompatibleVideoSchedulerSelectsOnlyOptedInAccount(t *testing.T) {
	ctx := context.Background()
	groupID := int64(1001)
	ordinary := *compatibleVideoTestAccount("https://text.example")
	ordinary.ID = 1
	delete(ordinary.Credentials, "openai_capabilities")
	video := *compatibleVideoTestAccount("https://litellm.example")
	for _, a := range []*Account{&ordinary, &video} {
		a.Status = StatusActive
		a.Schedulable = true
		a.Concurrency = 1
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{ordinary, video}}, cache: &schedulerTestGatewayCache{}, cfg: cfg, concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{})}
	selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &groupID, "", "", "video-alias", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityVideoGeneration, false, false, false, PlatformOpenAI)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, video.ID, selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestCompatibleVideoSeedanceAndMinimaxContract(t *testing.T) {
	for _, tc := range []struct {
		name, model, resolution, size, upstreamResolution string
		price                                             float64
	}{
		{"seedance-square", "volcengine/doubao-seedance-2-0-260128", "720p", "960x960", "720p", .1},
		{"h3-768", "minimax/MiniMax-H3", "768p", "1366x768", "768P", .2},
		{"h3-2k", "minimax/MiniMax-H3", "2K", "2560x1440", "2K", .4},
		{"h3-max-480", "minimax/MiniMax-H3-Max", "480p", "854x480", "480P", .05},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":"video-alias","prompt":"waves","seconds":5,"size":%q,"resolution":%q}`, tc.size, tc.resolution))
			info, err := ParseCompatibleVideoRequest("application/json", body)
			require.NoError(t, err)
			key := compatibleVideoTestKey()
			key.Group.VideoModelPrices = NormalizeVideoModelPrices(map[string]map[string]float64{"video-alias": {tc.resolution: tc.price}})
			task, err := (&OpenAIGatewayService{}).NewCompatibleVideoTask(context.Background(), key, nil, compatibleVideoTestAccount("https://gateway.test"), info, body)
			require.NoError(t, err)
			require.InDelta(t, tc.price*5, task.TotalCost, 1e-9)
			require.InDelta(t, tc.price*10, task.HoldAmount, 1e-9)
			prepared, _, err := PrepareCompatibleVideoRequest(body, "application/json", tc.model)
			require.NoError(t, err)
			require.Equal(t, tc.model, gjson.GetBytes(prepared, "model").String())
			require.Equal(t, tc.upstreamResolution, gjson.GetBytes(prepared, "resolution").String())
			require.Equal(t, int64(task.Seconds), gjson.GetBytes(prepared, "seconds").Int())
			require.Equal(t, strings.ToLower(tc.resolution), task.Resolution)
			key.Group.VideoModelPrices = nil
			flatPrice := .01
			key.Group.VideoPrice480P = &flatPrice
			if task.Resolution == "768p" || task.Resolution == "2k" {
				_, err = (&OpenAIGatewayService{}).NewCompatibleVideoTask(context.Background(), key, nil, compatibleVideoTestAccount("https://gateway.test"), info, body)
				require.ErrorContains(t, err, "price is not configured")
			}
		})
	}
	for _, model := range []string{"minimax/MiniMax-H3", "volcengine/doubao-seedance-2-0-260128"} {
		_, _, err := PrepareCompatibleVideoRequest([]byte(compatibleVideoTestBody), "application/json", model)
		require.ErrorContains(t, err, "resolution is required")
	}
}

func TestCompatibleVideoRejectsBillingOverridesAndUnmoderatedReferences(t *testing.T) {
	for _, field := range []string{
		`"duration":60`, `"parameters":{"resolution":"2K"}`, `"extra_body":{"seconds":60}`,
		`"api_base":"https://other.test"`, `"api_key":"other"`, `"model_info":{}`,
		`"content":[{"type":"video_url","video_url":{"url":"https://other.test/v.mp4"}}]`,
		`"input_references":["https://other.test/image.png"]`, `"n":2`, `"input_reference":{}`,
	} {
		body := []byte(`{"model":"v","prompt":"p","seconds":5,"size":"1280x720",` + field + `}`)
		_, err := ParseCompatibleVideoRequest("application/json", body)
		require.Error(t, err, field)
	}
	for _, field := range []string{"duration", "parameters", "extra_body", "api_base", "content", "n"} {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		for k, v := range map[string]string{"model": "v", "prompt": "p", "seconds": "5", "size": "1280x720", field: "60"} {
			require.NoError(t, w.WriteField(k, v))
		}
		require.NoError(t, w.Close())
		_, err := ParseCompatibleVideoRequest(w.FormDataContentType(), buf.Bytes())
		require.Error(t, err, field)
	}
}

func TestCompatibleVideoMinimaxMultipartResolution(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range map[string]string{"model": "video-alias", "prompt": "p", "seconds": "5", "size": "1366x768", "resolution": "768p"} {
		require.NoError(t, w.WriteField(k, v))
	}
	p, err := w.CreateFormFile("input_reference", "frame.png")
	require.NoError(t, err)
	imageBytes := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	_, err = p.Write(imageBytes)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	body, ct, err := PrepareCompatibleVideoRequest(buf.Bytes(), w.FormDataContentType(), "minimax/MiniMax-H3")
	require.NoError(t, err)
	info, err := ParseCompatibleVideoRequest(ct, body)
	require.NoError(t, err)
	require.Equal(t, "768p", info.Resolution)
	require.Equal(t, "minimax/MiniMax-H3", info.Model)
	require.Equal(t, imageBytes, info.Uploads[0].Data)
}
