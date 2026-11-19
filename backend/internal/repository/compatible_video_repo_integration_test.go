//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newCompatibleVideoIntegrationTask(t *testing.T) (*usageBillingRepository, *service.CompatibleVideoTask) {
	t.Helper()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("video-%s@example.com", uuid.NewString()), PasswordHash: "hash", Balance: 100})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-video-" + uuid.NewString(), Name: "video-key", Quota: 100})
	account := mustCreateAccount(t, client, &service.Account{Name: "video-account-" + uuid.NewString(), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	repo := &usageBillingRepository{db: integrationDB}
	task := &service.CompatibleVideoTask{ID: "mvid_" + uuid.NewString(), UserID: user.ID, APIKeyID: key.ID, GroupID: 1, AccountID: account.ID, HoldAmount: 4, TotalCost: 2, ActualCost: 4, Model: "video-alias", UpstreamModel: "vendor-model", Resolution: "720p", Seconds: 8, RateMultiplier: 2, Status: "submitting", CreatedAt: time.Now().UTC()}
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM compatible_video_tasks WHERE id=$1", task.ID)
	})
	return repo, task
}
func requireCompatibleVideoBalance(t *testing.T, task *service.CompatibleVideoTask, balance, hold float64) {
	t.Helper()
	var gotBalance, gotHold float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), "SELECT balance,frozen_balance FROM users WHERE id=$1", task.UserID).Scan(&gotBalance, &gotHold))
	require.InDelta(t, balance, gotBalance, 1e-8)
	require.InDelta(t, hold, gotHold, 1e-8)
}
func compatibleVideoBillingCommand(task *service.CompatibleVideoTask) *service.UsageBillingCommand {
	return &service.UsageBillingCommand{CompatibleVideoTaskID: task.ID, RequestID: "compatible-video:" + task.ID, APIKeyID: task.APIKeyID, UserID: task.UserID, AccountID: task.AccountID, AccountType: service.AccountTypeAPIKey, Model: task.Model, BalanceCost: task.ActualCost, APIKeyQuotaCost: task.ActualCost}
}
func TestCompatibleVideoReservationAndConcurrentCapture(t *testing.T) {
	ctx := context.Background()
	repo, task := newCompatibleVideoIntegrationTask(t)
	require.NoError(t, repo.CreateCompatibleVideo(ctx, task))
	requireCompatibleVideoBalance(t, task, 96, 4)
	require.NoError(t, repo.AcceptCompatibleVideo(ctx, task.ID, "video_encoded/+=="))
	loaded, err := repo.GetCompatibleVideo(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, "video_encoded/+==", loaded.UpstreamID)
	require.Equal(t, task.Seconds, loaded.Seconds)
	const calls = 8
	results := make([]*service.UsageBillingApplyResult, calls)
	errs := make([]error, calls)
	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = repo.Apply(ctx, compatibleVideoBillingCommand(task))
		}(i)
	}
	wg.Wait()
	applied := 0
	for i, err := range errs {
		require.NoError(t, err)
		if results[i].Applied {
			applied++
		}
	}
	require.Equal(t, 1, applied)
	requireCompatibleVideoBalance(t, task, 96, 0)
	loaded, err = repo.GetCompatibleVideo(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, loaded.Settled)
	require.Equal(t, "completed", loaded.Status)
	var quota float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used FROM api_keys WHERE id=$1", task.APIKeyID).Scan(&quota))
	require.InDelta(t, 4, quota, 1e-8)
	require.NoError(t, repo.ReleaseCompatibleVideo(ctx, task.ID, "failed"))
	requireCompatibleVideoBalance(t, task, 96, 0)
}
func TestCompatibleVideoFailedCancellationReleasesOnce(t *testing.T) {
	for _, status := range []string{"failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			repo, task := newCompatibleVideoIntegrationTask(t)
			require.NoError(t, repo.CreateCompatibleVideo(ctx, task))
			require.NoError(t, repo.ReleaseCompatibleVideo(ctx, task.ID, status))
			require.NoError(t, repo.ReleaseCompatibleVideo(ctx, task.ID, status))
			requireCompatibleVideoBalance(t, task, 100, 0)
			_, err := repo.Apply(ctx, compatibleVideoBillingCommand(task))
			require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
			requireCompatibleVideoBalance(t, task, 100, 0)
		})
	}
}
func TestCompatibleVideoCaptureRollsBackOnBillingFailure(t *testing.T) {
	ctx := context.Background()
	repo, task := newCompatibleVideoIntegrationTask(t)
	require.NoError(t, repo.CreateCompatibleVideo(ctx, task))
	cmd := compatibleVideoBillingCommand(task)
	missing := int64(-999)
	cmd.BalanceCost = 0
	cmd.SubscriptionID = &missing
	cmd.SubscriptionCost = task.ActualCost
	_, err := repo.Apply(ctx, cmd)
	require.Error(t, err)
	requireCompatibleVideoBalance(t, task, 96, 4)
	loaded, err := repo.GetCompatibleVideo(ctx, task.ID)
	require.NoError(t, err)
	require.False(t, loaded.Settled)
	// The failed transaction must also roll back its dedup claim.
	result, err := repo.Apply(ctx, compatibleVideoBillingCommand(task))
	require.NoError(t, err)
	require.True(t, result.Applied)
	requireCompatibleVideoBalance(t, task, 96, 0)
}
func TestCompatibleVideoInsufficientFundsLeavesNoTask(t *testing.T) {
	ctx := context.Background()
	repo, task := newCompatibleVideoIntegrationTask(t)
	task.HoldAmount = 101
	require.ErrorIs(t, repo.CreateCompatibleVideo(ctx, task), service.ErrInsufficientBalance)
	_, err := repo.GetCompatibleVideo(ctx, task.ID)
	require.True(t, errors.Is(err, service.ErrCompatibleVideoNotFound))
	requireCompatibleVideoBalance(t, task, 100, 0)
}
func TestCompatibleVideoNoHoldAndPendingRecovery(t *testing.T) {
	ctx := context.Background()
	repo, task := newCompatibleVideoIntegrationTask(t)
	task.HoldAmount = 0
	task.SimpleMode = true
	require.NoError(t, repo.CreateCompatibleVideo(ctx, task))
	requireCompatibleVideoBalance(t, task, 100, 0)
	require.NoError(t, repo.AcceptCompatibleVideo(ctx, task.ID, "upstream-id"))
	pending, err := repo.ListPendingCompatibleVideos(ctx, 100)
	require.NoError(t, err)
	found := false
	for _, p := range pending {
		if p.ID == task.ID {
			found = true
			require.Equal(t, task.AccountID, p.AccountID)
		}
	}
	require.True(t, found)
	cmd := compatibleVideoBillingCommand(task)
	cmd.BalanceCost = 0
	cmd.APIKeyQuotaCost = 0
	result, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	requireCompatibleVideoBalance(t, task, 100, 0)
}

func TestCompatibleVideoCaptureAfterAPIKeyDeletion(t *testing.T) {
	ctx := context.Background()
	repo, task := newCompatibleVideoIntegrationTask(t)
	task.APIKeyQuota = 100
	require.NoError(t, repo.CreateCompatibleVideo(ctx, task))
	_, err := integrationDB.ExecContext(ctx, "UPDATE api_keys SET deleted_at = NOW() WHERE id = $1", task.APIKeyID)
	require.NoError(t, err)
	result, err := repo.Apply(ctx, compatibleVideoBillingCommand(task))
	require.NoError(t, err)
	require.True(t, result.Applied)
	requireCompatibleVideoBalance(t, task, 96, 0)
	_, err = repo.GetCompatibleVideo(ctx, task.ID)
	require.NoError(t, err)
}

func TestReplicateMusicSettlementIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("music-%s@example.com", uuid.NewString()), PasswordHash: "hash", Balance: 100})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-music-" + uuid.NewString(), Name: "music-key", Quota: 100})
	account := mustCreateAccount(t, client, &service.Account{Name: "music-account-" + uuid.NewString(), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	repo := &usageBillingRepository{db: integrationDB}
	task := &service.ReplicateMusicTask{ID: "rmus_" + uuid.NewString(), UserID: user.ID, APIKeyID: key.ID, GroupID: 1, AccountID: account.ID, HoldAmount: 4, TotalCost: 4, Status: "starting", UpstreamID: "prediction-1", Model: "owner/model", CreatedAt: time.Now().UTC()}
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM replicate_music_tasks WHERE id=$1", task.ID)
	})
	require.NoError(t, repo.CreateReplicateMusic(ctx, task))
	var balance, frozen float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance,frozen_balance FROM users WHERE id=$1", user.ID).Scan(&balance, &frozen))
	require.InDelta(t, 96, balance, 1e-8)
	require.InDelta(t, 4, frozen, 1e-8)
	output := []byte(`"https://cdn.example/audio.mp3"`)
	cmd := &service.UsageBillingCommand{ReplicateMusicTaskID: task.ID, ReplicateMusicOutput: output, RequestID: "replicate-music:" + task.ID, APIKeyID: key.ID, UserID: user.ID, AccountID: account.ID, AccountType: service.AccountTypeAPIKey, Model: task.Model, APIKeyQuotaCost: 4}
	result, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	result, err = repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, result.Applied)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance,frozen_balance FROM users WHERE id=$1", user.ID).Scan(&balance, &frozen))
	require.InDelta(t, 96, balance, 1e-8)
	require.Zero(t, frozen)
	var status string
	var settled bool
	var savedOutput []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT status,settled,output FROM replicate_music_tasks WHERE id=$1", task.ID).Scan(&status, &settled, &savedOutput))
	require.Equal(t, "succeeded", status)
	require.True(t, settled)
	require.JSONEq(t, string(output), string(savedOutput))
}
