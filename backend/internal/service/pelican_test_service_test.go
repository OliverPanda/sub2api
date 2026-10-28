package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExtractPelicanSVG(t *testing.T) {
	t.Parallel()

	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><circle cx="50" cy="50" r="40"/></svg>`

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{name: "markdown fenced", raw: "```svg\n" + svg + "\n```", want: svg},
		{name: "bare document", raw: svg, want: svg},
		{name: "surrounding explanation", raw: "Here is the drawing:\n\n" + svg + "\n\nIt is a pelican.", want: svg},
		{name: "empty output", raw: " \n\t", wantErr: "no output"},
		{name: "missing svg", raw: "I cannot draw that.", wantErr: "does not contain"},
		{name: "truncated svg", raw: `<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0"/>`, wantErr: "incomplete"},
		{name: "nested svg", raw: `<div><svg></svg></div>`, want: `<svg></svg>`},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ExtractPelicanSVG(tt.raw)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				require.Empty(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestSelectPelicanTestAccounts(t *testing.T) {
	t.Parallel()

	active := []Account{
		{ID: 1, Name: "first"},
		{ID: 2, Name: "second"},
	}

	selected, skipped, total := selectPelicanTestAccounts(active, nil)
	require.Len(t, selected, 2)
	require.Zero(t, skipped)
	require.Equal(t, 2, total)

	selected, skipped, total = selectPelicanTestAccounts(active, []int64{2, 2, 99, 0, -1, 1})
	require.Len(t, selected, 2)
	require.Equal(t, int64(2), selected[0].ID)
	require.Equal(t, int64(1), selected[1].ID)
	require.Equal(t, 1, skipped)
	require.Equal(t, 3, total)
}

func TestPelicanPromptPayloads(t *testing.T) {
	t.Parallel()

	const prompt = "draw a pelican riding a bicycle as SVG"

	claudePayload, err := createTestPayloadWithPrompt("claude-test", prompt)
	require.NoError(t, err)
	messages, ok := claudePayload["messages"].([]map[string]any)
	require.True(t, ok)
	content, ok := messages[0]["content"].([]map[string]any)
	require.True(t, ok)
	require.Equal(t, prompt, content[0]["text"])
	require.Equal(t, 8192, claudePayload["max_tokens"])

	claudeLegacyPayload, err := createTestPayloadWithPrompt("claude-test", "")
	require.NoError(t, err)
	legacyMessages, ok := claudeLegacyPayload["messages"].([]map[string]any)
	require.True(t, ok)
	legacyContent, ok := legacyMessages[0]["content"].([]map[string]any)
	require.True(t, ok)
	require.Equal(t, "hi", legacyContent[0]["text"])
	require.Equal(t, 1024, claudeLegacyPayload["max_tokens"])

	responsesPayload := createOpenAITestPayloadWithPrompt("gpt-test", false, prompt)
	responsesInput, ok := responsesPayload["input"].([]map[string]any)
	require.True(t, ok)
	responsesContent, ok := responsesInput[0]["content"].([]map[string]any)
	require.True(t, ok)
	require.Equal(t, prompt, responsesContent[0]["text"])
	require.Equal(t, 8192, responsesPayload["max_output_tokens"])

	responsesLegacy := createOpenAITestPayloadWithPrompt("gpt-test", false, "")
	legacyInput, ok := responsesLegacy["input"].([]map[string]any)
	require.True(t, ok)
	legacyContentOpenAI, ok := legacyInput[0]["content"].([]map[string]any)
	require.True(t, ok)
	require.Equal(t, "hi", legacyContentOpenAI[0]["text"])
	require.NotContains(t, responsesLegacy, "max_output_tokens")

	chatPayload := createOpenAIChatCompletionsTestPayload("chat-test", prompt)
	chatMessages, ok := chatPayload["messages"].([]map[string]any)
	require.True(t, ok)
	require.Equal(t, prompt, chatMessages[0]["content"])

	var geminiPayload map[string]any
	require.NoError(t, json.Unmarshal(createGeminiTestPayload("gemini-test", prompt), &geminiPayload))
	geminiContents, ok := geminiPayload["contents"].([]any)
	require.True(t, ok)
	geminiContent, ok := geminiContents[0].(map[string]any)
	require.True(t, ok)
	geminiParts, ok := geminiContent["parts"].([]any)
	require.True(t, ok)
	geminiPart, ok := geminiParts[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, prompt, geminiPart["text"])
}

func TestBuildGrokTestProbeBody(t *testing.T) {
	t.Parallel()

	body, err := buildGrokTestProbeBody("grok-test", "draw a pelican as SVG")
	require.NoError(t, err)
	require.JSONEq(t, `{"model":"grok-test","input":"draw a pelican as SVG","stream":true}`, string(body))

	legacyBody, err := buildGrokTestProbeBody("grok-test", "")
	require.NoError(t, err)
	quotaBody, err := buildGrokQuotaProbeBody("grok-test")
	require.NoError(t, err)
	require.JSONEq(t, string(quotaBody), string(legacyBody))
}

func TestPelicanTestServiceDeleteRunRejectsRunningBatch(t *testing.T) {
	t.Parallel()

	repo := &pelicanTestRepoStub{
		runDetail: &PelicanTestRunDetail{PelicanTestRun: PelicanTestRun{ID: 7, Status: PelicanTestStatusRunning}},
	}
	svc := &PelicanTestService{repo: repo}

	err := svc.DeleteRun(context.Background(), 7)
	require.ErrorContains(t, err, "running test cannot be deleted")
	require.False(t, repo.deleted)
}

func TestPelicanTestServiceStartRecoversRunningBatches(t *testing.T) {
	t.Parallel()

	repo := &pelicanTestRepoStub{recovered: 2}
	svc := NewPelicanTestService(repo, nil, nil, nil)
	require.NoError(t, svc.Start(context.Background()))
	require.Equal(t, int64(2), repo.recovered)
	require.Equal(t, pelicanTestRecoveryMessage, repo.recoveryMessage)
	require.WithinDuration(t, time.Now().Add(-pelicanTestLeaseTTL), repo.recoveryBefore, time.Second)

	// Recovery is a startup-only operation and must not run again when Start is
	// called defensively a second time.
	require.NoError(t, svc.Start(context.Background()))
	require.Equal(t, 1, repo.recoveryCalls)

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, svc.Stop(stopCtx))
}

func TestPelicanTestServiceEnforcesSingleRun(t *testing.T) {
	t.Parallel()

	repo := &pelicanTestRepoStub{}
	svc := NewPelicanTestService(repo, nil, nil, nil)
	require.NoError(t, svc.Start(context.Background()))

	releaseFirst, err := svc.registerRun(context.Background())
	require.NoError(t, err)
	_, err = svc.registerRun(context.Background())
	require.ErrorIs(t, err, ErrPelicanTestRunInProgress)

	releaseFirst()
	releaseSecond, err := svc.registerRun(context.Background())
	require.NoError(t, err)
	releaseSecond()

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, svc.Stop(stopCtx))
}

func TestPelicanTestServiceTimeoutPersistsPendingResultsAndFinishesRun(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	repo := &pelicanTestRepoStub{}
	tester := &pelicanTestRunnerStub{release: release}
	svc := NewPelicanTestService(repo, nil, nil, tester)
	require.NoError(t, svc.Start(context.Background()))
	svc.batchTimeout = 20 * time.Millisecond
	svc.persistTimeout = time.Second

	groupID := int64(9)
	run := &PelicanTestRun{ID: 11, GroupID: &groupID, ModelID: "test-model", RunnerID: "test-runner"}
	accounts := []Account{{ID: 21, Name: "stuck-account", Platform: "openai"}}

	svc.runWG.Add(1)
	done := make(chan struct{})
	go func() {
		defer svc.runWG.Done()
		svc.executeRun(run, accounts)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("executeRun waited for a worker that ignored context cancellation")
	}
	close(release)

	repo.mu.Lock()
	require.Len(t, repo.results, 1)
	require.Equal(t, int64(21), *repo.results[0].AccountID)
	require.Equal(t, PelicanTestResultFailed, repo.results[0].Status)
	require.Contains(t, repo.results[0].ErrorMessage, "timed out")
	require.Equal(t, PelicanTestStatusFailed, repo.finishStatus)
	require.Contains(t, repo.finishError, "timed out")
	require.Equal(t, "test-runner", repo.finishRunnerID)
	require.False(t, repo.persistContextCanceled)
	repo.mu.Unlock()

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, svc.Stop(stopCtx))
}

func TestPelicanTestServiceStopsRunWhenLeaseIsLost(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	repo := &pelicanTestRepoStub{heartbeatLost: true}
	tester := &pelicanTestRunnerStub{release: release}
	svc := NewPelicanTestService(repo, nil, nil, tester)
	require.NoError(t, svc.Start(context.Background()))
	svc.batchTimeout = time.Second
	svc.persistTimeout = time.Second
	svc.heartbeatEvery = 5 * time.Millisecond

	groupID := int64(9)
	run := &PelicanTestRun{ID: 12, GroupID: &groupID, ModelID: "test-model", RunnerID: "test-runner"}
	accounts := []Account{{ID: 22, Name: "stuck-account", Platform: "openai"}}

	svc.runWG.Add(1)
	done := make(chan struct{})
	go func() {
		defer svc.runWG.Done()
		svc.executeRun(run, accounts)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("executeRun did not stop after losing its runner lease")
	}
	close(release)

	repo.mu.Lock()
	require.Equal(t, PelicanTestStatusFailed, repo.finishStatus)
	require.Contains(t, repo.finishError, "lease")
	require.Len(t, repo.results, 1)
	require.Contains(t, repo.results[0].ErrorMessage, "lease")
	repo.mu.Unlock()

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, svc.Stop(stopCtx))
}

type pelicanTestRepoStub struct {
	mu                     sync.Mutex
	runDetail              *PelicanTestRunDetail
	deleted                bool
	recovered              int64
	recoveryMessage        string
	recoveryBefore         time.Time
	recoveryCalls          int
	results                []PelicanTestResult
	finishStatus           string
	finishError            string
	finishRunnerID         string
	heartbeatLost          bool
	persistContextCanceled bool
}

func (r *pelicanTestRepoStub) CreateRun(_ context.Context, run *PelicanTestRun) (*PelicanTestRun, error) {
	created := *run
	created.ID = 1
	return &created, nil
}

func (r *pelicanTestRepoStub) GetRun(_ context.Context, _ int64) (*PelicanTestRunDetail, error) {
	if r.runDetail == nil {
		return nil, errors.New("not found")
	}
	return r.runDetail, nil
}

func (r *pelicanTestRepoStub) ListRuns(_ context.Context, _ *int64, _ int) ([]PelicanTestRun, error) {
	return nil, nil
}

func (r *pelicanTestRepoStub) CreateResultAndIncrement(ctx context.Context, result *PelicanTestResult) (*PelicanTestResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		r.persistContextCanceled = true
	}
	r.results = append(r.results, *result)
	return result, nil
}

func (r *pelicanTestRepoStub) HeartbeatRun(_ context.Context, _ int64, _ string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.heartbeatLost, nil
}

func (r *pelicanTestRepoStub) FinishRun(_ context.Context, _ int64, runnerID, status, errorMessage string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishStatus = status
	r.finishError = errorMessage
	r.finishRunnerID = runnerID
	return nil
}

func (r *pelicanTestRepoStub) RecoverStaleRunningRuns(_ context.Context, staleBefore time.Time, errorMessage string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recoveryCalls++
	r.recoveryMessage = errorMessage
	r.recoveryBefore = staleBefore
	return r.recovered, nil
}

func (r *pelicanTestRepoStub) DeleteRun(_ context.Context, _ int64) error {
	r.deleted = true
	return nil
}

// Compile-time check that the minimal fake still matches the repository contract.
var _ PelicanTestRepository = (*pelicanTestRepoStub)(nil)

type pelicanTestRunnerStub struct {
	release <-chan struct{}
}

func (r *pelicanTestRunnerStub) RunAccountTestBackground(_ context.Context, _ int64, _, _ string) (*ScheduledTestResult, error) {
	<-r.release
	return nil, errors.New("released")
}

var _ PelicanTestAccountRunner = (*pelicanTestRunnerStub)(nil)
