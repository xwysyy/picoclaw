# 2026-03-08 External Learnings Adoption Progress

## Status

已完成本轮落地的 Task 3–7：

- [x] Task 3: Direct-answer `ReasoningContent` fallback
- [x] Task 4: first-class `send_file` tool
- [x] Task 5: centralized auth bootstrap selection
- [x] Task 6: reply / thread / edit contract matrix
- [x] Task 7: config alias / migration drift hardening

## Implemented

### Task 3

- `pkg/agent/loop.go`
- `pkg/agent/loop_test.go`
- 仅在 direct-answer 且 `Content == ""`、无 tool calls 时回退到 `ReasoningContent`
- 保留 `pkg/agent/pipeline_notify.go` 的默认回复兜底

### Task 4

- `pkg/tools/send_file.go`
- `pkg/tools/send_file_test.go`
- `pkg/agent/instance.go`
- `pkg/agent/loop.go`
- `pkg/agent/loop_test.go`
- `cmd/x-claw/internal/gateway/bootstrap.go`
- `pkg/config/config_tools.go`
- `pkg/config/runtime.go`
- `pkg/config/defaults.go`
- `send_file` 只在存在 `MediaStore` 的 runtime 中注册
- tool 通过 store callback 产出 `media://...` 引用，不直接暴露本地路径

### Task 5

- `pkg/auth/bootstrap.go`
- `pkg/auth/token.go`
- `pkg/providers/factory_auth.go`
- `pkg/auth/oauth_test.go`
- 引入 `BootstrapMethod` 与 `ResolveAuthBootstrapMethod(provider string)`
- OpenAI / Antigravity 走 browser OAuth bootstrap；Anthropic / default 走 paste-token bootstrap
- 保持 token 登录写入的 `AuthCredential.Provider` 不被统一入口偷偷改写

### Task 6

- `pkg/channels/manager_test.go`
- 新增 reply/media/stop-all 契约测试：
  - `TestReplyContract_EditPlaceholderPreservesReplyBinding`
  - `TestReplyContract_MediaSendDoesNotBreakMessageBinding`
  - `TestReplyContract_StopAllClearsReplyCapableWorkers`
- 未改生产代码；本轮确认的是契约收敛与覆盖补强，而非修复已复现的 channels 行为缺陷

### Task 7

- `pkg/providers/model_ref_test.go`
- `pkg/config/migration_test.go`
- `pkg/config/migration.go`
- 新增 alias drift 守护，覆盖 `z.ai` / `z-ai` / `qwen-portal` / `github-copilot`
- 修复 `ConvertProvidersToModelList` 中 migration alias 集合与 canonical protocol 语义漂移

## Verification

### Targeted

- `go test ./pkg/auth ./pkg/providers -run 'OAuth|Auth|Credential|Bootstrap' -count=1`
- `go test ./pkg/channels -run 'ReplyContract|SelectedChannelInitializers|LazyWorkerCreation|SendToChannelAfterStopAllReturns|BuildMediaScope_' -count=1`
- `go test ./pkg/providers ./pkg/config -run 'Factory|Fallback|Default|Provider|CanonicalProtocol|Migration' -count=1`
- `go test ./pkg/agent -run 'UsesReasoningContentWhenDirectAnswerContentEmpty|ReasoningContent|BuildMessages|SystemPrompt|SendFile' -count=1`
- `go test ./pkg/tools -run 'TestSendFileTool_|TestCronToolExecuteJob_' -count=1`
- `go test ./pkg/httpapi -run 'Console|Status' -count=1`
- `go test ./pkg/cron -count=1`

结果：全部通过。

### Baseline

- `go build -p 1 ./...`
- `go vet ./...`
- `go test ./... -run '^$' -count=1`

结果：全部通过。
