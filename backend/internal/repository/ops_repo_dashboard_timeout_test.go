package repository

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestIsQueryTimeoutErr(t *testing.T) {
	if !isQueryTimeoutErr(context.DeadlineExceeded) {
		t.Fatalf("context.DeadlineExceeded should be treated as query timeout")
	}
	if !isQueryTimeoutErr(fmt.Errorf("wrapped: %w", context.DeadlineExceeded)) {
		t.Fatalf("wrapped context.DeadlineExceeded should be treated as query timeout")
	}
	if isQueryTimeoutErr(context.Canceled) {
		t.Fatalf("context.Canceled should not be treated as query timeout")
	}
	if isQueryTimeoutErr(fmt.Errorf("wrapped: %w", context.Canceled)) {
		t.Fatalf("wrapped context.Canceled should not be treated as query timeout")
	}
}

func TestOpsOutputTPSOverviewFailurePolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		wantError bool
	}{
		{"timeout keeps overview", context.DeadlineExceeded, false},
		{"cancellation propagates", context.Canceled, true},
		{"database errors propagate", errors.New("database unavailable"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newSQLMock(t)
			repo := &opsRepository{db: db}
			// The six existing raw overview queries run before the output-rate query.
			for _, count := range []int{2, 13, 6, 2, 6, 2} {
				columns := make([]string, count)
				values := make([]driver.Value, count)
				for i := range columns {
					columns[i] = fmt.Sprint(i)
					values[i] = int64(0)
				}
				mock.ExpectQuery("SELECT|WITH").WillReturnRows(sqlmock.NewRows(columns).AddRow(values...))
			}
			mock.ExpectQuery(`percentile_cont\(0.05\)`).WillReturnError(tc.err)
			start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			overview, err := repo.GetDashboardOverview(context.Background(), &service.OpsDashboardFilter{StartTime: start, EndTime: start.Add(time.Hour), QueryMode: service.OpsQueryModeRaw})
			if tc.wantError {
				require.ErrorIs(t, err, tc.err)
				require.Nil(t, overview)
			} else {
				require.NoError(t, err)
				require.NotNil(t, overview)
				require.Nil(t, overview.OutputTPS)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestOpsCacheHitRateUsesTokenWeightedTotals(t *testing.T) {
	db, mock := newSQLMock(t)
	defer func() { _ = db.Close() }()
	repo := &opsRepository{db: db}
	now := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`percentile_cont\(0.05\)`).WillReturnRows(
		sqlmock.NewRows([]string{"p5", "p10", "p50", "avg", "sample_count", "cache_read", "cache_creation", "input"}).
			AddRow(10.0, 20.0, 30.0, 25.0, int64(4), int64(600), int64(100), int64(300)),
	)

	output, cache, err := repo.queryOutputTPS(context.Background(), &service.OpsDashboardFilter{
		StartTime: now.Add(-time.Hour),
		EndTime:   now,
	})
	require.NoError(t, err)
	require.EqualValues(t, 4, output.SampleCount)
	require.EqualValues(t, 600, cache.CacheReadTokens)
	require.EqualValues(t, 100, cache.CacheCreationTokens)
	require.EqualValues(t, 300, cache.InputTokens)
	require.NotNil(t, cache.HitRate)
	require.InDelta(t, 0.6, *cache.HitRate, 1e-9)
	require.NoError(t, mock.ExpectationsWereMet())
}
