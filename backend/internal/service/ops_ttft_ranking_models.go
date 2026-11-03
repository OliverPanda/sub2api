package service

import "time"

type OpsTTFTRankingDimension string

const (
	OpsTTFTRankingByGroup   OpsTTFTRankingDimension = "group"
	OpsTTFTRankingByAccount OpsTTFTRankingDimension = "account"
)

type OpsTTFTRankingFilter struct {
	StartTime time.Time
	EndTime   time.Time
	Dimension OpsTTFTRankingDimension
	Platform  string
}

type OpsTTFTRankingItem struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Platform     string   `json:"platform"`
	RequestCount int64    `json:"request_count"`
	SampleCount  int64    `json:"sample_count"`
	AvgMs        *float64 `json:"avg_ms"`
	P50Ms        *float64 `json:"p50_ms"`
	P95Ms        *float64 `json:"p95_ms"`
	P99Ms        *float64 `json:"p99_ms"`
	MaxMs        *int64   `json:"max_ms"`
}

type OpsTTFTRankingResponse struct {
	StartTime time.Time               `json:"start_time"`
	EndTime   time.Time               `json:"end_time"`
	Dimension OpsTTFTRankingDimension `json:"dimension"`
	Platform  string                  `json:"platform,omitempty"`
	Items     []*OpsTTFTRankingItem   `json:"items"`
}
