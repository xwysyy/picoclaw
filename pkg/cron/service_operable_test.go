package cron

import (
	"bytes"
	"log"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCronService_RecordsLifecyclePreviewAndRunHistory(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "cron", "jobs.json")
	logBuf := captureStandardLogOutput(t)

	var handlerCalls atomic.Int32
	cs := NewCronService(storePath, func(_ *CronJob) (string, error) {
		handlerCalls.Add(1)
		return "hello from cron", nil
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
	if got.State.LastOutputPreview == "" {
		t.Fatal("expected LastOutputPreview")
	}
	if !strings.Contains(got.State.LastOutputPreview, "hello from cron") {
		t.Fatalf("LastOutputPreview = %q, want substring %q", got.State.LastOutputPreview, "hello from cron")
	}
	if len(got.State.RunHistory) != 1 {
		t.Fatalf("len(RunHistory) = %d, want 1", len(got.State.RunHistory))
	}
	if got.State.RunHistory[0].RunID == "" {
		t.Fatal("expected RunHistory[0].RunID")
	}
	if got.State.RunHistory[0].Status != "ok" {
		t.Fatalf("RunHistory[0].Status = %q, want %q", got.State.RunHistory[0].Status, "ok")
	}
	if !strings.Contains(got.State.RunHistory[0].Output, "hello from cron") {
		t.Fatalf("RunHistory[0].Output = %q, want substring %q", got.State.RunHistory[0].Output, "hello from cron")
	}

	logs := logBuf.String()
	if !strings.Contains(logs, "[cron] start job="+job.ID+" run_id=") {
		t.Fatalf("expected start log, got %q", logs)
	}
	if !strings.Contains(logs, "[cron] finish job="+job.ID+" ") || !strings.Contains(logs, "status=ok") {
		t.Fatalf("expected finish log with status, got %q", logs)
	}
	if got.State.NextRunAtMS != nil && !strings.Contains(logs, "[cron] next job="+job.ID+" next_run_ms=") {
		t.Fatalf("expected next-run log, got %q", logs)
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

func TestCronService_PrunesStaleRunningState(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "cron", "jobs.json")
	cs := NewCronService(storePath, nil)
	const wantSessionKey = "cron-stale-session"

	job, err := cs.AddJob(
		"stale",
		CronSchedule{Kind: "every", EveryMS: int64Ptr(60_000)},
		"work",
		false,
		"cli",
		"direct",
	)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	startedAtMS := time.Now().Add(-2 * time.Minute).UnixMilli()
	previousRunAtMS := startedAtMS - 30_000
	previousDurationMS := int64(1234)
	job.State.Running = true
	job.State.RunningSinceMS = &startedAtMS
	job.State.RunningRunID = "stale-run"
	job.State.LastRunAtMS = &previousRunAtMS
	job.State.LastStatus = "ok"
	job.State.LastDurationMS = &previousDurationMS
	job.State.LastOutputPreview = "previous preview survives"
	job.State.LastSessionKey = wantSessionKey
	job.State.RunHistory = []CronRunRecord{{
		RunID:        "prior-run",
		StartedAtMS:  previousRunAtMS,
		FinishedAtMS: previousRunAtMS + previousDurationMS,
		DurationMS:   previousDurationMS,
		Status:       "ok",
		Output:       "previous preview survives",
	}}
	if err := cs.UpdateJob(job); err != nil {
		t.Fatalf("UpdateJob failed: %v", err)
	}
	cs = NewCronService(storePath, nil)

	nowMS := time.Now().UnixMilli()
	cs.mu.Lock()
	cs.clearStaleRunningStatesUnsafe(nowMS)
	cs.mu.Unlock()

	jobs := cs.ListJobs(true)
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(jobs))
	}

	got := jobs[0]
	if got.State.Running {
		t.Fatal("expected running=false after stale prune")
	}
	if got.State.LastStatus != "aborted" {
		t.Fatalf("LastStatus = %q, want %q", got.State.LastStatus, "aborted")
	}
	if got.State.LastDurationMS == nil {
		t.Fatal("expected LastDurationMS")
	}
	if got.State.LastOutputPreview != "previous preview survives" {
		t.Fatalf("LastOutputPreview = %q, want preserved preview", got.State.LastOutputPreview)
	}
	if got.State.LastSessionKey != wantSessionKey {
		t.Fatalf("LastSessionKey = %q, want %q", got.State.LastSessionKey, wantSessionKey)
	}
	if len(got.State.RunHistory) != 2 {
		t.Fatalf("len(RunHistory) = %d, want 2", len(got.State.RunHistory))
	}
	if got.State.RunHistory[1].Status != "aborted" {
		t.Fatalf("RunHistory[1].Status = %q, want %q", got.State.RunHistory[1].Status, "aborted")
	}
	if got.State.RunHistory[1].SessionKey != wantSessionKey {
		t.Fatalf("RunHistory[1].SessionKey = %q, want %q", got.State.RunHistory[1].SessionKey, wantSessionKey)
	}
	if got.State.RunHistory[1].Output != "previous preview survives" {
		t.Fatalf("RunHistory[1].Output = %q, want preserved preview", got.State.RunHistory[1].Output)
	}
	if !strings.Contains(got.State.LastError, "service restarted") {
		t.Fatalf("LastError = %q, want restart hint", got.State.LastError)
	}
}

func TestCronService_PersistsHandlerSessionContext(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "cron", "jobs.json")
	const wantSessionKey = "cron-session-test"

	cs := NewCronService(storePath, func(job *CronJob) (string, error) {
		setStringField(t, &job.State, "LastSessionKey", wantSessionKey)
		return "session-aware output", nil
	})

	job, err := cs.AddJob(
		"session-aware",
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

	jobs := cs.ListJobs(true)
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(jobs))
	}

	got := jobs[0]
	if got.State.LastStatus != "ok" {
		t.Fatalf("LastStatus = %q, want %q", got.State.LastStatus, "ok")
	}
	if gotSessionKey := getStringField(t, got.State, "LastSessionKey"); gotSessionKey != wantSessionKey {
		t.Fatalf("LastSessionKey = %q, want %q", gotSessionKey, wantSessionKey)
	}
	if len(got.State.RunHistory) != 1 {
		t.Fatalf("len(RunHistory) = %d, want 1", len(got.State.RunHistory))
	}
	if gotSessionKey := getStringField(t, got.State.RunHistory[0], "SessionKey"); gotSessionKey != wantSessionKey {
		t.Fatalf("RunHistory[0].SessionKey = %q, want %q", gotSessionKey, wantSessionKey)
	}
}

func captureStandardLogOutput(t *testing.T) *bytes.Buffer {
	t.Helper()

	buf := &bytes.Buffer{}
	oldWriter := log.Writer()
	oldFlags := log.Flags()
	oldPrefix := log.Prefix()

	log.SetOutput(buf)
	log.SetFlags(0)
	log.SetPrefix("")

	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	})

	return buf
}

func setStringField(t *testing.T, target any, fieldName, value string) {
	t.Helper()

	v := reflect.ValueOf(target)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		t.Fatalf("target for field %s must be non-nil pointer", fieldName)
	}
	v = v.Elem()
	field := v.FieldByName(fieldName)
	if !field.IsValid() {
		t.Fatalf("missing field %s", fieldName)
	}
	if field.Kind() != reflect.String {
		t.Fatalf("field %s is %s, want string", fieldName, field.Kind())
	}
	if !field.CanSet() {
		t.Fatalf("field %s is not settable", fieldName)
	}
	field.SetString(value)
}

func getStringField(t *testing.T, target any, fieldName string) string {
	t.Helper()

	v := reflect.ValueOf(target)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			t.Fatalf("target for field %s is nil", fieldName)
		}
		v = v.Elem()
	}
	field := v.FieldByName(fieldName)
	if !field.IsValid() {
		t.Fatalf("missing field %s", fieldName)
	}
	if field.Kind() != reflect.String {
		t.Fatalf("field %s is %s, want string", fieldName, field.Kind())
	}
	return field.String()
}
