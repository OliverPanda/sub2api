package service

import "time"

type OpsThroughputTrendPoint struct {
	BucketStart   time.Time `json:"bucket_start"`
	RequestCount  int64     `json:"request_count"`
	TokenConsumed int64     `json:"token_consumed"`
	SwitchCount   int64     `json:"switch_count"`
	QPS           float64   `json:"qps"`
	TPS           float64   `json:"tps"`
}

type OpsThroughputPlatformBreakdownItem struct {
	Platform      string `json:"platform"`
	RequestCount  int64  `json:"request_count"`
	TokenConsumed int64  `json:"token_consumed"`
}

type OpsThroughputGroupBreakdownItem struct {
	GroupID       int64  `json:"group_id"`
	GroupName     string `json:"group_name"`
	RequestCount  int64  `json:"request_count"`
	TokenConsumed int64  `json:"token_consumed"`
}

type OpsThroughputTrendResponse struct {
	Bucket string `json:"bucket"`

	Points []*OpsThroughputTrendPoint `json:"points"`

	// Optional drilldown helpers:
	// - When no platform/group is selected: returns totals by platform.
	// - When platform is selected but group is not: returns top groups in that platform.
	ByPlatform []*OpsThroughputPlatformBreakdownItem `json:"by_platform,omitempty"`
	TopGroups  []*OpsThroughputGroupBreakdownItem    `json:"top_groups,omitempty"`
}

type OpsErrorTrendPoint struct {
	BucketStart time.Time `json:"bucket_start"`

	ErrorCountTotal      int64 `json:"error_count_total"`
	BusinessLimitedCount int64 `json:"business_limited_count"`
	ErrorCountSLA        int64 `json:"error_count_sla"`

	UpstreamErrorCountExcl429529 int64 `json:"upstream_error_count_excl_429_529"`
	Upstream429Count             int64 `json:"upstream_429_count"`
	Upstream529Count             int64 `json:"upstream_529_count"`
}

type OpsErrorTrendResponse struct {
	Bucket string                `json:"bucket"`
	Points []*OpsErrorTrendPoint `json:"points"`
}

type OpsErrorDistributionItem struct {
	StatusCode      int   `json:"status_code"`
	Total           int64 `json:"total"`
	SLA             int64 `json:"sla"`
	BusinessLimited int64 `json:"business_limited"`
}

type OpsErrorDistributionResponse struct {
	Total int64                       `json:"total"`
	Items []*OpsErrorDistributionItem `json:"items"`
}

// OpsGroupQualityItem is one row in the admin group-quality ranking. Rates use
// the same 0..1 semantics as the rest of the Ops dashboard; the UI renders them
// as percentages. SampleCount is the SLA-scoped denominator (success +
// non-business-limited client-visible errors), so business-limited requests
// cannot make a healthy group look unreliable.
type OpsGroupQualityItem struct {
	GroupID           int64   `json:"group_id"`
	GroupName         string  `json:"group_name"`
	Platform          string  `json:"platform"`
	RequestCount      int64   `json:"request_count"`
	SuccessCount      int64   `json:"success_count"`
	ErrorCountSLA     int64   `json:"error_count_sla"`
	SuccessRate       float64 `json:"success_rate"`
	UpstreamErrorRate float64 `json:"upstream_error_rate"`
	RateLimitRate     float64 `json:"rate_limit_rate"`
	P95DurationMs     *int    `json:"p95_duration_ms"`
	P95TTFTMs         *int    `json:"p95_ttft_ms"`
	SampleCount       int64   `json:"sample_count"`
	SampleSufficient  bool    `json:"sample_sufficient"`
}

type OpsGroupQualityResponse struct {
	StartTime               time.Time              `json:"start_time"`
	EndTime                 time.Time              `json:"end_time"`
	Platform                string                 `json:"platform"`
	MinSample               int                    `json:"min_sample"`
	Items                   []*OpsGroupQualityItem `json:"items"`
	InsufficientSampleCount int                    `json:"insufficient_sample_count"`
}
