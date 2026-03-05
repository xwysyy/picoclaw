package cron

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/adhocore/gronx"

	"github.com/xwysyy/picoclaw/pkg/fileutil"
)

type CronSchedule struct {
	Kind    string `json:"kind"`
	AtMS    *int64 `json:"atMs,omitempty"`
	EveryMS *int64 `json:"everyMs,omitempty"`
	Expr    string `json:"expr,omitempty"`
	TZ      string `json:"tz,omitempty"`
}

type CronPayload struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Command string `json:"command,omitempty"`
	Deliver bool   `json:"deliver"`
	Channel string `json:"channel,omitempty"`
	To      string `json:"to,omitempty"`
}

type CronJobState struct {
	NextRunAtMS *int64 `json:"nextRunAtMs,omitempty"`
	LastRunAtMS *int64 `json:"lastRunAtMs,omitempty"`
	LastStatus  string `json:"lastStatus,omitempty"`
	LastError   string `json:"lastError,omitempty"`

	// Running indicates a job is currently executing (best-effort; cleared on restart).
	Running        bool   `json:"running,omitempty"`
	RunningSinceMS *int64 `json:"runningSinceMs,omitempty"`
	RunningRunID   string `json:"runningRunId,omitempty"`

	LastDurationMS    *int64          `json:"lastDurationMs,omitempty"`
	LastOutputPreview string          `json:"lastOutputPreview,omitempty"`
	RunHistory        []CronRunRecord `json:"runHistory,omitempty"`
}

type CronRunRecord struct {
	RunID        string `json:"runId"`
	StartedAtMS  int64  `json:"startedAtMs"`
	FinishedAtMS int64  `json:"finishedAtMs"`
	DurationMS   int64  `json:"durationMs"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
	Output       string `json:"output,omitempty"`
}

type CronJob struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Enabled        bool         `json:"enabled"`
	Schedule       CronSchedule `json:"schedule"`
	Payload        CronPayload  `json:"payload"`
	State          CronJobState `json:"state"`
	CreatedAtMS    int64        `json:"createdAtMs"`
	UpdatedAtMS    int64        `json:"updatedAtMs"`
	DeleteAfterRun bool         `json:"deleteAfterRun"`
}

type CronStore struct {
	Version int       `json:"version"`
	Jobs    []CronJob `json:"jobs"`
}

type JobHandler func(job *CronJob) (string, error)

type CronService struct {
	storePath string
	store     *CronStore
	onJob     JobHandler
	mu        sync.RWMutex
	running   bool
	stopChan  chan struct{}
	gronx     *gronx.Gronx
}

const (
	defaultRunHistoryLimit    = 20
	defaultOutputPreviewRunes = 400
	defaultErrorPreviewRunes  = 800
)

func NewCronService(storePath string, onJob JobHandler) *CronService {
	cs := &CronService{
		storePath: storePath,
		onJob:     onJob,
		gronx:     gronx.New(),
	}
	// Initialize and load store on creation
	cs.loadStore()
	return cs
}

func (cs *CronService) Start() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.running {
		return nil
	}

	if err := cs.loadStore(); err != nil {
		return fmt.Errorf("failed to load store: %w", err)
	}

	// Best-effort recovery: if the service restarts mid-run, clear "running" flags
	// so jobs are operable again and an aborted run is recorded for visibility.
	cs.clearStaleRunningStatesUnsafe(time.Now().UnixMilli())

	cs.recomputeNextRuns()
	if err := cs.saveStoreUnsafe(); err != nil {
		return fmt.Errorf("failed to save store: %w", err)
	}

	cs.stopChan = make(chan struct{})
	cs.running = true
	go cs.runLoop(cs.stopChan)

	return nil
}

func (cs *CronService) Stop() {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if !cs.running {
		return
	}

	cs.running = false
	if cs.stopChan != nil {
		close(cs.stopChan)
		cs.stopChan = nil
	}
}

func (cs *CronService) runLoop(stopChan chan struct{}) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stopChan:
			return
		case <-ticker.C:
			cs.checkJobs()
		}
	}
}

func (cs *CronService) checkJobs() {
	cs.mu.Lock()

	if !cs.running {
		cs.mu.Unlock()
		return
	}

	now := time.Now().UnixMilli()
	var dueJobIDs []string

	// Collect jobs that are due (we need to copy them to execute outside lock)
	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if job.Enabled && !job.State.Running && job.State.NextRunAtMS != nil && *job.State.NextRunAtMS <= now {
			dueJobIDs = append(dueJobIDs, job.ID)
		}
	}

	// Reset next run for due jobs before unlocking to avoid duplicate execution.
	dueMap := make(map[string]bool, len(dueJobIDs))
	for _, jobID := range dueJobIDs {
		dueMap[jobID] = true
	}
	for i := range cs.store.Jobs {
		if dueMap[cs.store.Jobs[i].ID] {
			cs.store.Jobs[i].State.NextRunAtMS = nil
		}
	}

	// Only persist state when we actually changed anything (i.e. a job became due).
	// Otherwise this loop would write the store file every second, causing needless
	// disk churn and log spam (especially under ENOSPC).
	if len(dueJobIDs) > 0 {
		if err := cs.saveStoreUnsafe(); err != nil {
			log.Printf("[cron] failed to save store: %v", err)
		}
	}

	cs.mu.Unlock()

	// Execute jobs outside lock.
	for _, jobID := range dueJobIDs {
		cs.executeJobByID(jobID)
	}
}

func (cs *CronService) executeJobByID(jobID string) {
	startedAt := time.Now()
	startedAtMS := startedAt.UnixMilli()
	runID := generateID()

	// Mark job as running and persist state before executing handler.
	cs.mu.Lock()
	var job *CronJob
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == jobID {
			job = &cs.store.Jobs[i]
			break
		}
	}
	if job == nil {
		cs.mu.Unlock()
		return
	}
	if job.State.Running {
		// Coalesce: do not queue another run for the same job.
		cs.mu.Unlock()
		return
	}

	job.State.Running = true
	job.State.RunningSinceMS = &startedAtMS
	job.State.RunningRunID = runID
	job.UpdatedAtMS = time.Now().UnixMilli()

	jobCopy := *job
	if err := cs.saveStoreUnsafe(); err != nil {
		log.Printf("[cron] failed to save store before run: %v", err)
	}
	cs.mu.Unlock()

	// Execute job outside lock.
	var output string
	var err error
	if cs.onJob != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("cron job panic: %v", r)
				}
			}()
			output, err = cs.onJob(&jobCopy)
		}()
	}

	finishedAt := time.Now()
	finishedAtMS := finishedAt.UnixMilli()
	durationMS := finishedAtMS - startedAtMS
	if durationMS < 0 {
		durationMS = 0
	}

	// Update state and run history.
	cs.mu.Lock()
	defer cs.mu.Unlock()

	job = nil
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == jobID {
			job = &cs.store.Jobs[i]
			break
		}
	}
	if job == nil {
		log.Printf("[cron] job %s disappeared before state update", jobID)
		return
	}

	job.State.Running = false
	job.State.RunningSinceMS = nil
	job.State.RunningRunID = ""

	job.State.LastRunAtMS = &startedAtMS
	job.State.LastDurationMS = &durationMS
	job.State.LastOutputPreview = truncateRunes(output, defaultOutputPreviewRunes)
	job.UpdatedAtMS = time.Now().UnixMilli()

	status := "ok"
	errText := ""
	if err != nil {
		status = "error"
		errText = truncateRunes(err.Error(), defaultErrorPreviewRunes)
	}
	job.State.LastStatus = status
	job.State.LastError = errText

	job.State.RunHistory = append(job.State.RunHistory, CronRunRecord{
		RunID:        runID,
		StartedAtMS:  startedAtMS,
		FinishedAtMS: finishedAtMS,
		DurationMS:   durationMS,
		Status:       status,
		Error:        errText,
		Output:       job.State.LastOutputPreview,
	})
	if len(job.State.RunHistory) > defaultRunHistoryLimit {
		job.State.RunHistory = job.State.RunHistory[len(job.State.RunHistory)-defaultRunHistoryLimit:]
	}

	// Compute next run time (coalesce missed ticks by scheduling from finish time).
	if job.Schedule.Kind == "at" {
		if job.DeleteAfterRun {
			cs.removeJobUnsafe(job.ID)
		} else {
			job.Enabled = false
			job.State.NextRunAtMS = nil
		}
	} else {
		if job.Enabled {
			nextRun := cs.computeNextRun(&job.Schedule, finishedAtMS)
			job.State.NextRunAtMS = nextRun
		} else {
			job.State.NextRunAtMS = nil
		}
	}

	if err := cs.saveStoreUnsafe(); err != nil {
		log.Printf("[cron] failed to save store: %v", err)
	}
}

func (cs *CronService) computeNextRun(schedule *CronSchedule, nowMS int64) *int64 {
	if schedule.Kind == "at" {
		if schedule.AtMS != nil && *schedule.AtMS > nowMS {
			return schedule.AtMS
		}
		return nil
	}

	if schedule.Kind == "every" {
		if schedule.EveryMS == nil || *schedule.EveryMS <= 0 {
			return nil
		}
		next := nowMS + *schedule.EveryMS
		return &next
	}

	if schedule.Kind == "cron" {
		if schedule.Expr == "" {
			return nil
		}

		// Use gronx to calculate next run time
		loc, err := resolveScheduleLocation(schedule.TZ)
		if err != nil {
			log.Printf("[cron] failed to load timezone %q: %v", schedule.TZ, err)
			return nil
		}
		now := time.UnixMilli(nowMS).In(loc)
		nextTime, err := gronx.NextTickAfter(schedule.Expr, now, false)
		if err != nil {
			log.Printf("[cron] failed to compute next run for expr '%s': %v", schedule.Expr, err)
			return nil
		}

		nextMS := nextTime.UnixMilli()
		return &nextMS
	}

	return nil
}

func resolveScheduleLocation(tz string) (*time.Location, error) {
	trimmed := strings.TrimSpace(tz)
	if trimmed == "" || strings.EqualFold(trimmed, "local") {
		return time.Local, nil
	}
	return time.LoadLocation(trimmed)
}

func (cs *CronService) recomputeNextRuns() {
	now := time.Now().UnixMilli()
	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if job.Enabled {
			job.State.NextRunAtMS = cs.computeNextRun(&job.Schedule, now)
		}
	}
}

func (cs *CronService) clearStaleRunningStatesUnsafe(nowMS int64) {
	if cs.store == nil || len(cs.store.Jobs) == 0 {
		return
	}

	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if !job.State.Running {
			continue
		}

		startedAt := nowMS
		if job.State.RunningSinceMS != nil {
			startedAt = *job.State.RunningSinceMS
		}

		runID := strings.TrimSpace(job.State.RunningRunID)
		if runID == "" {
			runID = generateID()
		}

		duration := nowMS - startedAt
		if duration < 0 {
			duration = 0
		}

		errMsg := "aborted: service restarted while job was running"

		job.State.Running = false
		job.State.RunningSinceMS = nil
		job.State.RunningRunID = ""

		job.State.LastRunAtMS = &startedAt
		job.State.LastStatus = "aborted"
		job.State.LastError = errMsg
		job.State.LastOutputPreview = ""
		job.State.LastDurationMS = &duration

		job.State.RunHistory = append(job.State.RunHistory, CronRunRecord{
			RunID:        runID,
			StartedAtMS:  startedAt,
			FinishedAtMS: nowMS,
			DurationMS:   duration,
			Status:       "aborted",
			Error:        errMsg,
		})
		if len(job.State.RunHistory) > defaultRunHistoryLimit {
			job.State.RunHistory = job.State.RunHistory[len(job.State.RunHistory)-defaultRunHistoryLimit:]
		}

		job.UpdatedAtMS = nowMS
	}
}

func (cs *CronService) getNextWakeMS() *int64 {
	var nextWake *int64
	for _, job := range cs.store.Jobs {
		if job.Enabled && job.State.NextRunAtMS != nil {
			if nextWake == nil || *job.State.NextRunAtMS < *nextWake {
				nextWake = job.State.NextRunAtMS
			}
		}
	}
	return nextWake
}

func (cs *CronService) Load() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.loadStore()
}

func (cs *CronService) SetOnJob(handler JobHandler) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.onJob = handler
}

func (cs *CronService) loadStore() error {
	cs.store = &CronStore{
		Version: 1,
		Jobs:    []CronJob{},
	}

	data, err := os.ReadFile(cs.storePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	return json.Unmarshal(data, cs.store)
}

func (cs *CronService) saveStoreUnsafe() error {
	data, err := json.MarshalIndent(cs.store, "", "  ")
	if err != nil {
		return err
	}

	// Use unified atomic write utility with explicit sync for flash storage reliability.
	return fileutil.WriteFileAtomic(cs.storePath, data, 0o600)
}

func (cs *CronService) AddJob(
	name string,
	schedule CronSchedule,
	message string,
	deliver bool,
	channel, to string,
) (*CronJob, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	now := time.Now().UnixMilli()
	if err := cs.validateSchedule(&schedule, now); err != nil {
		return nil, err
	}

	// One-time tasks (at) should be deleted after execution
	deleteAfterRun := (schedule.Kind == "at")

	job := CronJob{
		ID:       generateID(),
		Name:     name,
		Enabled:  true,
		Schedule: schedule,
		Payload: CronPayload{
			Kind:    "agent_turn",
			Message: message,
			Deliver: deliver,
			Channel: channel,
			To:      to,
		},
		State: CronJobState{
			NextRunAtMS: cs.computeNextRun(&schedule, now),
		},
		CreatedAtMS:    now,
		UpdatedAtMS:    now,
		DeleteAfterRun: deleteAfterRun,
	}

	cs.store.Jobs = append(cs.store.Jobs, job)
	if err := cs.saveStoreUnsafe(); err != nil {
		return nil, err
	}

	return &job, nil
}

func (cs *CronService) validateSchedule(schedule *CronSchedule, nowMS int64) error {
	if schedule == nil {
		return fmt.Errorf("schedule is required")
	}

	switch schedule.Kind {
	case "at":
		if schedule.AtMS == nil {
			return fmt.Errorf("at schedule requires atMs")
		}
		if *schedule.AtMS <= nowMS {
			return fmt.Errorf("at schedule time must be in the future")
		}
	case "every":
		if schedule.EveryMS == nil || *schedule.EveryMS <= 0 {
			return fmt.Errorf("every schedule requires everyMs > 0")
		}
	case "cron":
		expr := strings.TrimSpace(schedule.Expr)
		if expr == "" {
			return fmt.Errorf("cron schedule requires expr")
		}
		schedule.Expr = expr
		if !cs.gronx.IsValid(expr) {
			return fmt.Errorf("invalid cron expression: %s", expr)
		}
		if _, err := resolveScheduleLocation(schedule.TZ); err != nil {
			return fmt.Errorf("invalid timezone %q: %w", schedule.TZ, err)
		}
	default:
		return fmt.Errorf("unsupported schedule kind: %s", schedule.Kind)
	}

	return nil
}

func (cs *CronService) UpdateJob(job *CronJob) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == job.ID {
			cs.store.Jobs[i] = *job
			cs.store.Jobs[i].UpdatedAtMS = time.Now().UnixMilli()
			return cs.saveStoreUnsafe()
		}
	}
	return fmt.Errorf("job not found")
}

func (cs *CronService) RemoveJob(jobID string) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	return cs.removeJobUnsafe(jobID)
}

func (cs *CronService) removeJobUnsafe(jobID string) bool {
	before := len(cs.store.Jobs)
	var jobs []CronJob
	for _, job := range cs.store.Jobs {
		if job.ID != jobID {
			jobs = append(jobs, job)
		}
	}
	cs.store.Jobs = jobs
	removed := len(cs.store.Jobs) < before

	if removed {
		if err := cs.saveStoreUnsafe(); err != nil {
			log.Printf("[cron] failed to save store after remove: %v", err)
		}
	}

	return removed
}

func (cs *CronService) EnableJob(jobID string, enabled bool) *CronJob {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if job.ID == jobID {
			job.Enabled = enabled
			job.UpdatedAtMS = time.Now().UnixMilli()

			if enabled {
				job.State.NextRunAtMS = cs.computeNextRun(&job.Schedule, time.Now().UnixMilli())
			} else {
				job.State.NextRunAtMS = nil
			}

			if err := cs.saveStoreUnsafe(); err != nil {
				log.Printf("[cron] failed to save store after enable: %v", err)
			}
			return job
		}
	}

	return nil
}

func (cs *CronService) ListJobs(includeDisabled bool) []CronJob {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	if includeDisabled {
		return cs.store.Jobs
	}

	var enabled []CronJob
	for _, job := range cs.store.Jobs {
		if job.Enabled {
			enabled = append(enabled, job)
		}
	}

	return enabled
}

func (cs *CronService) Status() map[string]any {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	var enabledCount int
	var runningJobs int
	for _, job := range cs.store.Jobs {
		if job.Enabled {
			enabledCount++
		}
		if job.State.Running {
			runningJobs++
		}
	}

	return map[string]any{
		"enabled":      cs.running,
		"jobs":         len(cs.store.Jobs),
		"running_jobs": runningJobs,
		"nextWakeAtMS": cs.getNextWakeMS(),
	}
}

func truncateRunes(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

func generateID() string {
	// Use crypto/rand for better uniqueness under concurrent access
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback to time-based if crypto/rand fails
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
