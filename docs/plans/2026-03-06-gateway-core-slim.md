# Gateway Core Slim Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 将 `x-claw` 重构成以 Gateway 为核心、仅保留 Feishu / Telegram 主线和调试 CLI 的轻量单体工程，并显著减少文件数与结构复杂度。

**Architecture:** 先在当前仓库内建立新的 `internal/app`、`internal/gateway`、`internal/runtime`、`internal/channel/{feishu,telegram}` 骨架，逐步把主链迁入，再删除旧的命令面、多渠道面和治理型安全/策略层。测试只围绕 Feishu / Telegram 主链、核心工具和最小 CLI debug 能力展开。

**Tech Stack:** Go 1.25、Cobra CLI、现有 Gateway/Agent/Tool/Channel 实现、Go test

---

### Task 1: 固化当前主线行为基线

**Files:**
- Create: `cmd/x-claw/main_slim_test.go`
- Modify: `cmd/x-claw/main.go`
- Test: `cmd/x-claw/main_slim_test.go`

**Step 1: 写最小命令面基线测试**

在 `cmd/x-claw/main_slim_test.go` 中新增测试，验证根命令未来只保留 `gateway`、`agent`、`version` 三类入口，删除的命令不会继续出现在帮助输出中。

**Step 2: 运行测试确认当前会失败**

Run: `go test ./cmd/x-claw -run TestSlimCommandSurface -count=1`

Expected: FAIL，原因是当前命令仍然过多。

**Step 3: 在根命令实现中准备瘦身入口**

在 `cmd/x-claw/main.go` 中先收敛命令注册逻辑，允许后续分批删除命令目录时只调整一个注册点。

**Step 4: 重新运行测试**

Run: `go test ./cmd/x-claw -run TestSlimCommandSurface -count=1`

Expected: PASS。

**Step 5: 提交暂存**

Run: `git add cmd/x-claw/main.go cmd/x-claw/main_slim_test.go`

### Task 2: 建立新的 Gateway Core 应用骨架

**Files:**
- Create: `internal/app/app.go`
- Create: `internal/gateway/server.go`
- Create: `internal/gateway/routes.go`
- Create: `internal/gateway/handlers_health.go`
- Modify: `cmd/x-claw/internal/gateway/command.go`
- Test: `internal/gateway/server_test.go`

**Step 1: 写新的 Gateway 启动测试**

在 `internal/gateway/server_test.go` 中新增测试，验证 `NewServer` 可以注册 `/health` 并返回 `200`。

**Step 2: 运行测试确认失败**

Run: `go test ./internal/gateway -run TestServerHealthRoute -count=1`

Expected: FAIL，原因是新骨架尚不存在。

**Step 3: 写最小实现**

新增 `internal/app/app.go` 与 `internal/gateway/*.go`，让 `gateway` 命令通过新的应用入口启动最小 HTTP 服务。

**Step 4: 重新运行测试**

Run: `go test ./internal/gateway -run TestServerHealthRoute -count=1`

Expected: PASS。

**Step 5: 提交暂存**

Run: `git add internal/app/app.go internal/gateway/server.go internal/gateway/routes.go internal/gateway/handlers_health.go internal/gateway/server_test.go cmd/x-claw/internal/gateway/command.go`

### Task 3: 将 `pkg/httpapi` 主链接口并入新 Gateway

**Files:**
- Create: `internal/gateway/handlers_notify.go`
- Create: `internal/gateway/handlers_console.go`
- Create: `internal/gateway/handlers_resume.go`
- Modify: `cmd/x-claw/internal/gateway/httpapi.go`
- Modify: `internal/gateway/routes.go`
- Delete: `pkg/httpapi/notify.go`
- Delete: `pkg/httpapi/resume_last_task.go`
- Delete: `pkg/httpapi/console*.go`
- Test: `internal/gateway/handlers_test.go`

**Step 1: 先写 handler 回归测试**

在 `internal/gateway/handlers_test.go` 中写针对 `/api/notify`、`/api/resume_last_task`、`/console/` 的最小行为测试。

**Step 2: 跑测试确认新实现缺失**

Run: `go test ./internal/gateway -run 'TestNotifyHandler|TestResumeLastTaskHandler|TestConsoleHandler' -count=1`

Expected: FAIL。

**Step 3: 将主链接口迁入 `internal/gateway`**

复制并收敛现有逻辑，删除不再需要的中间 options/薄封装层，只保留主服务真正使用的字段。

**Step 4: 重新运行测试**

Run: `go test ./internal/gateway -run 'TestNotifyHandler|TestResumeLastTaskHandler|TestConsoleHandler' -count=1`

Expected: PASS。

**Step 5: 提交暂存**

Run: `git add internal/gateway cmd/x-claw/internal/gateway/httpapi.go && git add -u pkg/httpapi`

### Task 4: 收敛渠道为 Feishu / Telegram 双通道

**Files:**
- Create: `internal/channel/feishu/adapter.go`
- Create: `internal/channel/telegram/adapter.go`
- Create: `internal/channel/manager.go`
- Modify: `pkg/channels/manager.go`
- Modify: `pkg/channels/manager_init.go`
- Delete: `pkg/channels/{discord,dingtalk,line,onebot,pico,qq,slack,wecom,whatsapp,whatsapp_native}/*`
- Test: `internal/channel/manager_test.go`

**Step 1: 写双通道选择测试**

在 `internal/channel/manager_test.go` 中验证：只有 Feishu / Telegram 会被启用，其它历史渠道不会再被注册。

**Step 2: 跑测试确认失败**

Run: `go test ./internal/channel -run TestSelectedChannels -count=1`

Expected: FAIL。

**Step 3: 实现新 manager 并迁移两条渠道主线**

先让新 `internal/channel/manager.go` 驱动 Feishu / Telegram，再把旧 `pkg/channels` 中不需要的注册逻辑删掉或变成薄桥接层。

**Step 4: 重新运行测试**

Run: `go test ./internal/channel -run TestSelectedChannels -count=1`

Expected: PASS。

**Step 5: 提交暂存**

Run: `git add internal/channel pkg/channels && git add -u pkg/channels`

### Task 5: 把主链运行时收敛到 `internal/runtime`

**Files:**
- Create: `internal/runtime/loop.go`
- Create: `internal/runtime/context.go`
- Create: `internal/runtime/session_queue.go`
- Create: `internal/store/session_store.go`
- Modify: `pkg/agent/*.go`
- Modify: `pkg/bus/*.go`
- Modify: `pkg/routing/*.go`
- Test: `internal/runtime/loop_test.go`
- Test: `internal/runtime/session_queue_test.go`

**Step 1: 写 session 串行测试**

新增测试验证：同一 session 串行执行，不同 session 可以并发。

**Step 2: 写最小 loop 测试**

新增测试验证：一条标准化消息输入后，可调用 runtime 并拿到输出消息。

**Step 3: 跑测试确认失败**

Run: `go test ./internal/runtime -run 'TestSessionQueueSerializesPerSession|TestLoopHandlesInboundMessage' -count=1`

Expected: FAIL。

**Step 4: 迁移最小运行时实现**

从 `pkg/agent`、`pkg/bus`、`pkg/routing` 抽出主链必需逻辑，去掉只服务历史平台能力的状态与分支。

**Step 5: 重新运行测试**

Run: `go test ./internal/runtime -run 'TestSessionQueueSerializesPerSession|TestLoopHandlesInboundMessage' -count=1`

Expected: PASS。

**Step 6: 提交暂存**

Run: `git add internal/runtime internal/store pkg/agent pkg/bus pkg/routing`

### Task 6: 收缩工具系统并整合 Feishu / Telegram 工具

**Files:**
- Create: `internal/tools/registry.go`
- Create: `internal/tools/core.go`
- Create: `internal/tools/feishu.go`
- Create: `internal/tools/telegram.go`
- Modify: `pkg/tools/*.go`
- Delete: `pkg/tools/tool_policy.go`
- Delete: `pkg/tools/tool_confirm.go`
- Delete: `pkg/tools/plan_mode_gate.go`
- Delete: `pkg/tools/tool_policy_store.go`
- Delete: `pkg/tools/estop.go`
- Test: `internal/tools/registry_test.go`

**Step 1: 先写注册白名单测试**

验证新的工具注册表默认只暴露核心工具与 Feishu / Telegram 相关工具。

**Step 2: 写渠道工具整合测试**

验证 Feishu / Telegram 工具仍能通过统一入口被调用，参数和结果格式符合预期。

**Step 3: 跑测试确认失败**

Run: `go test ./internal/tools -run 'TestDefaultRegistry|TestChannelTools' -count=1`

Expected: FAIL。

**Step 4: 实现新工具注册表并删除治理层**

迁移保留的工具到 `internal/tools`，删除策略/确认/estop 等治理型逻辑，统一 Feishu / Telegram 工具的注册与返回结构。

**Step 5: 重新运行测试**

Run: `go test ./internal/tools -run 'TestDefaultRegistry|TestChannelTools' -count=1`

Expected: PASS。

**Step 6: 提交暂存**

Run: `git add internal/tools pkg/tools && git add -u pkg/tools`

### Task 7: 删减 CLI 命令面与旧辅助目录

**Files:**
- Modify: `cmd/x-claw/main.go`
- Delete: `cmd/x-claw/internal/{auditlog,auth,config,cron,doctor,estop,export,migrate,onboard,security,skills,status}`
- Modify: `cmd/x-claw/internal/agent/command.go`
- Modify: `cmd/x-claw/internal/version/command.go`
- Test: `cmd/x-claw/main_slim_test.go`

**Step 1: 删除非主线命令注册**

更新根命令只暴露 `gateway`、`agent`、`version`。

**Step 2: 跑命令面测试**

Run: `go test ./cmd/x-claw -run TestSlimCommandSurface -count=1`

Expected: PASS。

**Step 3: 删除旧命令目录并修正构建引用**

清理失效导入、帮助文本、测试和文档引用。

**Step 4: 再次运行包级测试**

Run: `go test ./cmd/x-claw/... -count=1`

Expected: PASS。

**Step 5: 提交暂存**

Run: `git add cmd/x-claw && git add -u cmd/x-claw`

### Task 8: 收缩配置模型与文档

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/defaults.go`
- Modify: `pkg/config/*.go`
- Modify: `README.md`
- Modify: `docker/docker-compose.yml`
- Test: `internal/config/config_test.go`

**Step 1: 写最小配置加载测试**

验证新配置只需要 Gateway、Feishu、Telegram、Agent defaults、Tools core 几个核心区块即可启动。

**Step 2: 跑测试确认失败**

Run: `go test ./internal/config -run TestMinimalGatewayCoreConfig -count=1`

Expected: FAIL。

**Step 3: 实现最小配置模型并更新文档**

删除迁移/secretref/复杂治理字段或将其降级为内部默认值，更新 README 与 Docker 示例为新的主线部署方式。

**Step 4: 重新运行测试**

Run: `go test ./internal/config -run TestMinimalGatewayCoreConfig -count=1`

Expected: PASS。

**Step 5: 提交暂存**

Run: `git add internal/config pkg/config README.md docker/docker-compose.yml`

### Task 9: 删除旧实现、跑针对性验证并清理仓库

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Delete: 已被新主链替代的 `pkg/httpapi`、`pkg/health`、`pkg/bus`、`pkg/routing`、低频渠道和失效测试文件
- Test: `internal/gateway/...`
- Test: `internal/channel/...`
- Test: `internal/runtime/...`
- Test: `internal/tools/...`
- Test: `cmd/x-claw/...`

**Step 1: 删除不再被引用的旧实现与测试**

先用 `rg` 与 `go test` 找出未引用包，逐步删除并修复 import。

**Step 2: 跑定向测试矩阵**

Run: `go test ./internal/gateway/... ./internal/channel/... ./internal/runtime/... ./internal/tools/... ./cmd/x-claw/... -count=1`

Expected: PASS。

**Step 3: 跑一次仓库级构建/测试**

Run: `go test ./... -count=1`

Expected: PASS，或只剩用户明确接受的待删旧包失败项；若有依赖下载，使用 `source ~/.zshrc && proxy_on && go test ./... -count=1`。

**Step 4: 更新最终删减说明**

在 `README.md` 或独立变更说明中列出：保留的命令、保留的渠道、删除的治理能力、保留的工具类型。

**Step 5: 提交暂存**

Run: `git add .`

### Task 10: 发出完成通知

**Files:**
- Modify: 无

**Step 1: 在 Gateway 运行后发送完成通知**

Run: `curl -sS -X POST http://127.0.0.1:18790/api/notify -H 'Content-Type: application/json' -d '{"content":"✅ X-Claw: Gateway Core slim refactor ready for review."}'`

Expected: 返回成功 JSON，消息发送到 `last_active`。

**Step 2: 若已配置 API key，则加上 Bearer 头后重试**

Run: `curl -sS -X POST http://127.0.0.1:18790/api/notify -H 'Authorization: Bearer <api_key>' -H 'Content-Type: application/json' -d '{"content":"✅ X-Claw: Gateway Core slim refactor ready for review."}'`

Expected: 返回成功 JSON。

**Step 3: 记录最终验证结果**

在最终交付说明中写明通过的测试命令与仍待人工验证的 Feishu / Telegram 实机链路。
