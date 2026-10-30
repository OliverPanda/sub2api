-- Add runner leases and result idempotency to installations that already ran 246.
ALTER TABLE pelican_test_runs
    ADD COLUMN IF NOT EXISTS runner_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE pelican_test_runs
    ADD COLUMN IF NOT EXISTS heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

UPDATE pelican_test_runs
SET heartbeat_at = created_at
WHERE runner_id = '';

-- Before adding the global one-running constraint, close batches left behind by
-- the old runner. They cannot be resumed by this process, and multiple legacy
-- rows would otherwise make the unique index creation fail.
UPDATE pelican_test_runs
SET status = 'failed',
    failed_count = LEAST(
        GREATEST(total_accounts - skipped_count, 0),
        GREATEST(failed_count, total_accounts - skipped_count - success_count)
    ),
    error_message = CASE
        WHEN error_message = '' THEN 'pelican test was interrupted by a service upgrade'
        ELSE error_message || '; pelican test was interrupted by a service upgrade'
    END,
    finished_at = NOW()
WHERE status = 'running';

CREATE INDEX IF NOT EXISTS idx_pelican_test_runs_running_heartbeat
    ON pelican_test_runs(heartbeat_at)
    WHERE status = 'running';

CREATE UNIQUE INDEX IF NOT EXISTS idx_pelican_test_runs_single_running
    ON pelican_test_runs ((status))
    WHERE status = 'running';

-- Keep the earliest result if an earlier retry race produced duplicates. Counters
-- are decremented for removed rows so they remain consistent with stored results.
WITH duplicates AS (
    SELECT id, run_id, status,
           ROW_NUMBER() OVER (PARTITION BY run_id, account_id ORDER BY id ASC) AS row_num
    FROM pelican_test_results
    WHERE account_id IS NOT NULL
), removed AS (
    DELETE FROM pelican_test_results results
    USING duplicates
    WHERE results.id = duplicates.id
      AND duplicates.row_num > 1
    RETURNING results.run_id, results.status
), removed_counts AS (
    SELECT run_id,
           COUNT(*) FILTER (WHERE status = 'success')::INT AS success_count,
           COUNT(*) FILTER (WHERE status = 'failed')::INT AS failed_count
    FROM removed
    GROUP BY run_id
)
UPDATE pelican_test_runs runs
SET success_count = GREATEST(runs.success_count - COALESCE(removed_counts.success_count, 0), 0),
    failed_count = GREATEST(runs.failed_count - COALESCE(removed_counts.failed_count, 0), 0)
FROM removed_counts
WHERE runs.id = removed_counts.run_id;

CREATE UNIQUE INDEX IF NOT EXISTS idx_pelican_test_results_run_account
    ON pelican_test_results(run_id, account_id)
    WHERE account_id IS NOT NULL;
