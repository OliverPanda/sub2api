package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPelicanTestTablesIncludeRunnerLeaseAndResultIdempotency(t *testing.T) {
	base, err := FS.ReadFile("246_add_pelican_test_tables.sql")
	require.NoError(t, err)
	baseSQL := strings.Join(strings.Fields(string(base)), " ")
	for _, fragment := range []string{
		"runner_id VARCHAR(64) NOT NULL DEFAULT ''",
		"heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT NOW()",
		"idx_pelican_test_runs_running_heartbeat",
		"idx_pelican_test_runs_single_running",
		"idx_pelican_test_results_run_account",
	} {
		require.Contains(t, baseSQL, fragment)
	}
}

func TestMigration247UpgradesExistingPelicanTestTables(t *testing.T) {
	content, err := FS.ReadFile("247_pelican_test_runner_lease.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")

	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS runner_id VARCHAR(64) NOT NULL DEFAULT ''")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT NOW()")
	require.Contains(t, sql, "SET heartbeat_at = created_at WHERE runner_id = ''")
	require.Contains(t, sql, "WHERE status = 'running'")
	require.Contains(t, sql, "pelican test was interrupted by a service upgrade")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS idx_pelican_test_runs_single_running")
	require.Contains(t, sql, "ROW_NUMBER() OVER (PARTITION BY run_id, account_id ORDER BY id ASC)")
	require.Contains(t, sql, "success_count = GREATEST(runs.success_count - COALESCE(removed_counts.success_count, 0), 0)")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS idx_pelican_test_results_run_account")
}
