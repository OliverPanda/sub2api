package service

import (
	"context"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func (s *OpsService) GetTTFTRanking(ctx context.Context, filter *OpsTTFTRankingFilter) (*OpsTTFTRankingResponse, error) {
	if err := s.RequireMonitoringEnabled(ctx); err != nil {
		return nil, err
	}
	if s.opsRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPS_REPO_UNAVAILABLE", "Ops repository not available")
	}
	if filter == nil || filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, infraerrors.BadRequest("OPS_FILTER_REQUIRED", "valid start_time/end_time are required")
	}
	if filter.StartTime.After(filter.EndTime) {
		return nil, infraerrors.BadRequest("OPS_TIME_RANGE_INVALID", "start_time must be <= end_time")
	}
	if filter.Dimension == "" {
		filter.Dimension = OpsTTFTRankingByGroup
	}
	if filter.Dimension != OpsTTFTRankingByGroup && filter.Dimension != OpsTTFTRankingByAccount {
		return nil, infraerrors.BadRequest("OPS_TTFT_DIMENSION_INVALID", "dimension must be group or account")
	}
	filter.Platform = strings.TrimSpace(strings.ToLower(filter.Platform))
	return s.opsRepo.GetTTFTRanking(ctx, filter)
}
