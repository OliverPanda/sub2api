package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// GetGroupQualityRanking aggregates SLA-scoped quality metrics per group.
//
// Success comes from usage_logs. Failures come from client-visible rows in
// ops_error_logs, excluding count_tokens traffic. Upstream failures exclude
// business limits and rate-limit responses (429/529), matching the existing
// dashboard overview semantics. Percentiles cover successful requests only.
func (r *opsRepository) GetGroupQualityRanking(ctx context.Context, filter *service.OpsDashboardFilter, minSample int) (*service.OpsGroupQualityResponse, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, fmt.Errorf("nil filter")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, fmt.Errorf("start_time/end_time required")
	}
	if minSample <= 0 {
		minSample = 20
	}

	start := filter.StartTime.UTC()
	end := filter.EndTime.UTC()
	args := []any{start, end}
	next := 3
	groupClauses := []string{"g.deleted_at IS NULL"}

	if filter.GroupID != nil && *filter.GroupID > 0 {
		groupClauses = append(groupClauses, fmt.Sprintf("g.id = $%d", next))
		args = append(args, *filter.GroupID)
		next++
	}
	if platform := strings.TrimSpace(strings.ToLower(filter.Platform)); platform != "" {
		groupClauses = append(groupClauses, fmt.Sprintf("LOWER(g.platform) = $%d", next))
		args = append(args, platform)
	}

	query := `
WITH filtered_groups AS (
  SELECT g.id, g.name, g.platform
  FROM groups g
  WHERE ` + strings.Join(groupClauses, " AND ") + `
),
usage_stats AS (
  SELECT
    ul.group_id,
    COUNT(*)::bigint AS success_count,
    percentile_cont(0.95) WITHIN GROUP (ORDER BY ul.duration_ms)
      FILTER (WHERE ul.duration_ms IS NOT NULL) AS p95_duration_ms,
    percentile_cont(0.95) WITHIN GROUP (ORDER BY ul.first_token_ms)
      FILTER (WHERE ul.first_token_ms IS NOT NULL) AS p95_ttft_ms
  FROM usage_logs ul
  JOIN filtered_groups fg ON fg.id = ul.group_id
  WHERE ul.created_at >= $1 AND ul.created_at < $2
  GROUP BY ul.group_id
),
error_stats AS (
  SELECT
    oe.group_id,
    COUNT(*)::bigint AS error_count_total,
    COUNT(*) FILTER (
      WHERE NOT oe.is_business_limited
        AND COALESCE(oe.status_code, 0) >= 400
    )::bigint AS error_count_sla,
    COUNT(*) FILTER (
      WHERE NOT oe.is_business_limited
        AND oe.error_owner = 'provider'
        AND COALESCE(oe.upstream_status_code, oe.status_code) NOT IN (429, 529)
    )::bigint AS upstream_error_count,
    COUNT(*) FILTER (
      WHERE NOT oe.is_business_limited
        AND oe.error_owner = 'provider'
        AND COALESCE(oe.upstream_status_code, oe.status_code) = 429
    )::bigint AS rate_limit_count
  FROM ops_error_logs oe
  JOIN filtered_groups fg ON fg.id = oe.group_id
  WHERE oe.created_at >= $1 AND oe.created_at < $2
    AND oe.is_count_tokens = FALSE
    AND COALESCE(oe.status_code, 0) >= 400
  GROUP BY oe.group_id
)
SELECT
  fg.id,
  fg.name,
  fg.platform,
  COALESCE(us.success_count, 0)::bigint AS success_count,
  COALESCE(es.error_count_total, 0)::bigint AS error_count_total,
  COALESCE(es.error_count_sla, 0)::bigint AS error_count_sla,
  COALESCE(es.upstream_error_count, 0)::bigint AS upstream_error_count,
  COALESCE(es.rate_limit_count, 0)::bigint AS rate_limit_count,
  us.p95_duration_ms,
  us.p95_ttft_ms
FROM filtered_groups fg
LEFT JOIN usage_stats us ON us.group_id = fg.id
LEFT JOIN error_stats es ON es.group_id = fg.id`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]*service.OpsGroupQualityItem, 0, 64)
	insufficient := 0
	for rows.Next() {
		var (
			item             service.OpsGroupQualityItem
			errorTotal       int64
			upstreamErrorCnt int64
			rateLimitCount   int64
			p95Duration      sql.NullFloat64
			p95TTFT          sql.NullFloat64
		)
		if err := rows.Scan(
			&item.GroupID,
			&item.GroupName,
			&item.Platform,
			&item.SuccessCount,
			&errorTotal,
			&item.ErrorCountSLA,
			&upstreamErrorCnt,
			&rateLimitCount,
			&p95Duration,
			&p95TTFT,
		); err != nil {
			return nil, err
		}

		item.RequestCount = item.SuccessCount + errorTotal
		item.SampleCount = item.SuccessCount + item.ErrorCountSLA
		item.SampleSufficient = item.SampleCount >= int64(minSample)
		if item.SampleCount > 0 {
			denominator := float64(item.SampleCount)
			item.SuccessRate = roundTo4DP(float64(item.SuccessCount) / denominator)
			item.UpstreamErrorRate = roundTo4DP(float64(upstreamErrorCnt) / denominator)
			item.RateLimitRate = roundTo4DP(float64(rateLimitCount) / denominator)
		}
		item.P95DurationMs = floatToIntPtr(p95Duration)
		item.P95TTFTMs = floatToIntPtr(p95TTFT)
		if !item.SampleSufficient {
			insufficient++
		}
		items = append(items, &item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Default order answers the operator's question directly: groups with
	// enough samples and the lowest success rate come first.
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.SampleSufficient != b.SampleSufficient {
			return a.SampleSufficient
		}
		if a.SampleSufficient {
			if a.SuccessRate != b.SuccessRate {
				return a.SuccessRate < b.SuccessRate
			}
			if a.UpstreamErrorRate != b.UpstreamErrorRate {
				return a.UpstreamErrorRate > b.UpstreamErrorRate
			}
			if a.RateLimitRate != b.RateLimitRate {
				return a.RateLimitRate > b.RateLimitRate
			}
		}
		if a.RequestCount != b.RequestCount {
			return a.RequestCount > b.RequestCount
		}
		return a.GroupName < b.GroupName
	})

	return &service.OpsGroupQualityResponse{
		StartTime:               start,
		EndTime:                 end,
		Platform:                strings.TrimSpace(strings.ToLower(filter.Platform)),
		MinSample:               minSample,
		Items:                   items,
		InsufficientSampleCount: insufficient,
	}, nil
}
