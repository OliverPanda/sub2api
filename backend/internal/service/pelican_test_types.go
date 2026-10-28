package service

import (
	"context"
	"time"
)

const (
	PelicanTestStatusRunning   = "running"
	PelicanTestStatusCompleted = "completed"
	PelicanTestStatusFailed    = "failed"

	PelicanTestResultSuccess = "success"
	PelicanTestResultFailed  = "failed"

	DefaultPelicanTestPrompt = "请只用 SVG 绘制一只骑自行车的鹈鹕。只输出完整、可独立渲染的 SVG 代码，不要解释，不要使用图像生成工具。"
)

// PelicanTestRun is one batch SVG probe across the active accounts of a group.
type PelicanTestRun struct {
	ID            int64      `json:"id"`
	GroupID       *int64     `json:"group_id"`
	GroupName     string     `json:"group_name"`
	ModelID       string     `json:"model_id"`
	Prompt        string     `json:"prompt"`
	Status        string     `json:"status"`
	TotalAccounts int        `json:"total_accounts"`
	SuccessCount  int        `json:"success_count"`
	FailedCount   int        `json:"failed_count"`
	SkippedCount  int        `json:"skipped_count"`
	ErrorMessage  string     `json:"error_message"`
	CreatedBy     int64      `json:"created_by"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	CreatedAt     time.Time  `json:"created_at"`
	RunnerID      string     `json:"-"`
}

// PelicanTestResult is the persisted probe result for one account.
type PelicanTestResult struct {
	ID           int64     `json:"id"`
	RunID        int64     `json:"run_id"`
	GroupID      *int64    `json:"group_id"`
	AccountID    *int64    `json:"account_id"`
	AccountName  string    `json:"account_name"`
	Platform     string    `json:"platform"`
	ModelID      string    `json:"model_id"`
	Status       string    `json:"status"`
	RawOutput    string    `json:"raw_output"`
	SVGContent   string    `json:"svg_content"`
	ErrorMessage string    `json:"error_message"`
	LatencyMS    int64     `json:"latency_ms"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	CreatedAt    time.Time `json:"created_at"`
}

// PelicanTestRunDetail is returned when viewing one historical batch.
type PelicanTestRunDetail struct {
	PelicanTestRun
	Results []PelicanTestResult `json:"results"`
}

// CreatePelicanTestRunRequest starts a batch probe for a group.
type CreatePelicanTestRunRequest struct {
	GroupID    int64   `json:"group_id"`
	ModelID    string  `json:"model_id"`
	AccountIDs []int64 `json:"account_ids"`
	Prompt     string  `json:"prompt"`
}

// PelicanTestRepository persists batches and their per-account results.
type PelicanTestRepository interface {
	CreateRun(ctx context.Context, run *PelicanTestRun) (*PelicanTestRun, error)
	GetRun(ctx context.Context, id int64) (*PelicanTestRunDetail, error)
	ListRuns(ctx context.Context, groupID *int64, limit int) ([]PelicanTestRun, error)
	CreateResultAndIncrement(ctx context.Context, result *PelicanTestResult) (*PelicanTestResult, error)
	HeartbeatRun(ctx context.Context, id int64, runnerID string) (bool, error)
	FinishRun(ctx context.Context, id int64, runnerID, status, errorMessage string) error
	RecoverStaleRunningRuns(ctx context.Context, staleBefore time.Time, errorMessage string) (int64, error)
	DeleteRun(ctx context.Context, id int64) error
}
