package cron

import (
	"fmt"
	"log"
	"strings"
	"time"
)

func (cs *CronService) executeJobByID(jobID string) {
	startedAtMS := time.Now().UnixMilli()
	runID := generateID()

	cs.mu.Lock()
	jobCopy, ok := cs.beginJobRunUnsafe(jobID, startedAtMS, runID)
	cs.mu.Unlock()
	if !ok {
		return
	}
	log.Printf("[cron] start job=%s run_id=%s", jobCopy.ID, runID)

	output, runErr := cs.executeJobHandler(&jobCopy)
	finishedAtMS := time.Now().UnixMilli()

	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.finishJobRunUnsafe(jobID, runID, startedAtMS, finishedAtMS, output, runErr, strings.TrimSpace(jobCopy.State.LastSessionKey))
}

func (cs *CronService) beginJobRunUnsafe(jobID string, startedAtMS int64, runID string) (CronJob, bool) {
	job := cs.findJobUnsafe(jobID)
	if job == nil || job.State.Running {
		return CronJob{}, false
	}

	job.State.Running = true
	job.State.RunningSinceMS = &startedAtMS
	job.State.RunningRunID = runID
	job.UpdatedAtMS = time.Now().UnixMilli()

	jobCopy := *job
	if err := cs.saveStoreUnsafe(); err != nil {
		log.Printf("[cron] failed to save store before run: %v", err)
	}
	return jobCopy, true
}

func (cs *CronService) executeJobHandler(job *CronJob) (string, error) {
	if cs.onJob == nil {
		return "", nil
	}

	var (
		output string
		err    error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("cron job panic: %v", r)
			}
		}()
		output, err = cs.onJob(job)
	}()
	return output, err
}

func (cs *CronService) finishJobRunUnsafe(jobID, runID string, startedAtMS, finishedAtMS int64, output string, runErr error, lastSessionKey string) {
	job := cs.findJobUnsafe(jobID)
	if job == nil {
		log.Printf("[cron] job %s disappeared before state update", jobID)
		return
	}
	jobKind := job.Schedule.Kind
	deleteAfterRun := job.DeleteAfterRun

	durationMS := finishedAtMS - startedAtMS
	if durationMS < 0 {
		durationMS = 0
	}

	job.State.Running = false
	job.State.RunningSinceMS = nil
	job.State.RunningRunID = ""
	job.State.LastRunAtMS = &startedAtMS
	job.State.LastDurationMS = &durationMS
	job.State.LastOutputPreview = truncateRunes(output, defaultOutputPreviewRunes)
	job.State.LastSessionKey = lastSessionKey
	job.UpdatedAtMS = time.Now().UnixMilli()

	status, errText := summarizeRunError(runErr)
	job.State.LastStatus = status
	job.State.LastError = errText
	job.State.RunHistory = append(job.State.RunHistory, CronRunRecord{
		RunID:        runID,
		StartedAtMS:  startedAtMS,
		FinishedAtMS: finishedAtMS,
		DurationMS:   durationMS,
		Status:       status,
		SessionKey:   lastSessionKey,
		Error:        errText,
		Output:       job.State.LastOutputPreview,
	})
	if len(job.State.RunHistory) > defaultRunHistoryLimit {
		job.State.RunHistory = job.State.RunHistory[len(job.State.RunHistory)-defaultRunHistoryLimit:]
	}

	cs.updateNextRunAfterCompletionUnsafe(job, finishedAtMS)
	log.Printf("[cron] finish job=%s run_id=%s status=%s duration_ms=%d", jobID, runID, status, durationMS)
	if !(jobKind == "at" && deleteAfterRun) && job.State.NextRunAtMS != nil {
		log.Printf("[cron] next job=%s next_run_ms=%d", jobID, *job.State.NextRunAtMS)
	}
	if err := cs.saveStoreUnsafe(); err != nil {
		log.Printf("[cron] failed to save store: %v", err)
	}
}

func summarizeRunError(runErr error) (status, errText string) {
	status = "ok"
	if runErr == nil {
		return status, ""
	}
	return "error", truncateRunes(runErr.Error(), defaultErrorPreviewRunes)
}

func (cs *CronService) updateNextRunAfterCompletionUnsafe(job *CronJob, finishedAtMS int64) {
	if job.Schedule.Kind == "at" {
		if job.DeleteAfterRun {
			cs.removeJobUnsafe(job.ID)
			return
		}
		job.Enabled = false
		job.State.NextRunAtMS = nil
		return
	}

	if job.Enabled {
		job.State.NextRunAtMS = cs.computeNextRun(&job.Schedule, finishedAtMS)
		return
	}
	job.State.NextRunAtMS = nil
}

func (cs *CronService) findJobUnsafe(jobID string) *CronJob {
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == jobID {
			return &cs.store.Jobs[i]
		}
	}
	return nil
}
