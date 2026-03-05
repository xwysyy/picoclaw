package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/config"
	cronpkg "github.com/xwysyy/X-Claw/pkg/cron"
)

type stubCronExecutor struct {
	lastCh string
	lastID string

	gotChannel string
	gotChatID  string

	response string
	err      error
}

func (s *stubCronExecutor) LastActive() (string, string) {
	return s.lastCh, s.lastID
}

func (s *stubCronExecutor) ProcessDirectWithChannel(_ context.Context, _content, _sessionKey, channel, chatID string) (string, error) {
	s.gotChannel = channel
	s.gotChatID = chatID
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
		lastCh: "feishu",
		lastID: "oc_test",
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
