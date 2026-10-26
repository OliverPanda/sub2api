package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsRepositoryGetGroupQualityRanking_CalculatesRatesAndRanksSufficientSamples(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}

	start := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	filter := &service.OpsDashboardFilter{
		StartTime: start,
		EndTime:   end,
		Platform:  " OpenAI ",
	}

	rows := sqlmock.NewRows([]string{
		"group_id", "group_name", "platform", "success_count", "error_count_total",
		"error_count_sla", "upstream_error_count", "rate_limit_count",
		"p95_duration_ms", "p95_ttft_ms",
	}).
		AddRow(int64(12), "unstable-relay", "openai", int64(70), int64(30), int64(20), int64(10), int64(5), 2300.4, 850.2).
		AddRow(int64(13), "new-group", "openai", int64(5), int64(0), int64(0), int64(0), int64(0), nil, nil)

	mock.ExpectQuery(`(?s)WITH filtered_groups.*g\.deleted_at IS NULL.*LOWER\(g\.platform\) = \$3.*upstream_status_code, oe\.status_code\) NOT IN \(429, 529\).*upstream_status_code, oe\.status_code\) = 429.*FROM filtered_groups fg`).
		WithArgs(start, end, "openai").
		WillReturnRows(rows)

	result, err := repo.GetGroupQualityRanking(context.Background(), filter, 20)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "openai", result.Platform)
	require.Equal(t, 20, result.MinSample)
	require.Equal(t, 1, result.InsufficientSampleCount)
	require.Len(t, result.Items, 2)

	worst := result.Items[0]
	require.Equal(t, int64(12), worst.GroupID)
	require.Equal(t, int64(100), worst.RequestCount)
	require.Equal(t, int64(90), worst.SampleCount)
	require.True(t, worst.SampleSufficient)
	require.InDelta(t, 0.7778, worst.SuccessRate, 0.00001)
	require.InDelta(t, 0.1111, worst.UpstreamErrorRate, 0.00001)
	require.InDelta(t, 0.0556, worst.RateLimitRate, 0.00001)
	require.NotNil(t, worst.P95DurationMs)
	require.Equal(t, 2300, *worst.P95DurationMs)
	require.NotNil(t, worst.P95TTFTMs)
	require.Equal(t, 850, *worst.P95TTFTMs)

	lowSample := result.Items[1]
	require.Equal(t, int64(13), lowSample.GroupID)
	require.False(t, lowSample.SampleSufficient)
	require.InDelta(t, 1.0, lowSample.SuccessRate, 0.00001)
	require.Nil(t, lowSample.P95DurationMs)
	require.Nil(t, lowSample.P95TTFTMs)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsRepositoryGetGroupQualityRanking_GroupFilterUsesStablePlaceholder(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}

	start := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	groupID := int64(12)
	filter := &service.OpsDashboardFilter{StartTime: start, EndTime: end, GroupID: &groupID}

	mock.ExpectQuery(`(?s)WITH filtered_groups.*g\.deleted_at IS NULL.*g\.id = \$3.*FROM filtered_groups fg`).
		WithArgs(start, end, groupID).
		WillReturnRows(sqlmock.NewRows([]string{
			"group_id", "group_name", "platform", "success_count", "error_count_total",
			"error_count_sla", "upstream_error_count", "rate_limit_count",
			"p95_duration_ms", "p95_ttft_ms",
		}))

	result, err := repo.GetGroupQualityRanking(context.Background(), filter, 0)
	require.NoError(t, err)
	require.Equal(t, 20, result.MinSample)
	require.Empty(t, result.Items)
	require.NoError(t, mock.ExpectationsWereMet())
}
