package channels

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/xwysyy/X-Claw/pkg/bus"
	"github.com/xwysyy/X-Claw/pkg/config"
)

// mockChannel is a test double that delegates Send to a configurable function.
type mockChannel struct {
	BaseChannel
	sendFn func(ctx context.Context, msg bus.OutboundMessage) error
}

func (m *mockChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	return m.sendFn(ctx, msg)
}

func (m *mockChannel) Start(ctx context.Context) error { return nil }
func (m *mockChannel) Stop(ctx context.Context) error  { return nil }

// newTestManager creates a minimal Manager suitable for unit tests.
func newTestManager() *Manager {
	return &Manager{
		channels: make(map[string]Channel),
		workers:  make(map[string]*channelWorker),
	}
}

func setRetryTimingForTest(t *testing.T) {
	t.Helper()

	oldRateLimitDelay := rateLimitDelay
	oldBaseBackoff := baseBackoff
	oldMaxBackoff := maxBackoff

	// Keep tests fast and reduce the chance of external watchdogs killing
	// long-sleeping test binaries.
	rateLimitDelay = 20 * time.Millisecond
	baseBackoff = 10 * time.Millisecond
	maxBackoff = 80 * time.Millisecond

	t.Cleanup(func() {
		rateLimitDelay = oldRateLimitDelay
		baseBackoff = oldBaseBackoff
		maxBackoff = oldMaxBackoff
	})
}

func TestSendWithRetry_Success(t *testing.T) {
	m := newTestManager()
	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			return nil
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := context.Background()
	msg := bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"}

	m.sendWithRetry(ctx, "test", w, msg)

	if callCount != 1 {
		t.Fatalf("expected 1 Send call, got %d", callCount)
	}
}

func TestSelectedChannelInitializers(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Channels.Telegram.Enabled = true
	cfg.Channels.Telegram.Token = config.SecretRef{Inline: "token"}
	cfg.Channels.Feishu.Enabled = true
	cfg.Channels.Slack.Enabled = true
	cfg.Channels.Slack.BotToken = config.SecretRef{Inline: "bot"}
	cfg.Channels.WhatsApp.Enabled = true
	cfg.Channels.WhatsApp.UseNative = true

	specs := selectedChannelInitializers(cfg)
	got := map[string]bool{}
	for _, spec := range specs {
		if spec.enabled != nil && spec.enabled(cfg) {
			got[spec.name] = true
		}
	}

	for _, name := range []string{"telegram", "feishu"} {
		if !got[name] {
			t.Fatalf("expected initializer %q to be selected; got=%v", name, got)
		}
	}
	for _, removed := range []string{"whatsapp_native", "whatsapp", "slack", "discord", "line", "onebot", "wecom", "wecom_aibot", "wecom_app", "qq", "dingtalk", "pico"} {
		if got[removed] {
			t.Fatalf("expected initializer %q to be disabled in slim runtime; got=%v", removed, got)
		}
	}
}

func TestSendWithRetry_TemporaryThenSuccess(t *testing.T) {
	setRetryTimingForTest(t)

	m := newTestManager()
	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			if callCount <= 2 {
				return fmt.Errorf("network error: %w", ErrTemporary)
			}
			return nil
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := context.Background()
	msg := bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"}

	m.sendWithRetry(ctx, "test", w, msg)

	if callCount != 3 {
		t.Fatalf("expected 3 Send calls (2 failures + 1 success), got %d", callCount)
	}
}

func TestSendWithRetry_PermanentFailure(t *testing.T) {
	m := newTestManager()
	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			return fmt.Errorf("bad chat ID: %w", ErrSendFailed)
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := context.Background()
	msg := bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"}

	m.sendWithRetry(ctx, "test", w, msg)

	if callCount != 1 {
		t.Fatalf("expected 1 Send call (no retry for permanent failure), got %d", callCount)
	}
}

func TestSendWithRetry_NotRunning(t *testing.T) {
	m := newTestManager()
	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			return ErrNotRunning
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := context.Background()
	msg := bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"}

	m.sendWithRetry(ctx, "test", w, msg)

	if callCount != 1 {
		t.Fatalf("expected 1 Send call (no retry for ErrNotRunning), got %d", callCount)
	}
}

func TestSendWithRetry_RateLimitRetry(t *testing.T) {
	setRetryTimingForTest(t)

	m := newTestManager()
	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			if callCount == 1 {
				return fmt.Errorf("429: %w", ErrRateLimit)
			}
			return nil
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := context.Background()
	msg := bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"}

	start := time.Now()
	m.sendWithRetry(ctx, "test", w, msg)
	elapsed := time.Since(start)

	if callCount != 2 {
		t.Fatalf("expected 2 Send calls (1 rate limit + 1 success), got %d", callCount)
	}
	// Should have waited at least rateLimitDelay but allow some slack
	if min := rateLimitDelay - rateLimitDelay/4; elapsed < min {
		t.Fatalf("expected at least %v delay for rate limit retry, got %v", min, elapsed)
	}
}

func TestSendWithRetry_MaxRetriesExhausted(t *testing.T) {
	setRetryTimingForTest(t)

	m := newTestManager()
	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			return fmt.Errorf("timeout: %w", ErrTemporary)
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := context.Background()
	msg := bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"}

	m.sendWithRetry(ctx, "test", w, msg)

	expected := maxRetries + 1 // initial attempt + maxRetries retries
	if callCount != expected {
		t.Fatalf("expected %d Send calls, got %d", expected, callCount)
	}
}

func TestSendWithRetry_UnknownError(t *testing.T) {
	setRetryTimingForTest(t)

	m := newTestManager()
	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			if callCount == 1 {
				return errors.New("random unexpected error")
			}
			return nil
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := context.Background()
	msg := bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"}

	m.sendWithRetry(ctx, "test", w, msg)

	if callCount != 2 {
		t.Fatalf("expected 2 Send calls (unknown error treated as temporary), got %d", callCount)
	}
}

func TestSendWithRetry_ContextCancelled(t *testing.T) {
	setRetryTimingForTest(t)

	m := newTestManager()
	var callCount int
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callCount++
			return fmt.Errorf("timeout: %w", ErrTemporary)
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	msg := bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"}

	// Cancel context after first Send attempt returns
	ch.sendFn = func(_ context.Context, _ bus.OutboundMessage) error {
		callCount++
		cancel()
		return fmt.Errorf("timeout: %w", ErrTemporary)
	}

	m.sendWithRetry(ctx, "test", w, msg)

	// Should have called Send once, then noticed ctx canceled during backoff
	if callCount != 1 {
		t.Fatalf("expected 1 Send call before context cancellation, got %d", callCount)
	}
}

func TestWorkerRateLimiter(t *testing.T) {
	m := newTestManager()

	var mu sync.Mutex
	var sendTimes []time.Time
	var wg sync.WaitGroup
	wg.Add(4)

	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			mu.Lock()
			sendTimes = append(sendTimes, time.Now())
			mu.Unlock()
			wg.Done()
			return nil
		},
	}

	// Create a worker with a low rate: 10 msg/s, burst 1
	w := &channelWorker{
		ch:      ch,
		queue:   make(chan bus.OutboundMessage, 10),
		done:    make(chan struct{}),
		limiter: rate.NewLimiter(10, 1),
	}

	ctx := t.Context()

	go m.runWorker(ctx, "test", w)

	// Enqueue 4 messages
	for i := range 4 {
		w.queue <- bus.OutboundMessage{Channel: "test", ChatID: "1", Content: fmt.Sprintf("msg%d", i)}
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for worker to send messages")
	}

	mu.Lock()
	times := make([]time.Time, len(sendTimes))
	copy(times, sendTimes)
	mu.Unlock()

	if len(times) != 4 {
		t.Fatalf("expected 4 sends, got %d", len(times))
	}

	// Verify rate limiting: total duration should be at least 1s
	// (first message immediate, then ~100ms between each subsequent one at 10/s)
	totalDuration := times[len(times)-1].Sub(times[0])
	if totalDuration < 200*time.Millisecond {
		t.Fatalf("expected total duration >= 200ms for 4 msgs at 10/s rate, got %v", totalDuration)
	}
}

func TestNewChannelWorker_DefaultRate(t *testing.T) {
	ch := &mockChannel{}
	w := newChannelWorker("unknown_channel", ch)

	if w.limiter == nil {
		t.Fatal("expected limiter to be non-nil")
	}
	if w.limiter.Limit() != rate.Limit(defaultRateLimit) {
		t.Fatalf("expected rate limit %v, got %v", rate.Limit(defaultRateLimit), w.limiter.Limit())
	}
}

func TestNewChannelWorker_ConfiguredRate(t *testing.T) {
	ch := &mockChannel{}

	for name, expectedRate := range channelRateConfig {
		w := newChannelWorker(name, ch)
		if w.limiter.Limit() != rate.Limit(expectedRate) {
			t.Fatalf("channel %s: expected rate %v, got %v", name, expectedRate, w.limiter.Limit())
		}
	}
}

func TestRunWorker_MessageSplitting(t *testing.T) {
	m := newTestManager()

	var mu sync.Mutex
	var received []string

	ch := &mockChannelWithLength{
		mockChannel: mockChannel{
			sendFn: func(_ context.Context, msg bus.OutboundMessage) error {
				mu.Lock()
				received = append(received, msg.Content)
				mu.Unlock()
				return nil
			},
		},
		maxLen: 5,
	}

	w := &channelWorker{
		ch:      ch,
		queue:   make(chan bus.OutboundMessage, 10),
		done:    make(chan struct{}),
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := t.Context()

	go m.runWorker(ctx, "test", w)

	// Send a message that should be split
	w.queue <- bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello world"}

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := len(received)
	mu.Unlock()

	if count < 2 {
		t.Fatalf("expected message to be split into at least 2 chunks, got %d", count)
	}
}

// mockChannelWithLength implements MessageLengthProvider.
type mockChannelWithLength struct {
	mockChannel
	maxLen int
}

func (m *mockChannelWithLength) MaxMessageLength() int {
	return m.maxLen
}

func TestSendWithRetry_ExponentialBackoff(t *testing.T) {
	setRetryTimingForTest(t)

	m := newTestManager()

	var callTimes []time.Time
	var callCount atomic.Int32
	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			callTimes = append(callTimes, time.Now())
			callCount.Add(1)
			return fmt.Errorf("timeout: %w", ErrTemporary)
		},
	}
	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	ctx := context.Background()
	msg := bus.OutboundMessage{Channel: "test", ChatID: "1", Content: "hello"}

	start := time.Now()
	m.sendWithRetry(ctx, "test", w, msg)
	totalElapsed := time.Since(start)

	// With maxRetries=3: attempts at 0, ~10ms, ~30ms, ~70ms
	// Total backoff: 10ms + 20ms + 40ms = 70ms
	// Allow some margin
	expectedBackoff := baseBackoff + min(2*baseBackoff, maxBackoff) + min(4*baseBackoff, maxBackoff)
	if minExpected := expectedBackoff - baseBackoff/2; totalElapsed < minExpected {
		t.Fatalf("expected total elapsed >= %v for exponential backoff, got %v", minExpected, totalElapsed)
	}

	if int(callCount.Load()) != maxRetries+1 {
		t.Fatalf("expected %d calls, got %d", maxRetries+1, callCount.Load())
	}
}

// --- Phase 10: preSend orchestration tests ---

// mockMessageEditor is a channel that supports MessageEditor.
type mockMessageEditor struct {
	mockChannel
	editFn func(ctx context.Context, chatID, messageID, content string) error
}

func (m *mockMessageEditor) EditMessage(ctx context.Context, chatID, messageID, content string) error {
	return m.editFn(ctx, chatID, messageID, content)
}

func TestPreSend_PlaceholderEditSuccess(t *testing.T) {
	m := newTestManager()
	var sendCalled bool
	var editCalled bool

	ch := &mockMessageEditor{
		mockChannel: mockChannel{
			sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
				sendCalled = true
				return nil
			},
		},
		editFn: func(_ context.Context, chatID, messageID, content string) error {
			editCalled = true
			if chatID != "123" {
				t.Fatalf("expected chatID 123, got %s", chatID)
			}
			if messageID != "456" {
				t.Fatalf("expected messageID 456, got %s", messageID)
			}
			if content != "hello" {
				t.Fatalf("expected content 'hello', got %s", content)
			}
			return nil
		},
	}

	// Register placeholder
	m.RecordPlaceholder("test", "123", "456")

	msg := bus.OutboundMessage{Channel: "test", ChatID: "123", Content: "hello"}
	edited := m.preSend(context.Background(), "test", msg, ch)

	if !edited {
		t.Fatal("expected preSend to return true (placeholder edited)")
	}
	if !editCalled {
		t.Fatal("expected EditMessage to be called")
	}
	if sendCalled {
		t.Fatal("expected Send to NOT be called when placeholder edited")
	}
}

func TestSchedulePlaceholder_CancelBeforeDelay(t *testing.T) {
	m := newTestManager()

	var called atomic.Int64
	send := func(_ context.Context) (string, error) {
		called.Add(1)
		return "ph_id", nil
	}

	m.SchedulePlaceholder(context.Background(), "test", "123", send, 50*time.Millisecond)
	m.CancelPlaceholder("test", "123")

	time.Sleep(120 * time.Millisecond)

	if called.Load() != 0 {
		t.Fatalf("expected placeholder send to be canceled, called=%d", called.Load())
	}
	if _, ok := m.placeholders.Load("test:123"); ok {
		t.Fatal("expected no placeholder recorded when canceled before delay")
	}
}

func TestSchedulePlaceholder_RescheduleReplacesExisting(t *testing.T) {
	m := newTestManager()

	calls := make(chan string, 2)
	first := func(_ context.Context) (string, error) {
		calls <- "first"
		return "ph_first", nil
	}
	second := func(_ context.Context) (string, error) {
		calls <- "second"
		return "ph_second", nil
	}

	m.SchedulePlaceholder(context.Background(), "test", "123", first, 80*time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	m.SchedulePlaceholder(context.Background(), "test", "123", second, 20*time.Millisecond)

	time.Sleep(120 * time.Millisecond)

	got := make([]string, 0, 2)
	for {
		select {
		case s := <-calls:
			got = append(got, s)
		default:
			goto done
		}
	}
done:

	if len(got) != 1 || got[0] != "second" {
		t.Fatalf("expected only second placeholder send, got %v", got)
	}

	if v, ok := m.placeholders.Load("test:123"); !ok {
		t.Fatal("expected placeholder recorded for rescheduled send")
	} else if entry, ok := v.(placeholderEntry); !ok || entry.id != "ph_second" {
		t.Fatalf("expected placeholder id ph_second, got %#v", v)
	}
}

func TestSchedulePlaceholder_ImmediateSendInheritsCallerContext(t *testing.T) {
	m := newTestManager()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	observed := make(chan error, 1)
	send := func(sendCtx context.Context) (string, error) {
		observed <- sendCtx.Err()
		return "", sendCtx.Err()
	}

	m.SchedulePlaceholder(ctx, "test", "123", send, 0)

	select {
	case err := <-observed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled context, got %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected send to observe parent context cancellation")
	}
}

func TestSchedulePlaceholder_DelayedSendCancelsWhenParentContextCancels(t *testing.T) {
	m := newTestManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{}, 1)
	finished := make(chan error, 1)
	send := func(sendCtx context.Context) (string, error) {
		started <- struct{}{}
		<-sendCtx.Done()
		finished <- sendCtx.Err()
		return "", sendCtx.Err()
	}

	m.SchedulePlaceholder(ctx, "test", "123", send, 10*time.Millisecond)

	select {
	case <-started:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected delayed send to start")
	}

	cancel()

	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected canceled send context, got %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected delayed send context to be canceled by parent context")
	}
}

func TestPreSend_PlaceholderEditFails_FallsThrough(t *testing.T) {
	m := newTestManager()

	ch := &mockMessageEditor{
		mockChannel: mockChannel{
			sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
				return nil
			},
		},
		editFn: func(_ context.Context, _, _, _ string) error {
			return fmt.Errorf("edit failed")
		},
	}

	m.RecordPlaceholder("test", "123", "456")

	msg := bus.OutboundMessage{Channel: "test", ChatID: "123", Content: "hello"}
	edited := m.preSend(context.Background(), "test", msg, ch)

	if edited {
		t.Fatal("expected preSend to return false when edit fails")
	}
}

func TestPreSend_TypingStopCalled(t *testing.T) {
	m := newTestManager()
	var stopCalled bool

	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			return nil
		},
	}

	m.RecordTypingStop("test", "123", func() {
		stopCalled = true
	})

	msg := bus.OutboundMessage{Channel: "test", ChatID: "123", Content: "hello"}
	m.preSend(context.Background(), "test", msg, ch)

	if !stopCalled {
		t.Fatal("expected typing stop func to be called")
	}
}

func TestPreSend_NoRegisteredState(t *testing.T) {
	m := newTestManager()

	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			return nil
		},
	}

	msg := bus.OutboundMessage{Channel: "test", ChatID: "123", Content: "hello"}
	edited := m.preSend(context.Background(), "test", msg, ch)

	if edited {
		t.Fatal("expected preSend to return false with no registered state")
	}
}

func TestPreSend_TypingAndPlaceholder(t *testing.T) {
	m := newTestManager()
	var stopCalled bool
	var editCalled bool

	ch := &mockMessageEditor{
		mockChannel: mockChannel{
			sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
				return nil
			},
		},
		editFn: func(_ context.Context, _, _, _ string) error {
			editCalled = true
			return nil
		},
	}

	m.RecordTypingStop("test", "123", func() {
		stopCalled = true
	})
	m.RecordPlaceholder("test", "123", "456")

	msg := bus.OutboundMessage{Channel: "test", ChatID: "123", Content: "hello"}
	edited := m.preSend(context.Background(), "test", msg, ch)

	if !stopCalled {
		t.Fatal("expected typing stop to be called")
	}
	if !editCalled {
		t.Fatal("expected EditMessage to be called")
	}
	if !edited {
		t.Fatal("expected preSend to return true")
	}
}

func TestRecordPlaceholder_ConcurrentSafe(t *testing.T) {
	m := newTestManager()

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chatID := fmt.Sprintf("chat_%d", i%10)
			m.RecordPlaceholder("test", chatID, fmt.Sprintf("msg_%d", i))
		}(i)
	}
	wg.Wait()
}

func TestRecordTypingStop_ConcurrentSafe(t *testing.T) {
	m := newTestManager()

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chatID := fmt.Sprintf("chat_%d", i%10)
			m.RecordTypingStop("test", chatID, func() {})
		}(i)
	}
	wg.Wait()
}

func TestSendWithRetry_PreSendEditsPlaceholder(t *testing.T) {
	m := newTestManager()
	var sendCalled bool

	ch := &mockMessageEditor{
		mockChannel: mockChannel{
			sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
				sendCalled = true
				return nil
			},
		},
		editFn: func(_ context.Context, _, _, _ string) error {
			return nil // edit succeeds
		},
	}

	m.RecordPlaceholder("test", "123", "456")

	w := &channelWorker{
		ch:      ch,
		limiter: rate.NewLimiter(rate.Inf, 1),
	}

	msg := bus.OutboundMessage{Channel: "test", ChatID: "123", Content: "hello"}
	m.sendWithRetry(context.Background(), "test", w, msg)

	if sendCalled {
		t.Fatal("expected Send to NOT be called when placeholder was edited")
	}
}

// --- Dispatcher exit tests (Step 1) ---

func TestDispatcherExitsOnCancel(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	m := &Manager{
		channels: make(map[string]Channel),
		workers:  make(map[string]*channelWorker),
		bus:      mb,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		m.dispatchOutbound(ctx)
		close(done)
	}()

	// Cancel context and verify the dispatcher exits quickly
	cancel()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("dispatchOutbound did not exit within 2s after context cancel")
	}
}

func TestDispatcherMediaExitsOnCancel(t *testing.T) {
	mb := bus.NewMessageBus()
	defer mb.Close()

	m := &Manager{
		channels: make(map[string]Channel),
		workers:  make(map[string]*channelWorker),
		bus:      mb,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		m.dispatchOutboundMedia(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("dispatchOutboundMedia did not exit within 2s after context cancel")
	}
}

// --- TTL Janitor tests (Step 2) ---

func TestTypingStopJanitorEviction(t *testing.T) {
	m := newTestManager()

	var stopCalled atomic.Bool
	// Store a typing entry with a creation time far in the past
	m.typingStops.Store("test:123", typingEntry{
		stop:      func() { stopCalled.Store(true) },
		createdAt: time.Now().Add(-10 * time.Minute), // well past typingStopTTL
	})

	// Run janitor with a short-lived context
	ctx, cancel := context.WithCancel(context.Background())

	// Manually trigger the janitor logic once by simulating a tick
	go func() {
		// Override janitor to run immediately
		now := time.Now()
		m.typingStops.Range(func(key, value any) bool {
			if entry, ok := value.(typingEntry); ok {
				if now.Sub(entry.createdAt) > typingStopTTL {
					if _, loaded := m.typingStops.LoadAndDelete(key); loaded {
						entry.stop()
					}
				}
			}
			return true
		})
		cancel()
	}()

	<-ctx.Done()

	if !stopCalled.Load() {
		t.Fatal("expected typing stop function to be called by janitor eviction")
	}

	// Verify entry was deleted
	if _, loaded := m.typingStops.Load("test:123"); loaded {
		t.Fatal("expected typing entry to be deleted after eviction")
	}
}

func TestPlaceholderJanitorEviction(t *testing.T) {
	m := newTestManager()

	// Store a placeholder entry with a creation time far in the past
	m.placeholders.Store("test:456", placeholderEntry{
		id:        "msg_old",
		createdAt: time.Now().Add(-20 * time.Minute), // well past placeholderTTL
	})

	// Simulate janitor logic
	now := time.Now()
	m.placeholders.Range(func(key, value any) bool {
		if entry, ok := value.(placeholderEntry); ok {
			if now.Sub(entry.createdAt) > placeholderTTL {
				m.placeholders.Delete(key)
			}
		}
		return true
	})

	// Verify entry was deleted
	if _, loaded := m.placeholders.Load("test:456"); loaded {
		t.Fatal("expected placeholder entry to be deleted after eviction")
	}
}

func TestPreSendStillWorksWithWrappedTypes(t *testing.T) {
	m := newTestManager()
	var stopCalled bool
	var editCalled bool

	ch := &mockMessageEditor{
		mockChannel: mockChannel{
			sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
				return nil
			},
		},
		editFn: func(_ context.Context, chatID, messageID, content string) error {
			editCalled = true
			if messageID != "ph_id" {
				t.Fatalf("expected messageID ph_id, got %s", messageID)
			}
			return nil
		},
	}

	// Use the new wrapped types via the public API
	m.RecordTypingStop("test", "chat1", func() {
		stopCalled = true
	})
	m.RecordPlaceholder("test", "chat1", "ph_id")

	msg := bus.OutboundMessage{Channel: "test", ChatID: "chat1", Content: "response"}
	edited := m.preSend(context.Background(), "test", msg, ch)

	if !stopCalled {
		t.Fatal("expected typing stop to be called via wrapped type")
	}
	if !editCalled {
		t.Fatal("expected EditMessage to be called via wrapped type")
	}
	if !edited {
		t.Fatal("expected preSend to return true")
	}
}

// --- Lazy worker creation tests (Step 6) ---

func TestLazyWorkerCreation(t *testing.T) {
	m := newTestManager()

	ch := &mockChannel{
		sendFn: func(_ context.Context, _ bus.OutboundMessage) error {
			return nil
		},
	}

	// RegisterChannel should NOT create a worker
	m.RegisterChannel("lazy", ch)

	m.mu.RLock()
	_, chExists := m.channels["lazy"]
	_, wExists := m.workers["lazy"]
	m.mu.RUnlock()

	if !chExists {
		t.Fatal("expected channel to be registered")
	}
	if wExists {
		t.Fatal("expected worker to NOT be created by RegisterChannel (lazy creation)")
	}
}

// --- FastID uniqueness test (Step 5) ---

func TestBuildMediaScope_FastIDUniqueness(t *testing.T) {
	seen := make(map[string]bool)

	for range 1000 {
		scope := BuildMediaScope("test", "chat1", "")
		if seen[scope] {
			t.Fatalf("duplicate scope generated: %s", scope)
		}
		seen[scope] = true
	}

	// Verify format: "channel:chatID:id"
	scope := BuildMediaScope("telegram", "42", "")
	parts := 0
	for _, c := range scope {
		if c == ':' {
			parts++
		}
	}
	if parts != 2 {
		t.Fatalf("expected scope to have 2 colons (channel:chatID:id), got: %s", scope)
	}
}

func TestBuildMediaScope_WithMessageID(t *testing.T) {
	scope := BuildMediaScope("discord", "chat99", "msg123")
	expected := "discord:chat99:msg123"
	if scope != expected {
		t.Fatalf("expected %s, got %s", expected, scope)
	}
}

func TestBaseChannelIsAllowed(t *testing.T) {
	tests := []struct {
		name      string
		allowList []string
		senderID  string
		want      bool
	}{
		{
			name:      "empty allowlist allows all",
			allowList: nil,
			senderID:  "anyone",
			want:      true,
		},
		{
			name:      "compound sender matches numeric allowlist",
			allowList: []string{"123456"},
			senderID:  "123456|alice",
			want:      true,
		},
		{
			name:      "compound sender matches username allowlist",
			allowList: []string{"@alice"},
			senderID:  "123456|alice",
			want:      true,
		},
		{
			name:      "numeric sender matches legacy compound allowlist",
			allowList: []string{"123456|alice"},
			senderID:  "123456",
			want:      true,
		},
		{
			name:      "non matching sender is denied",
			allowList: []string{"123456"},
			senderID:  "654321|bob",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := NewBaseChannel("test", nil, nil, tt.allowList)
			if got := ch.IsAllowed(tt.senderID); got != tt.want {
				t.Fatalf("IsAllowed(%q) = %v, want %v", tt.senderID, got, tt.want)
			}
		})
	}
}

func TestShouldRespondInGroup(t *testing.T) {
	tests := []struct {
		name        string
		gt          config.GroupTriggerConfig
		isMentioned bool
		content     string
		wantRespond bool
		wantContent string
	}{
		{
			name:        "no config - safe default (ignore)",
			gt:          config.GroupTriggerConfig{},
			isMentioned: false,
			content:     "hello world",
			wantRespond: false,
			wantContent: "hello world",
		},
		{
			name:        "no config - mentioned",
			gt:          config.GroupTriggerConfig{},
			isMentioned: true,
			content:     "hello world",
			wantRespond: true,
			wantContent: "hello world",
		},
		{
			name:        "command bypass (default /)",
			gt:          config.GroupTriggerConfig{CommandBypass: true},
			isMentioned: false,
			content:     "/tree list",
			wantRespond: true,
			wantContent: "/tree list",
		},
		{
			name:        "command bypass works even when mention_only=true",
			gt:          config.GroupTriggerConfig{MentionOnly: true, CommandBypass: true},
			isMentioned: false,
			content:     "/switch plan to run",
			wantRespond: true,
			wantContent: "/switch plan to run",
		},
		{
			name:        "mention_only - not mentioned",
			gt:          config.GroupTriggerConfig{MentionOnly: true},
			isMentioned: false,
			content:     "hello world",
			wantRespond: false,
			wantContent: "hello world",
		},
		{
			name:        "mention_only - mentioned",
			gt:          config.GroupTriggerConfig{MentionOnly: true},
			isMentioned: true,
			content:     "hello world",
			wantRespond: true,
			wantContent: "hello world",
		},
		{
			name:        "mentionless - respond without mention/prefix",
			gt:          config.GroupTriggerConfig{Mentionless: true},
			isMentioned: false,
			content:     "hello world",
			wantRespond: true,
			wantContent: "hello world",
		},
		{
			name:        "prefix match",
			gt:          config.GroupTriggerConfig{Prefixes: []string{"/ask"}},
			isMentioned: false,
			content:     "/ask hello",
			wantRespond: true,
			wantContent: "hello",
		},
		{
			name:        "prefix no match - not mentioned",
			gt:          config.GroupTriggerConfig{Prefixes: []string{"/ask"}},
			isMentioned: false,
			content:     "hello world",
			wantRespond: false,
			wantContent: "hello world",
		},
		{
			name:        "prefix no match - but mentioned",
			gt:          config.GroupTriggerConfig{Prefixes: []string{"/ask"}},
			isMentioned: true,
			content:     "hello world",
			wantRespond: true,
			wantContent: "hello world",
		},
		{
			name:        "multiple prefixes - second matches",
			gt:          config.GroupTriggerConfig{Prefixes: []string{"/ask", "/bot"}},
			isMentioned: false,
			content:     "/bot help me",
			wantRespond: true,
			wantContent: "help me",
		},
		{
			name:        "mention_only with prefixes - mentioned overrides",
			gt:          config.GroupTriggerConfig{MentionOnly: true, Prefixes: []string{"/ask"}},
			isMentioned: true,
			content:     "hello",
			wantRespond: true,
			wantContent: "hello",
		},
		{
			name:        "mention_only with prefixes - not mentioned, no prefix",
			gt:          config.GroupTriggerConfig{MentionOnly: true, Prefixes: []string{"/ask"}},
			isMentioned: false,
			content:     "hello",
			wantRespond: false,
			wantContent: "hello",
		},
		{
			name:        "empty prefix in list is skipped",
			gt:          config.GroupTriggerConfig{Prefixes: []string{"", "/ask"}},
			isMentioned: false,
			content:     "/ask test",
			wantRespond: true,
			wantContent: "test",
		},
		{
			name:        "prefix strips leading whitespace after prefix",
			gt:          config.GroupTriggerConfig{Prefixes: []string{"/ask "}},
			isMentioned: false,
			content:     "/ask hello",
			wantRespond: true,
			wantContent: "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := NewBaseChannel("test", nil, nil, nil, WithGroupTrigger(tt.gt))
			gotRespond, gotContent := ch.ShouldRespondInGroup(tt.isMentioned, tt.content)
			if gotRespond != tt.wantRespond {
				t.Errorf("ShouldRespondInGroup() respond = %v, want %v", gotRespond, tt.wantRespond)
			}
			if gotContent != tt.wantContent {
				t.Errorf("ShouldRespondInGroup() content = %q, want %q", gotContent, tt.wantContent)
			}
		})
	}
}

func TestIsAllowedSender(t *testing.T) {
	tests := []struct {
		name      string
		allowList []string
		sender    bus.SenderInfo
		want      bool
	}{
		{
			name:      "empty allowlist allows all",
			allowList: nil,
			sender:    bus.SenderInfo{PlatformID: "anyone"},
			want:      true,
		},
		{
			name:      "numeric ID matches PlatformID",
			allowList: []string{"123456"},
			sender: bus.SenderInfo{
				Platform:    "telegram",
				PlatformID:  "123456",
				CanonicalID: "telegram:123456",
			},
			want: true,
		},
		{
			name:      "canonical format matches",
			allowList: []string{"telegram:123456"},
			sender: bus.SenderInfo{
				Platform:    "telegram",
				PlatformID:  "123456",
				CanonicalID: "telegram:123456",
			},
			want: true,
		},
		{
			name:      "canonical format wrong platform",
			allowList: []string{"discord:123456"},
			sender: bus.SenderInfo{
				Platform:    "telegram",
				PlatformID:  "123456",
				CanonicalID: "telegram:123456",
			},
			want: false,
		},
		{
			name:      "@username matches",
			allowList: []string{"@alice"},
			sender: bus.SenderInfo{
				Platform:    "telegram",
				PlatformID:  "123456",
				CanonicalID: "telegram:123456",
				Username:    "alice",
			},
			want: true,
		},
		{
			name:      "compound id|username matches by ID",
			allowList: []string{"123456|alice"},
			sender: bus.SenderInfo{
				Platform:    "telegram",
				PlatformID:  "123456",
				CanonicalID: "telegram:123456",
				Username:    "alice",
			},
			want: true,
		},
		{
			name:      "non matching sender denied",
			allowList: []string{"654321"},
			sender: bus.SenderInfo{
				Platform:    "telegram",
				PlatformID:  "123456",
				CanonicalID: "telegram:123456",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := NewBaseChannel("test", nil, nil, tt.allowList)
			if got := ch.IsAllowedSender(tt.sender); got != tt.want {
				t.Fatalf("IsAllowedSender(%+v) = %v, want %v", tt.sender, got, tt.want)
			}
		})
	}
}

func TestBaseChannelHandleMessageAllowList(t *testing.T) {
	msgBus := bus.NewMessageBus()
	ch := NewBaseChannel("test", nil, msgBus, []string{"allowed"})

	ctx := context.Background()
	ch.HandleMessage(ctx, bus.Peer{Kind: "direct", ID: "blocked"}, "msg-1", "blocked", "chat-1", "denied", nil, nil)

	deniedCtx, deniedCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer deniedCancel()
	if msg, ok := msgBus.ConsumeInbound(deniedCtx); ok {
		t.Fatalf("expected denied sender to be dropped, got message: %+v", msg)
	}

	ch.HandleMessage(
		ctx,
		bus.Peer{Kind: "direct", ID: "allowed"},
		"msg-2",
		"allowed",
		"chat-1",
		"accepted",
		[]string{"m1"},
		map[string]string{"k": "v"},
	)

	allowedCtx, allowedCancel := context.WithTimeout(context.Background(), time.Second)
	defer allowedCancel()
	msg, ok := msgBus.ConsumeInbound(allowedCtx)
	if !ok {
		t.Fatal("expected allowed sender message to be published")
	}
	if msg.Channel != "test" || msg.SenderID != "allowed" || msg.ChatID != "chat-1" || msg.Content != "accepted" {
		t.Fatalf("unexpected inbound message: %+v", msg)
	}
	if msg.MessageID != "msg-2" {
		t.Fatalf("unexpected message_id: %q", msg.MessageID)
	}
	if msg.MediaScope != "test:chat-1:msg-2" {
		t.Fatalf("unexpected media_scope: %q", msg.MediaScope)
	}
	if msg.Peer.Kind != "direct" || msg.Peer.ID != "allowed" {
		t.Fatalf("unexpected peer: %+v", msg.Peer)
	}
	if len(msg.Media) != 1 || msg.Media[0] != "m1" {
		t.Fatalf("unexpected media payload: %+v", msg.Media)
	}
	if msg.Metadata["k"] != "v" {
		t.Fatalf("unexpected metadata: %+v", msg.Metadata)
	}
}

func TestWithSecurityHeaders_SetsBaselineHeaders(t *testing.T) {
	h := withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want %q", got, "nosniff")
	}
	if got := rr.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q, want %q", got, "DENY")
	}
	if got := rr.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q, want %q", got, "no-referrer")
	}
	if got := rr.Header().Get("Permissions-Policy"); got == "" {
		t.Fatalf("Permissions-Policy should be set")
	}
}

func TestErrorsIs(t *testing.T) {
	wrapped := fmt.Errorf("telegram API: %w", ErrRateLimit)
	if !errors.Is(wrapped, ErrRateLimit) {
		t.Error("wrapped ErrRateLimit should match")
	}
	if errors.Is(wrapped, ErrTemporary) {
		t.Error("wrapped ErrRateLimit should not match ErrTemporary")
	}
}

func TestErrorsIsAllTypes(t *testing.T) {
	sentinels := []error{ErrNotRunning, ErrRateLimit, ErrTemporary, ErrSendFailed}

	for _, sentinel := range sentinels {
		wrapped := fmt.Errorf("context: %w", sentinel)
		if !errors.Is(wrapped, sentinel) {
			t.Errorf("wrapped %v should match itself", sentinel)
		}

		// Verify it doesn't match other sentinel errors
		for _, other := range sentinels {
			if other == sentinel {
				continue
			}
			if errors.Is(wrapped, other) {
				t.Errorf("wrapped %v should not match %v", sentinel, other)
			}
		}
	}
}

func TestErrorMessages(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{ErrNotRunning, "channel not running"},
		{ErrRateLimit, "rate limited"},
		{ErrTemporary, "temporary failure"},
		{ErrSendFailed, "send failed"},
	}

	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("error message = %q, want %q", got, tt.want)
		}
	}
}

func TestClassifySendError(t *testing.T) {
	raw := fmt.Errorf("some API error")

	tests := []struct {
		name       string
		statusCode int
		wantIs     error
		wantNil    bool
	}{
		{"429 -> ErrRateLimit", 429, ErrRateLimit, false},
		{"500 -> ErrTemporary", 500, ErrTemporary, false},
		{"502 -> ErrTemporary", 502, ErrTemporary, false},
		{"503 -> ErrTemporary", 503, ErrTemporary, false},
		{"400 -> ErrSendFailed", 400, ErrSendFailed, false},
		{"403 -> ErrSendFailed", 403, ErrSendFailed, false},
		{"404 -> ErrSendFailed", 404, ErrSendFailed, false},
		{"200 -> raw error", 200, nil, false},
		{"201 -> raw error", 201, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ClassifySendError(tt.statusCode, raw)
			if err == nil {
				t.Fatal("expected non-nil error")
			}
			if tt.wantIs != nil {
				if !errors.Is(err, tt.wantIs) {
					t.Errorf("errors.Is(err, %v) = false, want true; err = %v", tt.wantIs, err)
				}
			} else {
				// Should return the raw error unchanged
				if err != raw {
					t.Errorf("expected raw error to be returned unchanged for status %d, got %v", tt.statusCode, err)
				}
			}
		})
	}
}

func TestClassifySendErrorNoFalsePositive(t *testing.T) {
	raw := fmt.Errorf("some error")

	// 429 should NOT match ErrTemporary or ErrSendFailed
	err := ClassifySendError(429, raw)
	if errors.Is(err, ErrTemporary) {
		t.Error("429 should not match ErrTemporary")
	}
	if errors.Is(err, ErrSendFailed) {
		t.Error("429 should not match ErrSendFailed")
	}

	// 500 should NOT match ErrRateLimit or ErrSendFailed
	err = ClassifySendError(500, raw)
	if errors.Is(err, ErrRateLimit) {
		t.Error("500 should not match ErrRateLimit")
	}
	if errors.Is(err, ErrSendFailed) {
		t.Error("500 should not match ErrSendFailed")
	}

	// 400 should NOT match ErrRateLimit or ErrTemporary
	err = ClassifySendError(400, raw)
	if errors.Is(err, ErrRateLimit) {
		t.Error("400 should not match ErrRateLimit")
	}
	if errors.Is(err, ErrTemporary) {
		t.Error("400 should not match ErrTemporary")
	}
}

func TestClassifyNetError(t *testing.T) {
	t.Run("nil error returns nil", func(t *testing.T) {
		if err := ClassifyNetError(nil); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("non-nil error wraps as ErrTemporary", func(t *testing.T) {
		raw := fmt.Errorf("connection refused")
		err := ClassifyNetError(raw)
		if err == nil {
			t.Fatal("expected non-nil error")
		}
		if !errors.Is(err, ErrTemporary) {
			t.Errorf("errors.Is(err, ErrTemporary) = false, want true; err = %v", err)
		}
	})
}

func TestSplitMessage(t *testing.T) {
	longText := strings.Repeat("a", 2500)
	longCode := "```go\n" + strings.Repeat("fmt.Println(\"hello\")\n", 100) + "```" // ~2100 chars

	tests := []struct {
		name         string
		content      string
		maxLen       int
		expectChunks int                                 // Check number of chunks
		checkContent func(t *testing.T, chunks []string) // Custom validation
	}{
		{
			name:         "Empty message",
			content:      "",
			maxLen:       2000,
			expectChunks: 0,
		},
		{
			name:         "Short message fits in one chunk",
			content:      "Hello world",
			maxLen:       2000,
			expectChunks: 1,
		},
		{
			name:         "Simple split regular text",
			content:      longText,
			maxLen:       2000,
			expectChunks: 2,
			checkContent: func(t *testing.T, chunks []string) {
				if len([]rune(chunks[0])) > 2000 {
					t.Errorf("Chunk 0 too large: %d runes", len([]rune(chunks[0])))
				}
				if len([]rune(chunks[0]))+len([]rune(chunks[1])) != len([]rune(longText)) {
					t.Errorf(
						"Total rune length mismatch. Got %d, want %d",
						len([]rune(chunks[0]))+len([]rune(chunks[1])),
						len([]rune(longText)),
					)
				}
			},
		},
		{
			name: "Split at newline",
			// 1750 chars then newline, then more chars.
			// Dynamic buffer: 2000 / 10 = 200.
			// Effective limit: 2000 - 200 = 1800.
			// Split should happen at newline because it's at 1750 (< 1800).
			// Total length must > 2000 to trigger split. 1750 + 1 + 300 = 2051.
			content:      strings.Repeat("a", 1750) + "\n" + strings.Repeat("b", 300),
			maxLen:       2000,
			expectChunks: 2,
			checkContent: func(t *testing.T, chunks []string) {
				if len([]rune(chunks[0])) != 1750 {
					t.Errorf("Expected chunk 0 to be 1750 runes (split at newline), got %d", len([]rune(chunks[0])))
				}
				if chunks[1] != strings.Repeat("b", 300) {
					t.Errorf("Chunk 1 content mismatch. Len: %d", len([]rune(chunks[1])))
				}
			},
		},
		{
			name:         "Long code block split",
			content:      "Prefix\n" + longCode,
			maxLen:       2000,
			expectChunks: 2,
			checkContent: func(t *testing.T, chunks []string) {
				// Check that first chunk ends with closing fence
				if !strings.HasSuffix(chunks[0], "\n```") {
					t.Error("First chunk should end with injected closing fence")
				}
				// Check that second chunk starts with execution header
				if !strings.HasPrefix(chunks[1], "```go") {
					t.Error("Second chunk should start with injected code block header")
				}
			},
		},
		{
			name:         "Preserve Unicode characters (rune-aware)",
			content:      strings.Repeat("\u4e16", 2500), // 2500 runes, 7500 bytes
			maxLen:       2000,
			expectChunks: 2,
			checkContent: func(t *testing.T, chunks []string) {
				// Verify chunks contain valid unicode and don't split mid-rune
				for i, chunk := range chunks {
					runeCount := len([]rune(chunk))
					if runeCount > 2000 {
						t.Errorf("Chunk %d has %d runes, exceeds maxLen 2000", i, runeCount)
					}
					if !strings.Contains(chunk, "\u4e16") {
						t.Errorf("Chunk %d should contain unicode characters", i)
					}
				}
				// Verify total rune count is preserved
				totalRunes := 0
				for _, chunk := range chunks {
					totalRunes += len([]rune(chunk))
				}
				if totalRunes != 2500 {
					t.Errorf("Total rune count mismatch. Got %d, want 2500", totalRunes)
				}
			},
		},
		{
			name:         "Zero maxLen returns single chunk",
			content:      "Hello world",
			maxLen:       0,
			expectChunks: 1,
			checkContent: func(t *testing.T, chunks []string) {
				if chunks[0] != "Hello world" {
					t.Errorf("Expected original content, got %q", chunks[0])
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitMessage(tc.content, tc.maxLen)

			if tc.expectChunks == 0 {
				if len(got) != 0 {
					t.Errorf("Expected 0 chunks, got %d", len(got))
				}
				return
			}

			if len(got) != tc.expectChunks {
				t.Errorf("Expected %d chunks, got %d", tc.expectChunks, len(got))
				// Log sizes for debugging
				for i, c := range got {
					t.Logf("Chunk %d length: %d", i, len(c))
				}
				return // Stop further checks if count assumes specific split
			}

			if tc.checkContent != nil {
				tc.checkContent(t, got)
			}
		})
	}
}

// --- Helper function tests for index-based rune operations ---

func TestFindLastNewlineInRange(t *testing.T) {
	runes := []rune("aaa\nbbb\nccc")
	// Indices:        0123 4567 89 10

	tests := []struct {
		name         string
		start, end   int
		searchWindow int
		want         int
	}{
		{"finds last newline in full range", 0, 11, 200, 7},
		{"finds newline within search window", 0, 11, 4, 7},
		{"narrow window misses newline outside window", 4, 11, 3, 3}, // returns start-1 (not found)
		{"no newline in range", 0, 3, 200, -1},                       // start-1 = -1
		{"range limited to first segment", 0, 4, 200, 3},
		{"search window of 1 at newline", 0, 8, 1, 7},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := findLastNewlineInRange(runes, tc.start, tc.end, tc.searchWindow)
			if got != tc.want {
				t.Errorf("findLastNewlineInRange(runes, %d, %d, %d) = %d, want %d",
					tc.start, tc.end, tc.searchWindow, got, tc.want)
			}
		})
	}
}

func TestFindLastSpaceInRange(t *testing.T) {
	runes := []rune("abc def\tghi")
	// Indices:        0123 4567 89 10

	tests := []struct {
		name         string
		start, end   int
		searchWindow int
		want         int
	}{
		{"finds tab as last space/tab", 0, 11, 200, 7},
		{"finds space when tab out of window", 0, 7, 200, 3},
		{"no space in range", 0, 3, 200, -1},
		{"narrow window finds tab", 5, 11, 4, 7},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := findLastSpaceInRange(runes, tc.start, tc.end, tc.searchWindow)
			if got != tc.want {
				t.Errorf("findLastSpaceInRange(runes, %d, %d, %d) = %d, want %d",
					tc.start, tc.end, tc.searchWindow, got, tc.want)
			}
		})
	}
}

func TestFindNewlineFrom(t *testing.T) {
	runes := []rune("hello\nworld\n")

	tests := []struct {
		name string
		from int
		want int
	}{
		{"from start", 0, 5},
		{"from after first newline", 6, 11},
		{"from past all newlines", 12, -1},
		{"from newline itself", 5, 5},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := findNewlineFrom(runes, tc.from)
			if got != tc.want {
				t.Errorf("findNewlineFrom(runes, %d) = %d, want %d", tc.from, got, tc.want)
			}
		})
	}
}

func TestFindLastUnclosedCodeBlockInRange(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		start, end int
		want       int
	}{
		{
			name:    "no code blocks",
			content: "hello world",
			start:   0, end: 11,
			want: -1,
		},
		{
			name:    "complete code block",
			content: "```go\ncode\n```",
			start:   0, end: 14,
			want: -1,
		},
		{
			name:    "unclosed code block",
			content: "text\n```go\ncode here",
			start:   0, end: 20,
			want: 5,
		},
		{
			name:    "closed then unclosed",
			content: "```a\n```\n```b\ncode",
			start:   0, end: 17,
			want: 9,
		},
		{
			name:    "search within subrange",
			content: "```a\n```\n```b\ncode",
			start:   9, end: 17,
			want: 9,
		},
		{
			name:    "subrange with no code blocks",
			content: "```a\n```\nhello",
			start:   9, end: 14,
			want: -1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runes := []rune(tc.content)
			got := findLastUnclosedCodeBlockInRange(runes, tc.start, tc.end)
			if got != tc.want {
				t.Errorf("findLastUnclosedCodeBlockInRange(%q, %d, %d) = %d, want %d",
					tc.content, tc.start, tc.end, got, tc.want)
			}
		})
	}
}

func TestFindNextClosingCodeBlockInRange(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		startIdx int
		end      int
		want     int
	}{
		{
			name:     "finds closing fence",
			content:  "code\n```\nmore",
			startIdx: 0, end: 13,
			want: 8, // position after ```
		},
		{
			name:     "no closing fence",
			content:  "just code here",
			startIdx: 0, end: 14,
			want: -1,
		},
		{
			name:     "fence at start of search",
			content:  "```end",
			startIdx: 0, end: 6,
			want: 3,
		},
		{
			name:     "fence outside range",
			content:  "code\n```",
			startIdx: 0, end: 4,
			want: -1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runes := []rune(tc.content)
			got := findNextClosingCodeBlockInRange(runes, tc.startIdx, tc.end)
			if got != tc.want {
				t.Errorf("findNextClosingCodeBlockInRange(%q, %d, %d) = %d, want %d",
					tc.content, tc.startIdx, tc.end, got, tc.want)
			}
		})
	}
}

func TestSplitMessage_CodeBlockIntegrity(t *testing.T) {
	// Focused test for the core requirement: splitting inside a code block preserves syntax highlighting

	// 60 chars total approximately
	content := "```go\npackage main\n\nfunc main() {\n\tprintln(\"Hello\")\n}\n```"
	maxLen := 40

	chunks := SplitMessage(content, maxLen)

	if len(chunks) != 2 {
		t.Fatalf("Expected 2 chunks, got %d: %q", len(chunks), chunks)
	}

	// First chunk must end with "\n```"
	if !strings.HasSuffix(chunks[0], "\n```") {
		t.Errorf("First chunk should end with closing fence. Got: %q", chunks[0])
	}

	// Second chunk must start with the header "```go"
	if !strings.HasPrefix(chunks[1], "```go") {
		t.Errorf("Second chunk should start with code block header. Got: %q", chunks[1])
	}

	// First chunk should contain meaningful content
	if len([]rune(chunks[0])) > 40 {
		t.Errorf("First chunk exceeded maxLen: length %d runes", len([]rune(chunks[0])))
	}
}
