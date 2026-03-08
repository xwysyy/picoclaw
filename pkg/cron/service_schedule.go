package cron

import (
	"crypto/rand"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/adhocore/gronx"
)

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
		preview := truncateRunes(job.State.LastOutputPreview, defaultOutputPreviewRunes)
		sessionKey := strings.TrimSpace(job.State.LastSessionKey)
		errMsg := "aborted: service restarted while job was running"
		job.State.Running = false
		job.State.RunningSinceMS = nil
		job.State.RunningRunID = ""
		job.State.LastRunAtMS = &startedAt
		job.State.LastStatus = "aborted"
		job.State.LastError = errMsg
		job.State.LastOutputPreview = preview
		job.State.LastSessionKey = sessionKey
		job.State.LastDurationMS = &duration
		job.State.RunHistory = append(job.State.RunHistory, CronRunRecord{RunID: runID, StartedAtMS: startedAt, FinishedAtMS: nowMS, DurationMS: duration, Status: "aborted", SessionKey: sessionKey, Error: errMsg, Output: preview})
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
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}
