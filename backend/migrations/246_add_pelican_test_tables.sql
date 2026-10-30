-- 246_add_pelican_test_tables.sql
-- Batch pelican-drawing probes grouped by account group.

CREATE TABLE IF NOT EXISTS pelican_test_runs (
    id               BIGSERIAL PRIMARY KEY,
    group_id         BIGINT REFERENCES groups(id) ON DELETE SET NULL,
    group_name       VARCHAR(200) NOT NULL DEFAULT '',
    model_id         VARCHAR(200) NOT NULL DEFAULT '',
    prompt           TEXT NOT NULL DEFAULT '',
    status           VARCHAR(20) NOT NULL DEFAULT 'running',
    total_accounts   INT NOT NULL DEFAULT 0,
    success_count    INT NOT NULL DEFAULT 0,
    failed_count     INT NOT NULL DEFAULT 0,
    skipped_count    INT NOT NULL DEFAULT 0,
    error_message    TEXT NOT NULL DEFAULT '',
    created_by       BIGINT NOT NULL DEFAULT 0,
    started_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at      TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    runner_id        VARCHAR(64) NOT NULL DEFAULT '',
    heartbeat_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_pelican_test_runs_group_created
    ON pelican_test_runs(group_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_pelican_test_runs_created
    ON pelican_test_runs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_pelican_test_runs_running_heartbeat
    ON pelican_test_runs(heartbeat_at)
    WHERE status = 'running';

-- A single database-enforced running batch also protects deployments with more
-- than one application process. Expired runner leases are failed by any instance.
CREATE UNIQUE INDEX IF NOT EXISTS idx_pelican_test_runs_single_running
    ON pelican_test_runs ((status))
    WHERE status = 'running';

CREATE TABLE IF NOT EXISTS pelican_test_results (
    id             BIGSERIAL PRIMARY KEY,
    run_id         BIGINT NOT NULL REFERENCES pelican_test_runs(id) ON DELETE CASCADE,
    group_id       BIGINT REFERENCES groups(id) ON DELETE SET NULL,
    account_id     BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    account_name   VARCHAR(200) NOT NULL DEFAULT '',
    platform       VARCHAR(50) NOT NULL DEFAULT '',
    model_id       VARCHAR(200) NOT NULL DEFAULT '',
    status         VARCHAR(20) NOT NULL DEFAULT 'failed',
    raw_output     TEXT NOT NULL DEFAULT '',
    svg_content    TEXT NOT NULL DEFAULT '',
    error_message  TEXT NOT NULL DEFAULT '',
    latency_ms     BIGINT NOT NULL DEFAULT 0,
    started_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_pelican_test_results_run_created
    ON pelican_test_results(run_id, created_at ASC);
CREATE INDEX IF NOT EXISTS idx_pelican_test_results_account_created
    ON pelican_test_results(account_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pelican_test_results_run_account
    ON pelican_test_results(run_id, account_id)
    WHERE account_id IS NOT NULL;
