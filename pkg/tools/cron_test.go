package tools

import (
	"context"
	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/config"
	cronpkg "github.com/xwysyy/X-Claw/pkg/cron"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newCronToolForTest(t *testing.T) *CronTool {
	t.Helper()

	workspace := t.TempDir()
	storePath := filepath.Join(workspace, "cron", "jobs.json")
	cronService := cronpkg.NewCronService(storePath, nil)

	tool, err := NewCronTool(
		cronService,
		nil,
		bus.NewMessageBus(),
		workspace,
		true,
		5*time.Second,
		config.DefaultConfig(),
	)
	if err != nil {
		t.Fatalf("failed to construct cron tool: %v", err)
	}
	return tool
}

func TestCronToolAddJob_UsesCronExprWhenZeroNumericFieldsPresent(t *testing.T) {
	tool := newCronToolForTest(t)

	result := tool.Execute(withExecutionContext(context.Background(), "cli", "direct", ""), map[string]any{
		"action":        "add",
		"message":       "daily check",
		"at_seconds":    0,
		"every_seconds": 0,
		"cron_expr":     "*/5 * * * *",
		"timezone":      "Asia/Shanghai",
	})
	if result.IsError {
		t.Fatalf("expected add success, got error: %s", result.ForLLM)
	}

	jobs := tool.cronService.ListJobs(false)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Schedule.Kind != "cron" {
		t.Fatalf("expected cron schedule kind, got %q", jobs[0].Schedule.Kind)
	}
	if jobs[0].Schedule.Expr != "*/5 * * * *" {
		t.Fatalf("expected cron expr to be preserved, got %q", jobs[0].Schedule.Expr)
	}
}

func TestCronToolAddJob_UsesEveryWhenAtIsZero(t *testing.T) {
	tool := newCronToolForTest(t)

	result := tool.Execute(withExecutionContext(context.Background(), "cli", "direct", ""), map[string]any{
		"action":        "add",
		"message":       "hourly check",
		"at_seconds":    0,
		"every_seconds": 3600,
	})
	if result.IsError {
		t.Fatalf("expected add success, got error: %s", result.ForLLM)
	}

	jobs := tool.cronService.ListJobs(false)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Schedule.Kind != "every" {
		t.Fatalf("expected every schedule kind, got %q", jobs[0].Schedule.Kind)
	}
	if jobs[0].Schedule.EveryMS == nil || *jobs[0].Schedule.EveryMS != 3600*1000 {
		t.Fatalf("expected everyMs=3600000, got %+v", jobs[0].Schedule.EveryMS)
	}
}

func TestCronToolAddJob_NegativeSecondsRejected(t *testing.T) {
	tool := newCronToolForTest(t)

	result := tool.Execute(withExecutionContext(context.Background(), "cli", "direct", ""), map[string]any{
		"action":     "add",
		"message":    "invalid",
		"at_seconds": -1,
	})
	if !result.IsError {
		t.Fatalf("expected error for negative at_seconds, got success: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "at_seconds must be >= 0") {
		t.Fatalf("unexpected error text: %s", result.ForLLM)
	}
}

type stubCronExecutor struct {
	lastCh         string
	lastID         string
	lastSessionKey string

	gotChannel     string
	gotChatID      string
	gotSessionKey  string
	usedSessionAPI bool

	response string
	err      error
}

func (s *stubCronExecutor) LastActive() (string, string) {
	return s.lastCh, s.lastID
}

func (s *stubCronExecutor) LastActiveContext() (string, string, string) {
	return s.lastSessionKey, s.lastCh, s.lastID
}

func (s *stubCronExecutor) ProcessDirectWithChannel(_ context.Context, _content, sessionKey, channel, chatID string) (string, error) {
	s.gotChannel = channel
	s.gotChatID = chatID
	s.gotSessionKey = sessionKey
	return s.response, s.err
}

func (s *stubCronExecutor) ProcessSessionMessage(_ context.Context, _content, sessionKey, channel, chatID string) (string, error) {
	s.gotChannel = channel
	s.gotChatID = chatID
	s.gotSessionKey = sessionKey
	s.usedSessionAPI = true
	return s.response, s.err
}

func newCronToolWithExecutorForTest(t *testing.T, exec JobExecutor, mb *bus.MessageBus) *CronTool {
	t.Helper()

	workspace := t.TempDir()
	storePath := filepath.Join(workspace, "cron", "jobs.json")
	cronService := cronpkg.NewCronService(storePath, nil)

	tool, err := NewCronTool(
		cronService,
		exec,
		mb,
		workspace,
		true,
		5*time.Second,
		config.DefaultConfig(),
	)
	if err != nil {
		t.Fatalf("failed to construct cron tool: %v", err)
	}
	return tool
}

func TestCronToolExecuteJob_DeliverFalseUsesDedicatedCronSession(t *testing.T) {
	mb := bus.NewMessageBus()
	exec := &stubCronExecutor{lastCh: "feishu", lastID: "oc_test", response: "ok"}
	tool := newCronToolWithExecutorForTest(t, exec, mb)

	job := &cronpkg.CronJob{
		ID:      "job_session",
		Name:    "sessioned",
		Payload: cronpkg.CronPayload{Message: "do work", Deliver: false},
	}

	_, err := tool.ExecuteJob(context.Background(), job)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if !exec.usedSessionAPI {
		t.Fatalf("expected ProcessSessionMessage to be used")
	}
	if exec.gotSessionKey != "cron-job_session" {
		t.Fatalf("expected cron session key, got %q", exec.gotSessionKey)
	}
}

func TestCronToolExecuteJob_DeliverFalseReusesLastActiveSessionContext(t *testing.T) {
	mb := bus.NewMessageBus()
	exec := &stubCronExecutor{
		lastCh:         "feishu",
		lastID:         "oc_test",
		lastSessionKey: "conv:feishu:direct:oc_test",
		response:       "ok",
	}
	tool := newCronToolWithExecutorForTest(t, exec, mb)

	job := &cronpkg.CronJob{
		ID:      "job_context",
		Name:    "contexted",
		Payload: cronpkg.CronPayload{Message: "do work", Deliver: false},
	}

	_, err := tool.ExecuteJob(context.Background(), job)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if !exec.usedSessionAPI {
		t.Fatalf("expected ProcessSessionMessage to be used")
	}
	if exec.gotSessionKey != "conv:feishu:direct:oc_test" {
		t.Fatalf("expected last active session key, got %q", exec.gotSessionKey)
	}
}

func TestCronToolExecuteJob_BudgetExceededReturnsError(t *testing.T) {
	mb := bus.NewMessageBus()
	exec := &stubCronExecutor{lastCh: "feishu", lastID: "oc_test", response: "RESOURCE_BUDGET_EXCEEDED: run wall time exceeded (300s)."}
	tool := newCronToolWithExecutorForTest(t, exec, mb)

	job := &cronpkg.CronJob{
		ID:      "job_budget",
		Name:    "budgeted",
		Payload: cronpkg.CronPayload{Message: "do work", Deliver: false},
	}

	out, err := tool.ExecuteJob(context.Background(), job)
	if err == nil {
		t.Fatalf("expected error for budget exceeded response")
	}
	if out == "" || !strings.Contains(out, "RESOURCE_BUDGET_EXCEEDED") {
		t.Fatalf("unexpected output: %q", out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	msg, ok := mb.SubscribeOutbound(ctx)
	if !ok {
		t.Fatalf("expected outbound failure message")
	}
	if !strings.Contains(msg.Content, "failed") {
		t.Fatalf("unexpected outbound content: %q", msg.Content)
	}
}

func TestCronToolExecuteJob_DeliverFalsePublishesToLastActive(t *testing.T) {
	mb := bus.NewMessageBus()
	exec := &stubCronExecutor{
		lastCh:    "feishu",
		lastID:    "oc_test",
		response:  "hello from cron",
		gotChatID: "",
	}
	tool := newCronToolWithExecutorForTest(t, exec, mb)

	job := &cronpkg.CronJob{
		ID:   "job1",
		Name: "nightly",
		Payload: cronpkg.CronPayload{
			Message: "do work",
			Deliver: false,
		},
	}

	_, err := tool.ExecuteJob(context.Background(), job)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if exec.gotChannel != "feishu" || exec.gotChatID != "oc_test" {
		t.Fatalf("unexpected executor destination: %q %q", exec.gotChannel, exec.gotChatID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	msg, ok := mb.SubscribeOutbound(ctx)
	if !ok {
		t.Fatalf("expected outbound message")
	}
	if msg.Channel != "feishu" || msg.ChatID != "oc_test" {
		t.Fatalf("unexpected outbound destination: %q %q", msg.Channel, msg.ChatID)
	}
	if !strings.Contains(msg.Content, "Cron job 'nightly' completed.") {
		t.Fatalf("unexpected content: %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "hello from cron") {
		t.Fatalf("missing response in content: %q", msg.Content)
	}
}

func TestCronToolExecuteJob_DeliverFalseNoUpdateIsSilent(t *testing.T) {
	mb := bus.NewMessageBus()
	exec := &stubCronExecutor{
		lastCh:   "feishu",
		lastID:   "oc_test",
		response: "NO_UPDATE",
	}
	tool := newCronToolWithExecutorForTest(t, exec, mb)

	job := &cronpkg.CronJob{
		ID:   "job_no_update",
		Name: "watcher",
		Payload: cronpkg.CronPayload{
			Message: "check updates",
			Deliver: false,
		},
	}

	out, err := tool.ExecuteJob(context.Background(), job)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if strings.TrimSpace(out) != "NO_UPDATE" {
		t.Fatalf("unexpected output: %q", out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if msg, ok := mb.SubscribeOutbound(ctx); ok {
		t.Fatalf("expected no outbound message, got: %+v", msg)
	}
}

func TestCronToolExecuteJob_DeliverTrueUsesLastActive(t *testing.T) {
	mb := bus.NewMessageBus()
	exec := &stubCronExecutor{
		lastCh:         "feishu",
		lastID:         "oc_test",
		lastSessionKey: "conv:feishu:direct:oc_test",
	}
	tool := newCronToolWithExecutorForTest(t, exec, mb)

	job := &cronpkg.CronJob{
		ID:   "job2",
		Name: "reminder",
		Payload: cronpkg.CronPayload{
			Message: "ping",
			Deliver: true,
		},
	}

	_, err := tool.ExecuteJob(context.Background(), job)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	msg, ok := mb.SubscribeOutbound(ctx)
	if !ok {
		t.Fatalf("expected outbound message")
	}
	if msg.Channel != "feishu" || msg.ChatID != "oc_test" {
		t.Fatalf("unexpected outbound destination: %q %q", msg.Channel, msg.ChatID)
	}
	if msg.Content != "ping" {
		t.Fatalf("unexpected content: %q", msg.Content)
	}
	if msg.SessionKey != "" {
		t.Fatalf("expected empty outbound session_key for deliver=true, got %q", msg.SessionKey)
	}
	if job.State.LastSessionKey != "conv:feishu:direct:oc_test" {
		t.Fatalf("LastSessionKey = %q, want %q", job.State.LastSessionKey, "conv:feishu:direct:oc_test")
	}
}

func TestCronToolExecuteJob_CommandPublishesWithoutSessionKey(t *testing.T) {
	mb := bus.NewMessageBus()
	exec := &stubCronExecutor{
		lastCh:         "feishu",
		lastID:         "oc_test",
		lastSessionKey: "conv:feishu:direct:oc_test",
	}
	tool := newCronToolWithExecutorForTest(t, exec, mb)

	job := &cronpkg.CronJob{
		ID:   "job_cmd",
		Name: "commanded",
		Payload: cronpkg.CronPayload{
			Message: "run command",
			Deliver: false,
			Command: "printf 'hello-from-command'",
		},
	}

	out, err := tool.ExecuteJob(context.Background(), job)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if !strings.Contains(out, "hello-from-command") {
		t.Fatalf("expected command output in result, got %q", out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	msg, ok := mb.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("expected outbound message")
	}
	if msg.Channel != "feishu" || msg.ChatID != "oc_test" {
		t.Fatalf("unexpected outbound destination: %q %q", msg.Channel, msg.ChatID)
	}
	if !strings.Contains(msg.Content, "hello-from-command") {
		t.Fatalf("expected command output in outbound message, got %q", msg.Content)
	}
	if msg.SessionKey != "" {
		t.Fatalf("expected empty outbound session_key for command job, got %q", msg.SessionKey)
	}
	if job.State.LastSessionKey != "conv:feishu:direct:oc_test" {
		t.Fatalf("LastSessionKey = %q, want %q", job.State.LastSessionKey, "conv:feishu:direct:oc_test")
	}
}

func TestCronToolExecuteJob_ChannelOnlyUsesLastActiveToWhenSameChannel(t *testing.T) {
	mb := bus.NewMessageBus()
	exec := &stubCronExecutor{
		lastCh:    "feishu",
		lastID:    "oc_last",
		response:  "ok",
		gotChatID: "",
	}
	tool := newCronToolWithExecutorForTest(t, exec, mb)

	job := &cronpkg.CronJob{
		ID:   "job3",
		Name: "weekly",
		Payload: cronpkg.CronPayload{
			Message: "do work",
			Deliver: false,
			Channel: "feishu",
		},
	}

	_, err := tool.ExecuteJob(context.Background(), job)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if exec.gotChatID != "oc_last" {
		t.Fatalf("expected last_active chat id fallback, got %q", exec.gotChatID)
	}
}

func TestCronToolExecuteJob_MissingDestinationErrors(t *testing.T) {
	mb := bus.NewMessageBus()
	tool := newCronToolWithExecutorForTest(t, nil, mb)

	job := &cronpkg.CronJob{
		ID:   "job4",
		Name: "broken",
		Payload: cronpkg.CronPayload{
			Message: "ping",
			Deliver: true,
		},
	}

	_, err := tool.ExecuteJob(context.Background(), job)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestCronToolExecuteJob_DeliverFalsePublishesSessionKeyForReplyBinding(t *testing.T) {
	mb := bus.NewMessageBus()
	exec := &stubCronExecutor{
		lastCh:         "feishu",
		lastID:         "oc_test",
		lastSessionKey: "conv:feishu:direct:oc_test",
		response:       "hello from cron",
	}
	tool := newCronToolWithExecutorForTest(t, exec, mb)

	job := &cronpkg.CronJob{
		ID:      "job_bind",
		Name:    "bindable",
		Payload: cronpkg.CronPayload{Message: "do work", Deliver: false},
	}

	if _, err := tool.ExecuteJob(context.Background(), job); err != nil {
		t.Fatalf("ExecuteJob() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	msg, ok := mb.SubscribeOutbound(ctx)
	if !ok {
		t.Fatal("expected outbound message")
	}
	if msg.SessionKey != "conv:feishu:direct:oc_test" {
		t.Fatalf("outbound session_key = %q, want %q", msg.SessionKey, "conv:feishu:direct:oc_test")
	}
}
