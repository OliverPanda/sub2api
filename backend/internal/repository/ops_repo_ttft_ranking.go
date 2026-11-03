package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *opsRepository) GetTTFTRanking(ctx context.Context, filter *service.OpsTTFTRankingFilter) (*service.OpsTTFTRankingResponse, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, fmt.Errorf("nil filter")
	}

	var identityJoin, identitySelect, groupBy string
	switch filter.Dimension {
	case service.OpsTTFTRankingByGroup:
		identityJoin = "JOIN groups dimension ON dimension.id = ul.group_id"
		identitySelect = "dimension.id, dimension.name, COALESCE(dimension.platform, '')"
		groupBy = "dimension.id, dimension.name, dimension.platform"
	case service.OpsTTFTRankingByAccount:
		identityJoin = "JOIN accounts dimension ON dimension.id = ul.account_id"
		identitySelect = "dimension.id, dimension.name, dimension.platform"
		groupBy = "dimension.id, dimension.name, dimension.platform"
	default:
		return nil, fmt.Errorf("invalid TTFT ranking dimension %q", filter.Dimension)
	}

	args := []any{filter.StartTime.UTC(), filter.EndTime.UTC()}
	where := []string{"ul.created_at >= $1", "ul.created_at < $2"}
	if filter.Platform != "" {
		args = append(args, filter.Platform)
		where = append(where, fmt.Sprintf("LOWER(dimension.platform) = $%d", len(args)))
	}
	query := `SELECT
  ` + identitySelect + `,
  COUNT(*)::bigint AS request_count,
  COUNT(ul.first_token_ms)::bigint AS sample_count,
  AVG(ul.first_token_ms)::double precision AS avg_ms,
  percentile_cont(0.50) WITHIN GROUP (ORDER BY ul.first_token_ms) FILTER (WHERE ul.first_token_ms IS NOT NULL) AS p50_ms,
  percentile_cont(0.95) WITHIN GROUP (ORDER BY ul.first_token_ms) FILTER (WHERE ul.first_token_ms IS NOT NULL) AS p95_ms,
  percentile_cont(0.99) WITHIN GROUP (ORDER BY ul.first_token_ms) FILTER (WHERE ul.first_token_ms IS NOT NULL) AS p99_ms,
  MAX(ul.first_token_ms)::bigint AS max_ms
FROM usage_logs ul
` + identityJoin + `
WHERE ` + strings.Join(where, " AND ") + `
GROUP BY ` + groupBy + `
ORDER BY p99_ms DESC NULLS LAST, sample_count DESC, dimension.name ASC`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]*service.OpsTTFTRankingItem, 0, 32)
	for rows.Next() {
		item := &service.OpsTTFTRankingItem{}
		var avg, p50, p95, p99 sql.NullFloat64
		var max sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Name, &item.Platform, &item.RequestCount, &item.SampleCount, &avg, &p50, &p95, &p99, &max); err != nil {
			return nil, err
		}
		item.AvgMs = nullableFloat(avg)
		item.P50Ms = nullableFloat(p50)
		item.P95Ms = nullableFloat(p95)
		item.P99Ms = nullableFloat(p99)
		if max.Valid {
			value := max.Int64
			item.MaxMs = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &service.OpsTTFTRankingResponse{
		StartTime: filter.StartTime.UTC(),
		EndTime:   filter.EndTime.UTC(),
		Dimension: filter.Dimension,
		Platform:  filter.Platform,
		Items:     items,
	}, nil
}

func nullableFloat(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}
