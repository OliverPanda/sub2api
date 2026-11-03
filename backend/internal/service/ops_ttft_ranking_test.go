package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type ttftRankingRepoStub struct {
	OpsRepository
	filter *OpsTTFTRankingFilter
}

func (s *ttftRankingRepoStub) GetTTFTRanking(_ context.Context, filter *OpsTTFTRankingFilter) (*OpsTTFTRankingResponse, error) {
	s.filter = filter
	return &OpsTTFTRankingResponse{Dimension: filter.Dimension}, nil
}

func TestOpsServiceGetTTFTRankingDefaultsAndNormalizesFilter(t *testing.T) {
	start := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	repo := &ttftRankingRepoStub{}
	svc := &OpsService{opsRepo: repo}
	filter := &OpsTTFTRankingFilter{
		StartTime: start,
		EndTime:   start.Add(time.Hour),
		Platform:  " OPENAI ",
	}
	response, err := svc.GetTTFTRanking(context.Background(), filter)
	require.NoError(t, err)
	require.Equal(t, OpsTTFTRankingByGroup, response.Dimension)
	require.Equal(t, OpsTTFTRankingByGroup, repo.filter.Dimension)
	require.Equal(t, "openai", repo.filter.Platform)
}

func TestOpsServiceGetTTFTRankingRejectsInvalidDimension(t *testing.T) {
	svc := &OpsService{opsRepo: &ttftRankingRepoStub{}}
	_, err := svc.GetTTFTRanking(context.Background(), &OpsTTFTRankingFilter{
		StartTime: time.Now().UTC(),
		EndTime:   time.Now().UTC().Add(time.Hour),
		Dimension: "model",
	})
	require.Error(t, err)
}
