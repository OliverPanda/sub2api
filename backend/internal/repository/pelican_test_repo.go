package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type pelicanTestRepository struct {
	db *sql.DB
}

func NewPelicanTestRepository(db *sql.DB) service.PelicanTestRepository {
	return &pelicanTestRepository{db: db}
}

func (r *pelicanTestRepository) CreateRun(ctx context.Context, run *service.PelicanTestRun) (*service.PelicanTestRun, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO pelican_test_runs (
			group_id, group_name, model_id, prompt, status, total_accounts,
			success_count, failed_count, skipped_count, error_message, created_by,
			started_at, finished_at, created_at, runner_id, heartbeat_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NOW(), $14, NOW())
		RETURNING id, group_id, group_name, model_id, prompt, status, total_accounts,
			success_count, failed_count, skipped_count, error_message, created_by,
			started_at, finished_at, created_at
	`, run.GroupID, run.GroupName, run.ModelID, run.Prompt, run.Status, run.TotalAccounts,
		run.SuccessCount, run.FailedCount, run.SkippedCount, run.ErrorMessage, run.CreatedBy,
		run.StartedAt, run.FinishedAt, run.RunnerID)
	created, err := scanPelicanTestRun(row)
	if err != nil {
		return nil, translatePersistenceError(err, nil, service.ErrPelicanTestRunInProgress)
	}
	// runner_id is write-only in the API model, so it is intentionally not part
	// of RETURNING. Preserve it for the background heartbeat and terminal finish.
	created.RunnerID = run.RunnerID
	return created, nil
}

func (r *pelicanTestRepository) GetRun(ctx context.Context, id int64) (*service.PelicanTestRunDetail, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, group_id, group_name, model_id, prompt, status, total_accounts,
			success_count, failed_count, skipped_count, error_message, created_by,
			started_at, finished_at, created_at
		FROM pelican_test_runs
		WHERE id = $1
	`, id)
	run, err := scanPelicanTestRun(row)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id, run_id, group_id, account_id, account_name, platform, model_id,
			status, raw_output, svg_content, image_url, error_message, latency_ms,
			started_at, finished_at, created_at
		FROM pelican_test_results
		WHERE run_id = $1
		ORDER BY id ASC
	`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	detail := &service.PelicanTestRunDetail{PelicanTestRun: *run}
	for rows.Next() {
		result := service.PelicanTestResult{}
		if err := rows.Scan(
			&result.ID, &result.RunID, &result.GroupID, &result.AccountID,
			&result.AccountName, &result.Platform, &result.ModelID, &result.Status,
			&result.RawOutput, &result.SVGContent, &result.ImageURL, &result.ErrorMessage, &result.LatencyMS,
			&result.StartedAt, &result.FinishedAt, &result.CreatedAt,
		); err != nil {
			return nil, err
		}
		detail.Results = append(detail.Results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return detail, nil
}

func (r *pelicanTestRepository) ListRuns(ctx context.Context, groupID *int64, limit int) ([]service.PelicanTestRun, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, group_id, group_name, model_id, prompt, status, total_accounts,
			success_count, failed_count, skipped_count, error_message, created_by,
			started_at, finished_at, created_at
		FROM pelican_test_runs
		WHERE ($1::BIGINT IS NULL OR group_id = $1)
		ORDER BY created_at DESC, id DESC
		LIMIT $2
	`, groupID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	runs := make([]service.PelicanTestRun, 0)
	for rows.Next() {
		run, err := scanPelicanTestRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return runs, nil
}

func (r *pelicanTestRepository) CreateResultAndIncrement(ctx context.Context, result *service.PelicanTestResult) (*service.PelicanTestResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var runStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM pelican_test_runs WHERE id = $1 FOR UPDATE`, result.RunID).Scan(&runStatus); err != nil {
		return nil, err
	}
	if runStatus != service.PelicanTestStatusRunning {
		return nil, fmt.Errorf("pelican test run %d is no longer running", result.RunID)
	}

	row := tx.QueryRowContext(ctx, `
		INSERT INTO pelican_test_results (
			run_id, group_id, account_id, account_name, platform, model_id, status,
			raw_output, svg_content, image_url, error_message, latency_ms,
			started_at, finished_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, NOW())
		ON CONFLICT (run_id, account_id) WHERE account_id IS NOT NULL DO NOTHING
		RETURNING id, run_id, group_id, account_id, account_name, platform, model_id,
			status, raw_output, svg_content, image_url, error_message, latency_ms,
			started_at, finished_at, created_at
	`, result.RunID, result.GroupID, result.AccountID, result.AccountName, result.Platform,
		result.ModelID, result.Status, result.RawOutput, result.SVGContent,
		result.ImageURL, result.ErrorMessage, result.LatencyMS, result.StartedAt, result.FinishedAt)

	stored, err := scanPelicanTestResult(row)
	inserted := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		row = tx.QueryRowContext(ctx, `
			SELECT id, run_id, group_id, account_id, account_name, platform, model_id,
				status, raw_output, svg_content, image_url, error_message, latency_ms,
				started_at, finished_at, created_at
			FROM pelican_test_results
			WHERE run_id = $1 AND account_id = $2
		`, result.RunID, result.AccountID)
		stored, err = scanPelicanTestResult(row)
	}
	if err != nil {
		return nil, err
	}

	if inserted {
		column := ""
		switch result.Status {
		case service.PelicanTestResultSuccess:
			column = "success_count = success_count + 1"
		case service.PelicanTestResultFailed:
			column = "failed_count = failed_count + 1"
		default:
			return nil, fmt.Errorf("unsupported pelican test result status %q", result.Status)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE pelican_test_runs SET "+column+" WHERE id = $1", result.RunID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return stored, nil
}

func (r *pelicanTestRepository) HeartbeatRun(ctx context.Context, id int64, runnerID string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE pelican_test_runs
		SET heartbeat_at = NOW()
		WHERE id = $1 AND runner_id = $2 AND status = 'running'
	`, id, runnerID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func (r *pelicanTestRepository) FinishRun(ctx context.Context, id int64, runnerID, status, errorMessage string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE pelican_test_runs
		SET status = $2, error_message = $3, finished_at = NOW()
		WHERE id = $1 AND runner_id = $4 AND status = 'running'
	`, id, status, errorMessage, runnerID)
	return err
}

func (r *pelicanTestRepository) RecoverStaleRunningRuns(ctx context.Context, staleBefore time.Time, errorMessage string) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE pelican_test_runs
		SET status = 'failed',
			failed_count = LEAST(
				GREATEST(total_accounts - skipped_count, 0),
				GREATEST(failed_count, total_accounts - skipped_count - success_count)
			),
			error_message = CASE
				WHEN error_message = '' THEN $1
				ELSE error_message || '; ' || $1
			END,
			finished_at = NOW()
		WHERE status = 'running' AND heartbeat_at < $2
	`, errorMessage, staleBefore)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *pelicanTestRepository) DeleteRun(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM pelican_test_runs WHERE id = $1`, id)
	return err
}

type pelicanRunScanner interface {
	Scan(dest ...any) error
}

func scanPelicanTestResult(row pelicanRunScanner) (*service.PelicanTestResult, error) {
	result := &service.PelicanTestResult{}
	if err := row.Scan(
		&result.ID, &result.RunID, &result.GroupID, &result.AccountID,
		&result.AccountName, &result.Platform, &result.ModelID, &result.Status,
		&result.RawOutput, &result.SVGContent, &result.ImageURL, &result.ErrorMessage, &result.LatencyMS,
		&result.StartedAt, &result.FinishedAt, &result.CreatedAt,
	); err != nil {
		return nil, err
	}
	return result, nil
}

func scanPelicanTestRun(row pelicanRunScanner) (*service.PelicanTestRun, error) {
	run := &service.PelicanTestRun{}
	if err := row.Scan(
		&run.ID, &run.GroupID, &run.GroupName, &run.ModelID, &run.Prompt,
		&run.Status, &run.TotalAccounts, &run.SuccessCount, &run.FailedCount,
		&run.SkippedCount, &run.ErrorMessage, &run.CreatedBy, &run.StartedAt,
		&run.FinishedAt, &run.CreatedAt,
	); err != nil {
		return nil, err
	}
	return run, nil
}
