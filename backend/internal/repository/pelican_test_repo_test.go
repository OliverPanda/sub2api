package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPelicanTestRepositoryCreateRunPreservesRunnerID(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	groupID := int64(9)
	startedAt := time.Date(2026, time.October, 28, 12, 0, 0, 0, time.UTC)
	createdAt := startedAt.Add(time.Second)
	const runnerID = "runner-test"

	mock.ExpectQuery("INSERT INTO pelican_test_runs").
		WithArgs(groupID, "group-a", "model-a", "prompt-a", service.PelicanTestStatusRunning,
			1, 0, 0, 0, "", int64(7), startedAt, nil, runnerID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "group_id", "group_name", "model_id", "prompt", "status",
			"total_accounts", "success_count", "failed_count", "skipped_count",
			"error_message", "created_by", "started_at", "finished_at", "created_at",
		}).AddRow(
			42, groupID, "group-a", "model-a", "prompt-a", service.PelicanTestStatusRunning,
			1, 0, 0, 0, "", int64(7), startedAt, nil, createdAt,
		))

	repo := &pelicanTestRepository{db: db}
	created, err := repo.CreateRun(context.Background(), &service.PelicanTestRun{
		GroupID:       &groupID,
		GroupName:     "group-a",
		ModelID:       "model-a",
		Prompt:        "prompt-a",
		Status:        service.PelicanTestStatusRunning,
		TotalAccounts: 1,
		CreatedBy:     7,
		StartedAt:     startedAt,
		RunnerID:      runnerID,
	})

	require.NoError(t, err)
	require.Equal(t, int64(42), created.ID)
	require.Equal(t, runnerID, created.RunnerID)
	require.NoError(t, mock.ExpectationsWereMet())
}
