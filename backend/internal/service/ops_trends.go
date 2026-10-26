package service

import (
	"context"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func (s *OpsService) GetThroughputTrend(ctx context.Context, filter *OpsDashboardFilter, bucketSeconds int) (*OpsThroughputTrendResponse, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if filter == nil {
		return nil, infraerrors.BadRequest("OPS_FILTER_REQUIRED", "filter is required")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, infraerrors.BadRequest("OPS_TIME_RANGE_REQUIRED", "start_time/end_time are required")
	}
	if filter.StartTime.After(filter.EndTime) {
		return nil, infraerrors.BadRequest("OPS_TIME_RANGE_INVALID", "start_time must be <= end_time")
	}

	filter.QueryMode = s.resolveOpsQueryMode(ctx, filter.QueryMode)

	result, err := s.opsRepo.GetThroughputTrend(ctx, filter, bucketSeconds)
	if err != nil && shouldFallbackOpsPreagg(filter, err) {
		rawFilter := cloneOpsFilterWithMode(filter, OpsQueryModeRaw)
		return s.opsRepo.GetThroughputTrend(ctx, rawFilter, bucketSeconds)
	}
	return result, err
}

// GetGroupQualityRanking returns an SLA-scoped quality ranking for every live
// group. It deliberately uses raw usage/error logs (not pre-aggregates): the
// ranking needs per-group percentile latency and provider ownership, and those
// cannot be reconstructed reliably from existing hourly rollups.
func (s *OpsService) GetGroupQualityRanking(ctx context.Context, filter *OpsDashboardFilter, minSample int) (*OpsGroupQualityResponse, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if filter == nil {
		return nil, infraerrors.BadRequest("OPS_FILTER_REQUIRED", "filter is required")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, infraerrors.BadRequest("OPS_TIME_RANGE_REQUIRED", "start_time/end_time are required")
	}
	if filter.StartTime.After(filter.EndTime) {
		return nil, infraerrors.BadRequest("OPS_TIME_RANGE_INVALID", "start_time must be <= end_time")
	}
	if minSample <= 0 {
		minSample = 20
	}
	if minSample > 1000 {
		minSample = 1000
	}

	return s.opsRepo.GetGroupQualityRanking(ctx, filter, minSample)
}
