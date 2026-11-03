package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsRepositoryGetTTFTRankingGroupsByDimensionAndSortsP99(t *testing.T) {
	start := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	for _, tc := range []struct {
		name      string
		dimension service.OpsTTFTRankingDimension
		join      string
	}{
		{name: "group", dimension: service.OpsTTFTRankingByGroup, join: "JOIN groups dimension ON dimension.id = ul.group_id"},
		{name: "account", dimension: service.OpsTTFTRankingByAccount, join: "JOIN accounts dimension ON dimension.id = ul.account_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := newSQLMock(t)
			repo := &opsRepository{db: db}
			filter := &service.OpsTTFTRankingFilter{StartTime: start, EndTime: end, Dimension: tc.dimension, Platform: "openai"}
			query := `(?s)SELECT\s+dimension\.id, dimension\.name, .*FROM usage_logs ul\s+` + regexp.QuoteMeta(tc.join) + `\s+WHERE ul\.created_at >= \$1 AND ul\.created_at < \$2 AND LOWER\(dimension\.platform\) = \$3\s+GROUP BY dimension\.id, dimension\.name, dimension\.platform\s+ORDER BY p99_ms DESC NULLS LAST, sample_count DESC, dimension\.name ASC`
			mock.ExpectQuery(query).WithArgs(start, end, "openai").WillReturnRows(
				sqlmock.NewRows([]string{"id", "name", "platform", "request_count", "sample_count", "avg_ms", "p50_ms", "p95_ms", "p99_ms", "max_ms"}).
					AddRow(int64(7), "slow", "openai", int64(24), int64(20), 1500.0, 1200.0, 3000.0, 4500.0, int64(5100)),
			)

			resp, err := repo.GetTTFTRanking(context.Background(), filter)
			require.NoError(t, err)
			require.Equal(t, tc.dimension, resp.Dimension)
			require.Len(t, resp.Items, 1)
			require.EqualValues(t, 24, resp.Items[0].RequestCount)
			require.EqualValues(t, 20, resp.Items[0].SampleCount)
			require.Equal(t, 4500.0, *resp.Items[0].P99Ms)
			require.EqualValues(t, 5100, *resp.Items[0].MaxMs)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestOpsRepositoryGetTTFTRankingRejectsUnknownDimension(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}
	_, err := repo.GetTTFTRanking(context.Background(), &service.OpsTTFTRankingFilter{Dimension: "model"})
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
