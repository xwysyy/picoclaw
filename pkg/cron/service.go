package cron

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/adhocore/gronx"
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

	Running        bool   `json:"running,omitempty"`
	RunningSinceMS *int64 `json:"runningSinceMs,omitempty"`
	RunningRunID   string `json:"runningRunId,omitempty"`

	LastDurationMS    *int64          `json:"lastDurationMs,omitempty"`
	LastOutputPreview string          `json:"lastOutputPreview,omitempty"`
	LastSessionKey    string          `json:"lastSessionKey,omitempty"`
	RunHistory        []CronRunRecord `json:"runHistory,omitempty"`
}

type CronRunRecord struct {
	RunID        string `json:"runId"`
	StartedAtMS  int64  `json:"startedAtMs"`
	FinishedAtMS int64  `json:"finishedAtMs"`
	DurationMS   int64  `json:"durationMs"`
	Status       string `json:"status"`
	SessionKey   string `json:"sessionKey,omitempty"`
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
	for {
		wait := cs.nextLoopWait()
		timer := time.NewTimer(wait)
		select {
		case <-stopChan:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
			cs.checkJobs()
		}
	}
}

func (cs *CronService) nextLoopWait() time.Duration {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if !cs.running {
		return 5 * time.Second
	}

	nextWake := cs.getNextWakeMS()
	if nextWake == nil {
		return 5 * time.Second
	}

	delay := time.Until(time.UnixMilli(*nextWake))
	if delay <= 0 {
		return 0
	}
	if delay > 5*time.Second {
		return 5 * time.Second
	}
	return delay
}

func (cs *CronService) checkJobs() {
	cs.mu.Lock()
	if !cs.running {
		cs.mu.Unlock()
		return
	}

	now := time.Now().UnixMilli()
	var dueJobIDs []string
	for i := range cs.store.Jobs {
		job := &cs.store.Jobs[i]
		if job.Enabled && !job.State.Running && job.State.NextRunAtMS != nil && *job.State.NextRunAtMS <= now {
			dueJobIDs = append(dueJobIDs, job.ID)
		}
	}

	dueMap := make(map[string]bool, len(dueJobIDs))
	for _, jobID := range dueJobIDs {
		dueMap[jobID] = true
	}
	for i := range cs.store.Jobs {
		if dueMap[cs.store.Jobs[i].ID] {
			cs.store.Jobs[i].State.NextRunAtMS = nil
		}
	}

	if len(dueJobIDs) > 0 {
		if err := cs.saveStoreUnsafe(); err != nil {
			log.Printf("[cron] failed to save store: %v", err)
		}
	}
	cs.mu.Unlock()

	for _, jobID := range dueJobIDs {
		cs.executeJobByID(jobID)
	}
}
