package service

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

const (
	pelicanTestWorkerCount           = 3
	pelicanTestDefaultTimeout        = 20 * time.Minute
	pelicanTestDefaultPersistTimeout = 10 * time.Second
	pelicanTestLeaseTTL              = 2 * time.Minute
	pelicanTestHeartbeatInterval     = 20 * time.Second
	pelicanTestRecoveryInterval      = 30 * time.Second
	pelicanTestPersistAttempts       = 2
	pelicanTestFinishAttempts        = 3
	pelicanTestRecoveryMessage       = "pelican test was interrupted by a service restart"
	pelicanTestLeaseLostMessage      = "pelican test runner lease was lost before this account completed"
	pelicanTestTimeoutResultMessage  = "pelican test timed out before this account completed"
	pelicanTestStoppedResultMessage  = "pelican test stopped before this account completed"
)

var (
	pelicanSVGPattern = regexp.MustCompile(`(?is)<svg\b[^>]*>.*?</svg\s*>`)

	// ErrPelicanTestRunInProgress is returned when another batch is already running.
	ErrPelicanTestRunInProgress = infraerrors.Conflict("PELICAN_TEST_RUN_IN_PROGRESS", "another pelican test batch is already running")
	// ErrPelicanTestServiceStopped prevents new batches from starting during shutdown.
	ErrPelicanTestServiceStopped = errors.New("pelican test service is stopped")
	// ErrPelicanTestServiceNotStarted prevents accepting work before stale batches are recovered.
	ErrPelicanTestServiceNotStarted = errors.New("pelican test service is not started")
)

// PelicanTestAccountRunner is the account-test capability used by batch probes.
// Keeping it as an interface allows timeout and cancellation behavior to be tested
// without making real upstream requests.
type PelicanTestAccountRunner interface {
	RunAccountTestBackground(ctx context.Context, accountID int64, modelID, prompt string) (*ScheduledTestResult, error)
}

// PelicanTestService runs and stores group-level visual generation probes.
type PelicanTestService struct {
	repo           PelicanTestRepository
	accountRepo    AccountRepository
	groupRepo      GroupRepository
	accountTestSvc PelicanTestAccountRunner
	instanceID     string

	runtimeOnce    sync.Once
	lifecycleMu    sync.Mutex
	startMu        sync.Mutex
	lifecycleCtx   context.Context
	cancel         context.CancelFunc
	runSlots       chan struct{}
	runWG          sync.WaitGroup
	started        bool
	stopped        bool
	batchTimeout   time.Duration
	persistTimeout time.Duration
	leaseTTL       time.Duration
	heartbeatEvery time.Duration
	recoveryEvery  time.Duration
}

func NewPelicanTestService(
	repo PelicanTestRepository,
	accountRepo AccountRepository,
	groupRepo GroupRepository,
	accountTestSvc PelicanTestAccountRunner,
) *PelicanTestService {
	s := &PelicanTestService{
		repo:           repo,
		accountRepo:    accountRepo,
		groupRepo:      groupRepo,
		accountTestSvc: accountTestSvc,
		instanceID:     uuid.NewString(),
		batchTimeout:   pelicanTestDefaultTimeout,
		persistTimeout: pelicanTestDefaultPersistTimeout,
		leaseTTL:       pelicanTestLeaseTTL,
		heartbeatEvery: pelicanTestHeartbeatInterval,
		recoveryEvery:  pelicanTestRecoveryInterval,
	}
	s.ensureRuntime()
	return s
}

func (s *PelicanTestService) ensureRuntime() {
	s.runtimeOnce.Do(func() {
		s.lifecycleCtx, s.cancel = context.WithCancel(context.Background())
		s.runSlots = make(chan struct{}, 1)
	})
}

// Start recovers stale batches and starts the lease-recovery loop. It must be
// called before the HTTP server starts accepting new runs.
func (s *PelicanTestService) Start(ctx context.Context) error {
	s.ensureRuntime()
	s.startMu.Lock()
	defer s.startMu.Unlock()

	s.lifecycleMu.Lock()
	if s.stopped {
		s.lifecycleMu.Unlock()
		return ErrPelicanTestServiceStopped
	}
	if s.started {
		s.lifecycleMu.Unlock()
		return nil
	}
	s.lifecycleMu.Unlock()

	recovered, err := s.recoverStaleRuns(ctx)
	if err != nil {
		return fmt.Errorf("recover stale pelican test runs: %w", err)
	}

	s.lifecycleMu.Lock()
	if s.stopped {
		s.lifecycleMu.Unlock()
		return ErrPelicanTestServiceStopped
	}
	s.started = true
	s.runWG.Add(1)
	s.lifecycleMu.Unlock()
	go s.recoveryLoop()
	if recovered > 0 {
		log.Printf("[PelicanTest] recovered %d interrupted run(s)", recovered)
	}
	return nil
}

func (s *PelicanTestService) recoverStaleRuns(ctx context.Context) (int64, error) {
	ttl := s.leaseTTL
	if ttl <= 0 {
		ttl = pelicanTestLeaseTTL
	}
	return s.repo.RecoverStaleRunningRuns(ctx, time.Now().Add(-ttl), pelicanTestRecoveryMessage)
}

func (s *PelicanTestService) recoveryLoop() {
	defer s.runWG.Done()

	interval := s.recoveryEvery
	if interval <= 0 {
		interval = pelicanTestRecoveryInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.lifecycleCtx.Done():
			return
		case <-ticker.C:
			recoveryCtx, cancel := context.WithTimeout(s.lifecycleCtx, pelicanTestDefaultPersistTimeout)
			recovered, err := s.recoverStaleRuns(recoveryCtx)
			cancel()
			if err != nil {
				if s.lifecycleCtx.Err() == nil {
					log.Printf("[PelicanTest] stale run recovery failed: %v", err)
				}
				continue
			}
			if recovered > 0 {
				log.Printf("[PelicanTest] recovered %d stale run(s)", recovered)
			}
		}
	}
}

// Stop cancels active batches and waits briefly for their results and terminal
// run status to be persisted.
func (s *PelicanTestService) Stop(ctx context.Context) error {
	s.ensureRuntime()
	s.lifecycleMu.Lock()
	if !s.stopped {
		s.stopped = true
		s.cancel()
	}
	s.lifecycleMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.runWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StartRun validates the group/model, selects active accounts, persists a running
// batch and executes it in the background.
func (s *PelicanTestService) StartRun(ctx context.Context, req CreatePelicanTestRunRequest, adminID int64) (*PelicanTestRun, error) {
	if req.GroupID <= 0 {
		return nil, errors.New("group_id is required")
	}
	modelID := strings.TrimSpace(req.ModelID)
	if modelID == "" {
		return nil, errors.New("model_id is required")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		prompt = DefaultPelicanTestPrompt
	}

	releaseRun, err := s.registerRun(ctx)
	if err != nil {
		return nil, err
	}
	launched := false
	defer func() {
		if !launched {
			releaseRun()
		}
	}()
	// A crashed process can leave a lease behind without anyone restarting this
	// process. Recover only expired leases before attempting the unique-running
	// insert; a healthy lease in another instance remains untouched and yields 409.
	if _, err := s.recoverStaleRuns(ctx); err != nil {
		return nil, fmt.Errorf("recover stale pelican test runs: %w", err)
	}

	group, err := s.groupRepo.GetByID(ctx, req.GroupID)
	if err != nil || group == nil {
		return nil, errors.New("group not found")
	}
	accounts, err := s.accountRepo.ListByGroup(ctx, req.GroupID)
	if err != nil {
		return nil, fmt.Errorf("list group accounts: %w", err)
	}

	selected, skipped, total := selectPelicanTestAccounts(accounts, req.AccountIDs)
	startedAt := time.Now()
	groupID := req.GroupID
	run := &PelicanTestRun{
		GroupID:       &groupID,
		GroupName:     group.Name,
		ModelID:       modelID,
		Prompt:        prompt,
		Status:        PelicanTestStatusRunning,
		TotalAccounts: total,
		SkippedCount:  skipped,
		CreatedBy:     adminID,
		StartedAt:     startedAt,
		RunnerID:      s.instanceID,
	}
	created, err := s.repo.CreateRun(ctx, run)
	if err != nil {
		return nil, fmt.Errorf("create pelican test run: %w", err)
	}

	go func() {
		defer releaseRun()
		s.executeRun(created, selected)
	}()
	launched = true
	return created, nil
}

// registerRun atomically enforces one in-process batch and tracks the lifecycle so
// Stop cannot race with a new background run.
func (s *PelicanTestService) registerRun(ctx context.Context) (func(), error) {
	s.ensureRuntime()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopped {
		return nil, ErrPelicanTestServiceStopped
	}
	if !s.started {
		return nil, ErrPelicanTestServiceNotStarted
	}
	select {
	case s.runSlots <- struct{}{}:
	default:
		return nil, ErrPelicanTestRunInProgress
	}
	s.runWG.Add(1)
	return func() {
		<-s.runSlots
		s.runWG.Done()
	}, nil
}

// GetRun returns a batch and all persisted account results.
func (s *PelicanTestService) GetRun(ctx context.Context, id int64) (*PelicanTestRunDetail, error) {
	if id <= 0 {
		return nil, errors.New("invalid run id")
	}
	return s.repo.GetRun(ctx, id)
}

// ListRuns returns recent batches, optionally filtered by group.
func (s *PelicanTestService) ListRuns(ctx context.Context, groupID *int64, limit int) ([]PelicanTestRun, error) {
	if groupID != nil && *groupID <= 0 {
		return nil, errors.New("invalid group id")
	}
	return s.repo.ListRuns(ctx, groupID, limit)
}

// DeleteRun removes a completed historical batch.
func (s *PelicanTestService) DeleteRun(ctx context.Context, id int64) error {
	run, err := s.repo.GetRun(ctx, id)
	if err != nil {
		return err
	}
	if run.Status == PelicanTestStatusRunning {
		return errors.New("a running test cannot be deleted")
	}
	return s.repo.DeleteRun(ctx, id)
}

func (s *PelicanTestService) executeRun(run *PelicanTestRun, accounts []Account) {
	timeout := s.batchTimeout
	if timeout <= 0 {
		timeout = pelicanTestDefaultTimeout
	}
	ctx, cancel := context.WithTimeout(s.lifecycleCtx, timeout)
	defer cancel()

	status := PelicanTestStatusCompleted
	runError := ""
	handled := make(map[int64]struct{}, len(accounts))
	remaining := len(accounts)
	var leaseLost atomic.Bool
	heartbeatStop := make(chan struct{})
	var heartbeatWG sync.WaitGroup

	markFailed := func(message string) {
		status = PelicanTestStatusFailed
		if runError == "" {
			runError = message
		} else {
			runError += "; " + message
		}
	}
	persistPending := func(message string) {
		for i := range accounts {
			account := accounts[i]
			if _, ok := handled[account.ID]; ok {
				continue
			}
			handled[account.ID] = struct{}{}
			result := newPelicanTestResult(run, account, message)
			if err := s.persistResult(&result); err != nil {
				markFailed(fmt.Sprintf("persist account %d result: %v", account.ID, err))
			}
		}
	}
	markLeaseLost := func() {
		if leaseLost.CompareAndSwap(false, true) {
			cancel()
		}
	}

	if run.RunnerID != "" && s.repo != nil {
		heartbeatWG.Add(1)
		go func() {
			defer heartbeatWG.Done()
			s.heartbeatLoop(run.ID, run.RunnerID, heartbeatStop, markLeaseLost)
		}()
	}

	defer func() {
		recovered := recover()
		close(heartbeatStop)
		heartbeatWG.Wait()
		if recovered != nil {
			markFailed(fmt.Sprintf("pelican test runner panicked: %v", recovered))
			log.Printf("[PelicanTest] run=%d panic: %v", run.ID, recovered)
			persistPending("pelican test runner stopped before this account completed")
		}
		if leaseLost.Load() {
			markFailed("pelican test runner lease was lost")
			persistPending(pelicanTestLeaseLostMessage)
		}
		s.finishRun(run.ID, run.RunnerID, status, runError)
	}()

	if s.accountTestSvc == nil {
		markFailed("account test service is not configured")
		persistPending("account test service is not configured")
		return
	}
	if len(accounts) == 0 {
		return
	}

	jobs := make(chan Account)
	outcomes := make(chan PelicanTestResult, len(accounts))
	workerCount := pelicanTestWorkerCount
	if len(accounts) < workerCount {
		workerCount = len(accounts)
	}

	var workers sync.WaitGroup
	workers.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case account, ok := <-jobs:
					if !ok {
						return
					}
					if ctx.Err() != nil {
						return
					}
					outcomes <- s.runOneAccount(ctx, run, account)
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i := range accounts {
			select {
			case <-ctx.Done():
				return
			case jobs <- accounts[i]:
			}
		}
	}()
	go func() {
		workers.Wait()
		close(outcomes)
	}()

	timedOut := false
	incompleteResults := false
outcomeLoop:
	for remaining > 0 {
		if ctx.Err() != nil {
			timedOut = true
			break outcomeLoop
		}
		select {
		case result, ok := <-outcomes:
			if !ok {
				timedOut = true
				break outcomeLoop
			}
			if result.AccountID == nil {
				markFailed("pelican test returned a result without an account id")
				incompleteResults = true
				remaining--
				continue
			}
			accountID := *result.AccountID
			if _, exists := handled[accountID]; exists {
				continue
			}
			handled[accountID] = struct{}{}
			remaining--
			if err := s.persistResult(&result); err != nil {
				markFailed(fmt.Sprintf("persist account %d result: %v", accountID, err))
			}
		case <-ctx.Done():
			timedOut = true
			break outcomeLoop
		}
	}

	if timedOut {
		message := pelicanTestTimeoutResultMessage
		if leaseLost.Load() {
			message = pelicanTestLeaseLostMessage
			markFailed("pelican test runner lease was lost")
		} else if errors.Is(ctx.Err(), context.Canceled) {
			message = pelicanTestStoppedResultMessage
			markFailed("pelican test stopped: context canceled")
		} else {
			markFailed(fmt.Sprintf("pelican test timed out after %s", timeout))
		}
		persistPending(message)
	} else if incompleteResults {
		persistPending("pelican test returned a result without an account id")
	}
}

func (s *PelicanTestService) heartbeatLoop(id int64, runnerID string, stop <-chan struct{}, onLeaseLost func()) {
	interval := s.heartbeatEvery
	if interval <= 0 {
		interval = pelicanTestHeartbeatInterval
	}
	timeout := s.persistTimeout
	if timeout <= 0 {
		timeout = pelicanTestDefaultPersistTimeout
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			heartbeatCtx, cancel := context.WithTimeout(context.Background(), timeout)
			owned, err := s.repo.HeartbeatRun(heartbeatCtx, id, runnerID)
			cancel()
			if err != nil {
				log.Printf("[PelicanTest] run=%d heartbeat failed: %v", id, err)
				continue
			}
			if !owned {
				onLeaseLost()
				return
			}
		}
	}
}

func newPelicanTestResult(run *PelicanTestRun, account Account, message string) PelicanTestResult {
	now := time.Now()
	return PelicanTestResult{
		RunID:        run.ID,
		GroupID:      run.GroupID,
		AccountID:    &account.ID,
		AccountName:  account.Name,
		Platform:     account.Platform,
		ModelID:      run.ModelID,
		Status:       PelicanTestResultFailed,
		ErrorMessage: message,
		StartedAt:    now,
		FinishedAt:   now,
	}
}

func (s *PelicanTestService) runOneAccount(ctx context.Context, run *PelicanTestRun, account Account) (result PelicanTestResult) {
	startedAt := time.Now()
	result = PelicanTestResult{
		RunID:       run.ID,
		GroupID:     run.GroupID,
		AccountID:   &account.ID,
		AccountName: account.Name,
		Platform:    account.Platform,
		ModelID:     run.ModelID,
		Status:      PelicanTestResultFailed,
		StartedAt:   startedAt,
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			result.Status = PelicanTestResultFailed
			result.ErrorMessage = fmt.Sprintf("test panicked: %v", recovered)
		}
		result.FinishedAt = time.Now()
		result.LatencyMS = result.FinishedAt.Sub(startedAt).Milliseconds()
	}()

	testResult, testErr := s.accountTestSvc.RunAccountTestBackground(ctx, account.ID, run.ModelID, run.Prompt)
	if testResult != nil {
		result.RawOutput = testResult.ResponseText
		result.ErrorMessage = testResult.ErrorMessage
		if testResult.Status == PelicanTestResultSuccess {
			result.Status = PelicanTestResultSuccess
		}
	}
	if testErr != nil {
		result.Status = PelicanTestResultFailed
		if result.ErrorMessage == "" {
			result.ErrorMessage = testErr.Error()
		}
	}

	if result.Status == PelicanTestResultSuccess {
		svg, svgErr := ExtractPelicanSVG(result.RawOutput)
		if svgErr != nil {
			result.Status = PelicanTestResultFailed
			result.ErrorMessage = svgErr.Error()
		} else {
			result.SVGContent = svg
		}
	}
	if result.Status == PelicanTestResultFailed && result.ErrorMessage == "" {
		result.ErrorMessage = "pelican SVG generation failed"
	}
	return result
}

func (s *PelicanTestService) persistResult(result *PelicanTestResult) error {
	timeout := s.persistTimeout
	if timeout <= 0 {
		timeout = pelicanTestDefaultPersistTimeout
	}
	var lastErr error
	for attempt := 0; attempt < pelicanTestPersistAttempts; attempt++ {
		// Result persistence deliberately does not inherit the batch deadline: a
		// timed-out probe still needs a terminal account result and run counters.
		persistCtx, cancel := context.WithTimeout(context.Background(), timeout)
		_, lastErr = s.repo.CreateResultAndIncrement(persistCtx, result)
		cancel()
		if lastErr == nil {
			return nil
		}
		if attempt+1 < pelicanTestPersistAttempts {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return lastErr
}

func (s *PelicanTestService) finishRun(id int64, runnerID, status, errorMessage string) {
	var lastErr error
	for attempt := 0; attempt < pelicanTestFinishAttempts; attempt++ {
		// FinishRun also uses an independent context so a batch timeout cannot leave
		// the run stuck in running forever.
		finishCtx, cancel := context.WithTimeout(context.Background(), pelicanTestDefaultPersistTimeout)
		lastErr = s.repo.FinishRun(finishCtx, id, runnerID, status, errorMessage)
		cancel()
		if lastErr == nil {
			return
		}
		if attempt+1 < pelicanTestFinishAttempts {
			time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
		}
	}
	log.Printf("[PelicanTest] run=%d finish failed after retries: %v", id, lastErr)
}

// ExtractPelicanSVG extracts and validates one complete SVG document from model
// output. Markdown fences and explanatory text around the document are allowed.
func ExtractPelicanSVG(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("model returned no output")
	}
	svg := strings.TrimSpace(pelicanSVGPattern.FindString(raw))
	if svg == "" {
		lower := strings.ToLower(raw)
		if strings.Contains(lower, "<svg") {
			return "", errors.New("model returned an incomplete SVG document")
		}
		return "", errors.New("model output does not contain an SVG document")
	}
	if !strings.HasPrefix(strings.ToLower(svg), "<svg") {
		return "", errors.New("invalid SVG root element")
	}

	var root struct {
		XMLName xml.Name `xml:"svg"`
	}
	decoder := xml.NewDecoder(strings.NewReader(svg))
	decoder.Strict = false
	if err := decoder.Decode(&root); err != nil {
		return "", fmt.Errorf("invalid SVG document: %w", err)
	}
	if !strings.EqualFold(root.XMLName.Local, "svg") {
		return "", errors.New("invalid SVG root element")
	}
	return svg, nil
}

func selectPelicanTestAccounts(active []Account, requested []int64) (selected []Account, skipped, total int) {
	if len(requested) == 0 {
		return active, 0, len(active)
	}

	activeByID := make(map[int64]Account, len(active))
	for _, account := range active {
		activeByID[account.ID] = account
	}
	seen := make(map[int64]struct{}, len(requested))
	selected = make([]Account, 0, len(requested))
	for _, id := range requested {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		if account, ok := activeByID[id]; ok {
			selected = append(selected, account)
		} else {
			skipped++
		}
	}
	total = len(seen)
	return selected, skipped, total
}
