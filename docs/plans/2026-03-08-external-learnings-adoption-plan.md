# External Learnings Adoption Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 将 `picoclaw` / `openclaw` 近 7 天最有价值的运行时经验，按最小风险路径吸收到 `X-Claw`，优先补强 provider fallback 韧性、cron 可观测性、reply/output 兜底和媒体交付能力。

**Architecture:** 本计划不引入新的公开协议或会话格式；优先复用 `X-Claw` 已有的 `FailoverReason` / cooldown / outbound media bus / cron run state / OAuth primitive，把缺口收敛成小而硬的补丁。执行顺序按“补丁最小 + 风险收益比”排序：先 provider 错误归一化与 fallback 细化，再补 cron 生命周期 trace，再补 direct-answer reasoning fallback 与 `send_file`，最后统一 auth bootstrap。

**Tech Stack:** Go, existing `pkg/providers`, `pkg/agent`, `pkg/cron`, `pkg/tools`, `pkg/auth`, `pkg/channels`, `pkg/bus`, existing Go tests.

---

### Task 1: Provider 错误归一化与 fallback 原因细化

**Files:**
- Modify: `pkg/providers/error_classifier.go`
- Modify: `pkg/providers/error_classifier_test.go`
- Modify: `pkg/providers/fallback.go`
- Modify: `pkg/providers/cooldown.go`
- Modify: `pkg/providers/cooldown_test.go`
- Modify: `pkg/providers/openai_compat/provider.go`
- Modify: `pkg/agent/loop_fallback.go`
- Test: `pkg/providers/error_classifier_test.go`
- Test: `pkg/providers/cooldown_test.go`
- Test: `pkg/providers/fallback_test.go`

**Step 1: 写失败测试，锁定“粗粒度 402 分类”缺口**

在 `pkg/providers/error_classifier_test.go` 增加以下测试：

```go
func TestClassifyError_BillingTemporaryLimitPatterns(t *testing.T) {
    patterns := []string{
        "daily output token limit exceeded",
        "temporary spend limit reached",
        "billing cooldown in effect",
    }
    for _, msg := range patterns {
        result := ClassifyError(errors.New(msg), "openai", "gpt-4.1")
        if result == nil {
            t.Fatalf("%q: expected classification", msg)
        }
        if result.Reason != FailoverBilling {
            t.Fatalf("%q: reason=%q want=%q", msg, result.Reason, FailoverBilling)
        }
    }
}

func TestClassifyError_HTMLGatewayBodyPrefersTimeoutOrRateLimit(t *testing.T) {
    err := errors.New("HTTP 502 <html><title>Bad Gateway</title></html>")
    result := ClassifyError(err, "openai", "gpt-4.1")
    if result == nil {
        t.Fatal("expected classification")
    }
    if result.Reason != FailoverTimeout {
        t.Fatalf("reason=%q want=%q", result.Reason, FailoverTimeout)
    }
}
```

**Step 2: 运行测试，确认当前实现不能覆盖这些场景**

Run: `go test ./pkg/providers -run 'TestClassifyError_(BillingTemporaryLimitPatterns|HTMLGatewayBodyPrefersTimeoutOrRateLimit)' -count=1`
Expected: FAIL，至少有一个 pattern 未被分类或被错误分类。

**Step 3: 引入最小的 provider 错误归一化 helper**

在 `pkg/providers/error_classifier.go` 增加小 helper，不扩散到别的包：

```go
func normalizeProviderErrorMessage(err error) string {
    if err == nil {
        return ""
    }
    msg := strings.ToLower(strings.TrimSpace(err.Error()))
    msg = strings.ReplaceAll(msg, "\n", " ")
    msg = strings.ReplaceAll(msg, "\t", " ")
    return msg
}
```

并在 `ClassifyError` 内统一使用它，随后补充 pattern：

```go
var billingPatterns = []errorPattern{
    rxp(`\b402\b`),
    substr("payment required"),
    substr("insufficient credits"),
    substr("credit balance"),
    substr("plans & billing"),
    substr("insufficient balance"),
    substr("daily output token limit"),
    substr("spend limit"),
    substr("billing cooldown"),
}
```

同时确认 HTML 网关页类字符串仍优先落到状态码路径，而不是被误归类到 `format`。

**Step 4: 把 fallback / cooldown 的输出面再细一点，但不改公开行为**

在 `pkg/providers/fallback.go` / `pkg/providers/cooldown.go` 保持 `FailoverBilling` 不变，只补更清晰的 trace / skipped reason 文本，避免把“临时 spend limit”误读成永久余额不足。

最小示意：

```go
if failErr.Reason == FailoverBilling {
    // 仍走 billing cooldown，但日志文案区分 temporary-limit 与 insufficient-credit
}
```

**Step 5: 运行窄测试，确认分类与 cooldown 仍兼容现有行为**

Run: `go test ./pkg/providers -run 'TestClassifyError_|TestCooldown_' -count=1`
Expected: PASS

**Step 6: 运行 fallback 回归，确认 loop 侧不受破坏**

Run: `go test ./pkg/providers ./pkg/agent -run 'Fallback|Billing|Cooldown' -count=1`
Expected: PASS

**Step 7: Commit**

```bash
git add pkg/providers/error_classifier.go pkg/providers/error_classifier_test.go pkg/providers/fallback.go pkg/providers/cooldown.go pkg/providers/cooldown_test.go pkg/providers/openai_compat/provider.go pkg/agent/loop_fallback.go
git commit -m "fix(providers): harden fallback error normalization"
```

---

### Task 2: Cron 生命周期事件与 run/tool trace 关联

**Files:**
- Modify: `pkg/cron/service.go`
- Modify: `pkg/cron/service_runner.go`
- Modify: `pkg/cron/service_schedule.go`
- Modify: `pkg/tools/cron.go`
- Modify: `pkg/httpapi/console_status.go`
- Test: `pkg/cron/service_operable_test.go`
- Test: `pkg/cron/service_test.go`

**Step 1: 写失败测试，锁定 “已有状态但缺可观测串联” 的缺口**

在 `pkg/cron/service_operable_test.go` 增加：

```go
func TestCronService_RecordsLifecyclePreviewAndRunHistory(t *testing.T) {
    service := newTestCronService(t)
    service.SetOnJob(func(job *CronJob) (string, error) {
        return "hello from cron", nil
    })

    id := mustAddEverySecondJob(t, service)
    service.executeJobByID(id)

    jobs, err := service.ListJobs()
    if err != nil {
        t.Fatal(err)
    }
    got := jobs[0]
    if got.State.LastStatus != "ok" {
        t.Fatalf("LastStatus=%q", got.State.LastStatus)
    }
    if got.State.LastOutputPreview == "" {
        t.Fatal("expected LastOutputPreview")
    }
    if len(got.State.RunHistory) == 0 {
        t.Fatal("expected RunHistory")
    }
}
```

然后再补一个 “aborted stale running job” 测试，锁定 `service_schedule.go` 里的 stale path 不要把可观测字段清空得太干净。

**Step 2: 运行测试，确认缺口存在或行为不稳定**

Run: `go test ./pkg/cron -run 'TestCronService_(RecordsLifecyclePreviewAndRunHistory|PrunesStaleRunningState)' -count=1`
Expected: FAIL 或至少无法覆盖 start/finish/next-run 的串联信息。

**Step 3: 在 runner 内补最小结构化日志，不改外部格式**

在 `pkg/cron/service_runner.go` 中围绕执行生命周期补日志：

```go
log.Printf("[cron] start job=%s run_id=%s", job.ID, runID)
log.Printf("[cron] finish job=%s run_id=%s status=%s duration_ms=%d", job.ID, runID, status, durationMS)
```

如能低成本拿到 next run，则在 `finishJobRunUnsafe` 之后统一打印：

```go
log.Printf("[cron] next job=%s next_run_ms=%d", job.ID, valueOrZero(job.State.NextRunAtMS))
```

**Step 4: 在 `pkg/tools/cron.go` 把 agent run/session 关键信息透到 job state 可追踪字段**

保持现有存储格式，不新增必填字段；如果不适合改 `CronJobState`，先写到 output preview 或日志上下文里。优先最小补丁：

```go
logger.InfoCF("cron", "Executing cron job", map[string]any{
    "job_id": job.ID,
    "session_key": sessionKey,
})
```

**Step 5: 在 `console` 读取面补可见字段**

若 `/api/console/status` 已暴露 cron 信息，则补 `lastStatus` / `lastDurationMS` / `runHistory` 摘要；只读已有字段，不改 API 语义。

**Step 6: 运行定向测试**

Run: `go test ./pkg/cron -count=1`
Expected: PASS

**Step 7: 运行 console/cron 关联面 smoke test**

Run: `go test ./pkg/httpapi -run 'Console|Status' -count=1`
Expected: PASS

**Step 8: Commit**

```bash
git add pkg/cron/service.go pkg/cron/service_runner.go pkg/cron/service_schedule.go pkg/tools/cron.go pkg/httpapi/console_status.go pkg/cron/service_operable_test.go pkg/cron/service_test.go
git commit -m "feat(cron): expose lifecycle and trace-friendly state"
```

---

### Task 3: Direct-answer `ReasoningContent` 兜底

**Files:**
- Modify: `pkg/agent/loop.go`
- Modify: `pkg/agent/pipeline_notify.go`
- Test: `pkg/agent/loop_test.go`
- Test: `pkg/providers/openai_compat/provider_test.go`

**Step 1: 写失败测试，锁定 “content 为空但 reasoning 有值” 的直答缺口**

在 `pkg/agent/loop_test.go` 增加：

```go
func TestAgentLoop_UsesReasoningContentWhenDirectAnswerContentEmpty(t *testing.T) {
    response := &providers.LLMResponse{
        Content:          "",
        ReasoningContent: "fallback reasoning answer",
        ToolCalls:        nil,
    }
    // 用现有 test harness 驱动一次无 tool-call 迭代
    // 断言最终 assistant 输出为 fallback reasoning answer
}
```

**Step 2: 运行测试，确认当前实现失败**

Run: `go test ./pkg/agent -run 'UsesReasoningContentWhenDirectAnswerContentEmpty' -count=1`
Expected: FAIL，当前会落到默认回复或空输出路径。

**Step 3: 在 `pkg/agent/loop.go` 做最小实现**

参考 `picoclaw` 的思路，保持现有行为不变，只在 `len(response.ToolCalls) == 0` 且 `response.Content == ""` 时回退：

```go
if len(response.ToolCalls) == 0 {
    r.finalContent = response.Content
    if r.finalContent == "" && response.ReasoningContent != "" {
        r.finalContent = response.ReasoningContent
    }
    return true
}
```

**Step 4: 保持 `pipeline_notify` 的默认回复兜底不变**

`pkg/agent/pipeline_notify.go` 不要删除默认回复兜底；只确认“ReasoningContent 优先于默认回复”。

**Step 5: 运行定向测试**

Run: `go test ./pkg/agent -run 'ReasoningContent|BuildMessages|SystemPrompt' -count=1`
Expected: PASS

**Step 6: Commit**

```bash
git add pkg/agent/loop.go pkg/agent/pipeline_notify.go pkg/agent/loop_test.go
git commit -m "fix(agent): fall back to reasoning content on direct answers"
```

---

### Task 4: 一等 `send_file` tool，桥接现有 OutboundMedia

**Files:**
- Create: `pkg/tools/send_file.go`
- Create: `pkg/tools/send_file_test.go`
- Modify: `pkg/tools/registry.go`
- Modify: `pkg/agent/registry.go`
- Modify: `pkg/agent/loop_tools.go`
- Modify: `pkg/config/config.go`
- Modify: `pkg/config/defaults.go`
- Test: `pkg/tools/send_file_test.go`
- Test: `pkg/channels/manager_test.go`

**Step 1: 写失败测试，锁定 tool surface 和 media publish 行为**

在 `pkg/tools/send_file_test.go` 新增：

```go
func TestSendFileTool_PublishesOutboundMedia(t *testing.T) {
    // 准备一个临时文件
    // 调用 send_file tool
    // 断言返回 success
    // 断言 OutboundMediaMessage 被发布，且包含 path / mime / filename
}

func TestSendFileTool_RejectsOutsideWorkspaceWhenRestricted(t *testing.T) {
    // 限制工作区时，越界路径必须报错
}
```

**Step 2: 运行测试，确认当前工具不存在**

Run: `go test ./pkg/tools -run 'TestSendFileTool_' -count=1`
Expected: FAIL，当前没有 `send_file`。

**Step 3: 写最小实现**

在 `pkg/tools/send_file.go` 创建最小工具：

```go
type SendFileTool struct {
    workspace string
    restrict  bool
    maxBytes  int64
    publish   func(context.Context, bus.OutboundMediaMessage) error
}

func (t *SendFileTool) Name() string { return "send_file" }
```

执行时：
- 校验路径
- 生成 `bus.MediaPart`
- 调用 `publish(ctx, bus.OutboundMediaMessage{...})`
- 返回稳定 `ToolResult`

**Step 4: 在 agent 注册面接线**

在 `pkg/agent/registry.go` 或等价 shared tool 注册点里，按现有 `message` / `cron` tool 风格接入：

```go
if cfg.Tools.IsToolEnabled("send_file") {
    tool := tools.NewSendFileTool(...)
    tool.SetPublishCallback(func(ctx context.Context, msg bus.OutboundMediaMessage) error {
        return msgBus.PublishOutboundMedia(ctx, msg)
    })
    agent.Tools.Register(tool)
}
```

**Step 5: 运行窄测试**

Run: `go test ./pkg/tools -run 'TestSendFileTool_' -count=1`
Expected: PASS

**Step 6: 运行渠道侧媒体 smoke**

Run: `go test ./pkg/channels -run 'BuildMediaScope_|SendMedia' -count=1`
Expected: PASS

**Step 7: Commit**

```bash
git add pkg/tools/send_file.go pkg/tools/send_file_test.go pkg/tools/registry.go pkg/agent/registry.go pkg/agent/loop_tools.go pkg/config/config.go pkg/config/defaults.go
git commit -m "feat(tools): add send_file outbound media tool"
```

---

### Task 5: 统一 auth bootstrap 入口（P2）

**Files:**
- Create: `pkg/auth/bootstrap.go`
- Modify: `pkg/auth/oauth_browser.go`
- Modify: `pkg/auth/oauth_device.go`
- Modify: `pkg/auth/token.go`
- Modify: `pkg/providers/factory_auth.go`
- Modify: `pkg/auth/oauth_test.go`
- Test: `pkg/auth/oauth_test.go`

**Step 1: 写失败测试，锁定 provider -> bootstrap path 的单点分发**

在 `pkg/auth/oauth_test.go` 增加：

```go
func TestResolveAuthBootstrapMethod(t *testing.T) {
    // anthropic -> oauth/setup-token
    // openai -> oauth browser/device
    // antigravity -> oauth browser
}
```

**Step 2: 运行测试，确认当前没有统一入口**

Run: `go test ./pkg/auth -run 'ResolveAuthBootstrapMethod' -count=1`
Expected: FAIL

**Step 3: 在 `pkg/auth/bootstrap.go` 增加最小 registry**

```go
type BootstrapMethod string

const (
    BootstrapPasteToken BootstrapMethod = "paste_token"
    BootstrapOAuthBrowser BootstrapMethod = "oauth_browser"
    BootstrapOAuthDevice BootstrapMethod = "oauth_device"
)

func ResolveAuthBootstrapMethod(provider string) BootstrapMethod {
    switch strings.TrimSpace(provider) {
    case "openai":
        return BootstrapOAuthBrowser
    case "anthropic":
        return BootstrapPasteToken
    case "google-antigravity", "antigravity":
        return BootstrapOAuthBrowser
    default:
        return BootstrapPasteToken
    }
}
```

**Step 4: 让 provider auth factory 复用这个入口，但不改变现有默认语义**

在 `pkg/providers/factory_auth.go` 侧只复用文案与分发 helper，不改变 credential store 格式。

**Step 5: 运行定向测试**

Run: `go test ./pkg/auth ./pkg/providers -run 'OAuth|Auth|Credential|Bootstrap' -count=1`
Expected: PASS

**Step 6: Commit**

```bash
git add pkg/auth/bootstrap.go pkg/auth/oauth_browser.go pkg/auth/oauth_device.go pkg/auth/token.go pkg/providers/factory_auth.go pkg/auth/oauth_test.go
git commit -m "refactor(auth): centralize provider bootstrap selection"
```

---

### Task 6: Reply / thread / edit 契约测试矩阵（P1）

**Files:**
- Modify: `pkg/channels/manager_test.go`
- Modify: `pkg/httpapi/console_test.go`
- Test: `pkg/channels/manager_test.go`

**Step 1: 写契约测试，先锁 Feishu / Telegram 的最小矩阵**

新增测试点：

```go
func TestReplyContract_EditPlaceholderPreservesReplyBinding(t *testing.T) {}
func TestReplyContract_MediaSendDoesNotBreakMessageBinding(t *testing.T) {}
func TestReplyContract_StopAllClearsReplyCapableWorkers(t *testing.T) {}
```

**Step 2: 运行窄测试，确认覆盖不足或行为漂移**

Run: `go test ./pkg/channels -run 'ReplyContract|SendToChannelAfterStopAll|BuildMediaScope_' -count=1`
Expected: FAIL 或覆盖不足。

**Step 3: 只补最小 test harness / helper，不重构生产代码**

生产代码只有在测试暴露真实缺陷时才改；否则先把契约测稳。

**Step 4: 运行定向测试**

Run: `go test ./pkg/channels -run 'ReplyContract|SelectedChannelInitializers|LazyWorkerCreation|SendToChannelAfterStopAllReturns' -count=1`
Expected: PASS

**Step 5: Commit**

```bash
git add pkg/channels/manager_test.go pkg/httpapi/console_test.go
git commit -m "test(channels): add reply and media contract coverage"
```

---

### Task 7: Config alias / enum drift 加固（P1）

**Files:**
- Modify: `pkg/providers/model_ref_test.go`
- Modify: `pkg/providers/factory_provider_test.go`
- Modify: `pkg/config/migration_test.go`
- Test: `pkg/providers/model_ref_test.go`
- Test: `pkg/config/migration_test.go`

**Step 1: 写 drift 测试**

```go
func TestCanonicalProtocol_AcceptsKnownAliases(t *testing.T) {}
func TestMigration_DefaultProviderAliasesStayInSync(t *testing.T) {}
```

**Step 2: 运行测试确认缺口**

Run: `go test ./pkg/providers ./pkg/config -run 'CanonicalProtocol|Migration|Alias' -count=1`
Expected: FAIL 或空缺覆盖。

**Step 3: 只补测试和最小 helper 调整**

不要新增第二套 alias 表；优先复用现有 `CanonicalProtocol` / `EnsureProtocol`。

**Step 4: 运行定向测试**

Run: `go test ./pkg/providers ./pkg/config -run 'Factory|Fallback|Default|Provider|CanonicalProtocol|Migration' -count=1`
Expected: PASS

**Step 5: Commit**

```bash
git add pkg/providers/model_ref_test.go pkg/providers/factory_provider_test.go pkg/config/migration_test.go
git commit -m "test(config): harden alias and migration drift coverage"
```

---

## Final Verification

执行完所有任务后，运行：

```bash
go build -p 1 ./...
go vet ./...
go test ./... -run '^$' -count=1
go test ./pkg/session -count=1
go test ./pkg/httpapi -count=1
go test ./pkg/channels ./cmd/x-claw/internal/gateway ./pkg/providers -count=1
go test ./pkg/agent -run 'TestSanitizeHistoryForProvider|TestMemoryForSession|TestConcurrentBuildSystemPromptWithCache|TestNewAgentInstance_' -count=1
go test ./pkg/tools -run 'TestExecuteToolCalls_ParallelToolCallsPreserveOrder|TestRegistryExecuteWithContextNilResult|TestShellSessionTable_PrunesTerminalSessionsAtCapacity|TestBackgroundSession_' -count=1
```

如果环境允许，再补：

```bash
go test -race ./pkg/agent -run 'Context|BuildMessages|SystemPrompt' -count=1
go test -race ./pkg/session ./pkg/httpapi -count=1
```

Expected:
- 基线命令全部 PASS
- 若出现 `SIGKILL(137)` / 环境限制，记录替代验证命令与结果


---

## Execution Status (2026-03-08)

### Completed

- [x] Task 5: Centralize auth bootstrap selection
  - Added `pkg/auth/bootstrap.go` with `BootstrapMethod`, `ResolveAuthBootstrapMethod`, `BootstrapMethodHelp`, and `ProviderDisplayName`
  - Updated `pkg/providers/factory_auth.go` to reuse bootstrap-specific guidance without changing provider factory behavior
  - Updated `pkg/auth/token.go` to reuse display-name help while preserving stored `Provider` values
  - Added bootstrap selection coverage in `pkg/auth/oauth_test.go`

- [x] Task 6: Reply / thread / edit contract matrix
  - Added explicit contract tests in `pkg/channels/manager_test.go` for placeholder-edit reply binding, media-send binding preservation, and `StopAll` cleanup semantics
  - No production code changes were required; this task closed coverage gaps only

- [x] Task 7: Config alias / enum drift hardening
  - Added canonical protocol alias coverage in `pkg/providers/model_ref_test.go`
  - Added migration alias drift coverage in `pkg/config/migration_test.go`
  - Fixed real alias drift in `pkg/config/migration.go` for `z.ai` / `z-ai`, `github-copilot`, and `qwen-portal`

### Verification

Executed with workspace-local Go caches:

- `go build -p 1 ./...`
- `go vet ./...`
- `go test ./... -run '^$' -count=1`
- `go test ./pkg/auth ./pkg/providers -run 'ResolveAuthBootstrapMethod|LoginPasteToken|OAuth|Auth|Credential|Bootstrap' -count=1`
- `go test ./pkg/channels -run 'ReplyContract|SelectedChannelInitializers|LazyWorkerCreation|SendToChannelAfterStopAllReturnsNotRunning|BuildMediaScope_' -count=1`
- `go test ./pkg/providers ./pkg/config -run 'Factory|Fallback|Default|Provider|CanonicalProtocol|Migration' -count=1`

Additional environment note:

- `go test ./pkg/auth ./pkg/providers ./pkg/channels ./pkg/config -count=1` hit `signal: killed` in `pkg/channels` in the current environment, so package-level validation was replaced by the targeted `pkg/channels` contract suite above.

### Review Gates

- Task 5: spec review ✅, code quality review ✅
- Task 6: spec review ✅, code quality review ✅
- Task 7: spec review ✅, code quality review ✅
