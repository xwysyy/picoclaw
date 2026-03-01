package cron

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestCronService_RecordsRunHistory(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "cron", "jobs.json")

	var handlerCalls atomic.Int32
	cs := NewCronService(storePath, func(_ *CronJob) (string, error) {
		handlerCalls.Add(1)
		return "hello", nil
	})

	job, err := cs.AddJob(
		"test",
		CronSchedule{Kind: "every", EveryMS: int64Ptr(60_000)},
		"work",
		false,
		"cli",
		"direct",
	)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	cs.executeJobByID(job.ID)

	if handlerCalls.Load() != 1 {
		t.Fatalf("handlerCalls = %d, want 1", handlerCalls.Load())
	}

	jobs := cs.ListJobs(true)
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(jobs))
	}

	got := jobs[0]
	if got.State.Running {
		t.Fatalf("expected running=false after job completes")
	}
	if got.State.LastStatus != "ok" {
		t.Fatalf("LastStatus = %q, want %q", got.State.LastStatus, "ok")
	}
	if got.State.LastRunAtMS == nil {
		t.Fatalf("expected LastRunAtMS to be set")
	}
	if got.State.LastDurationMS == nil {
		t.Fatalf("expected LastDurationMS to be set")
	}
	if got.State.LastOutputPreview != "hello" {
		t.Fatalf("LastOutputPreview = %q, want %q", got.State.LastOutputPreview, "hello")
	}
	if len(got.State.RunHistory) != 1 {
		t.Fatalf("len(RunHistory) = %d, want 1", len(got.State.RunHistory))
	}
	if got.State.RunHistory[0].Status != "ok" {
		t.Fatalf("RunHistory[0].Status = %q, want %q", got.State.RunHistory[0].Status, "ok")
	}
	if got.State.RunHistory[0].Output != "hello" {
		t.Fatalf("RunHistory[0].Output = %q, want %q", got.State.RunHistory[0].Output, "hello")
	}
}

func TestCronService_CoalescesConcurrentRuns(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "cron", "jobs.json")

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})

	var handlerCalls atomic.Int32
	cs := NewCronService(storePath, func(_ *CronJob) (string, error) {
		handlerCalls.Add(1)
		close(started)
		<-release
		return "ok", nil
	})

	job, err := cs.AddJob(
		"test",
		CronSchedule{Kind: "every", EveryMS: int64Ptr(60_000)},
		"work",
		false,
		"cli",
		"direct",
	)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	go func() {
		cs.executeJobByID(job.ID)
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("handler did not start in time")
	}

	// Second run should be coalesced while the first is running.
	cs.executeJobByID(job.ID)

	if handlerCalls.Load() != 1 {
		t.Fatalf("handlerCalls = %d, want 1 (coalesced)", handlerCalls.Load())
	}

	close(release)

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("job did not finish in time")
	}

	jobs := cs.ListJobs(true)
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(jobs))
	}
	if len(jobs[0].State.RunHistory) != 1 {
		t.Fatalf("len(RunHistory) = %d, want 1", len(jobs[0].State.RunHistory))
	}
}
