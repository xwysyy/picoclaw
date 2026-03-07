# 重构执行进度

## Phase 1a: 运行时正确性修复
- [x] Task 1a.1: 跨 provider fallback 切换实例 (REQ-RT-001) — 2026-03-07 22:06:49 +0800，`main` 已有实现，`TestAgentLoop_FallbackAcrossProviders_UsesFallbackProviderInstance` 通过
- [x] Task 1a.2: resume_last_task 语义收紧 (REQ-RT-002 + REQ-PERF-002) — 2026-03-07 22:06:49 +0800，`main` 已有实现，`TestFindLastUnfinishedRun*` / `TestResumeLastTask*` 通过
- [x] Task 1a.3: Session 快照深拷贝 (REQ-RT-003) — 2026-03-07 22:06:49 +0800，`main` 已有实现，deep-copy 定向测试通过
- [x] Task 1a.4: PlanMode/Estop/ToolPolicy 硬拦截 (REQ-RT-004) — 2026-03-07 22:06:49 +0800，`main` 已有实现，`pkg/tools` 定向测试通过
- [x] Task 1a.5: Tool loop 检测保守策略 (REQ-RT-005) — 2026-03-07 22:06:49 +0800，`main` 已有实现，`TestDetectToolCallLoop*` 定向测试通过

## Phase 1b: Bug 修复与安全加固
- [x] Task 1b.1: Goroutine 泄漏修复 (REQ-BUG-001 + REQ-BUG-006 + REQ-BUG-011) — 2026-03-07 22:06:49 +0800，`SchedulePlaceholder` 改为继承父 `ctx`，补充即时/延迟取消回归测试，placeholder 相关回归通过
- [x] Task 1b.2: HTTP Body/JSON 错误处理 (REQ-BUG-002 + REQ-BUG-003 + REQ-BUG-010) — 2026-03-07 22:06:49 +0800，修复 OAuth `io.ReadAll` 吞错与 Console JSON 编解码吞错，`pkg/auth`/`pkg/httpapi` 定向与完整包测试通过
- [x] Task 1b.3: 会话 GC 机制 (REQ-BUG-005) — 2026-03-07 22:06:49 +0800，新增 Session TTL/LRU 内存驱逐、配置项与 info 日志，`pkg/session`/`pkg/config` 测试通过
- [x] Task 1b.4: DB 连接泄漏 + parseFlexibleInt (REQ-BUG-007 + REQ-BUG-004) — 2026-03-07 22:06:49 +0800，修复 FTS pragma 错误路径关库与整数解析错误语义，定向测试通过
- [x] Task 1b.5: Model Downgrade 竞态 + copyFile 语义 (REQ-BUG-008 + REQ-BUG-009) — 2026-03-07 22:06:49 +0800，`main` 现有 model downgrade 临界区已满足要求；本轮补齐 `copyFile` close 失败语义与回归测试
- [x] Task 1b.6: 消除 stdout 输出 (REQ-SEC-003) — 2026-03-07 22:06:49 +0800，`pkg/tools/shell.go` 改为走 logger，stdout 回归测试通过

## Phase 2: 文件拆分
- [x] Task 2.1: 拆分 pkg/config/config.go (REQ-ARCH-001) — 2026-03-07 22:20:07 +0800，拆分为 6 个域文件，`go build ./pkg/config/...` 与 `go test ./pkg/config/... -count=1` 通过
- [x] Task 2.2: 拆分 pkg/agent/memory.go (REQ-ARCH-002) — 2026-03-07 22:20:07 +0800，拆分为 `memory_vector.go` / `memory_embedder.go` / `memory_fts.go` / `memory_tools.go`，集成构建与定向 `pkg/agent` 测试通过
- [x] Task 2.3: 拆分 pkg/agent/loop.go (REQ-ARCH-003) — 2026-03-07 22:20:07 +0800，拆分为 commands/compaction/token_usage/model_downgrade 子文件，`pkg/agent` 定向测试通过
- [x] Task 2.4: 拆分 pkg/channels/manager.go (REQ-ARCH-004) — 2026-03-07 22:20:07 +0800，拆分为 placeholder/typing/dispatch/interfaces 子文件，`pkg/channels` 定向测试通过
- [x] Task 2.5: 拆分 console.go + toolcall_executor.go (REQ-ARCH-005) — 2026-03-07 22:20:07 +0800，拆分 console/status/sessions/file/stream/notify 与 toolcall policy/hooks/error 子文件，`pkg/httpapi`/`pkg/tools` 定向测试通过

## Phase 3: 代码质量提升
- [x] Task 3.1: 提取魔法数字为命名常量 (REQ-QUAL-001) — 2026-03-07 22:31:40 +0800，提取 placeholder delay 等命名常量并保持行为不变，相关包构建/定向测试通过
- [x] Task 3.2: 消除 Channel 间重复代码 (REQ-QUAL-002) — 2026-03-07 22:31:40 +0800，新增 `register.go` / `media_helpers.go`，复用 channel 注册与 MIME 分类逻辑
- [x] Task 3.3: 统一错误处理规范 (REQ-QUAL-003) — 2026-03-07 22:31:40 +0800，将 Telegram typing、Cron/Agent outbound、shell terminate 这几类错误忽略改为 debug 日志
- [x] Task 3.4: 降低 handleCommand 圈复杂度 (REQ-QUAL-004) — 2026-03-07 22:31:40 +0800，`loop_commands.go` 改为命令分发表 + 小 handler，新增 dispatch 回归测试

## Phase 4: 性能优化
- [x] Task 4.1: 字符串拼接优化 (REQ-PERF-001) — 2026-03-07 22:31:40 +0800，`buildUserMessage` 改用 `strings.Builder`，`WorkingState.FormatForContext` 增加 `Grow`
- [x] Task 4.2: JSON 序列化优化 (REQ-PERF-003) — 2026-03-07 22:31:40 +0800，`pkg/memory/jsonl.go` 将 `MarshalIndent` 改为 `Marshal`
- [x] Task 4.3: HTTP 客户端复用 (REQ-PERF-004) — 2026-03-07 22:31:40 +0800，`pkg/skills/registry.go` 复用共享 HTTP transport/client

## Phase 5: 测试补充
- [x] Task 5.1: memory 模块测试 (REQ-TEST-001) — 2026-03-07 22:40:55 +0800，补齐 CRUD / GetMemoryContext / embedder 切换 / hybrid 搜索关键路径测试，memory 定向测试通过
- [x] Task 5.2: session GC 测试 (REQ-TEST-002) — 2026-03-07 22:40:55 +0800，补齐并发 GC + 读写测试，并修复 `AddFullMessage` 在 GC 后可能绕过内存上限的问题；`pkg/session` 全包测试通过

## 最终验证
- 2026-03-07 22:40:55 +0800，`go build ./...`：通过
- 2026-03-07 22:40:55 +0800，`go vet ./...`：通过
- 2026-03-07 22:40:55 +0800，`go test ./... -count=1 -p 1`：当前环境中被 `SIGKILL(137)` 中断；日志显示已跑到 `pkg/bus`
- 2026-03-07 22:40:55 +0800，关键定向回归：`pkg/agent` / `pkg/auth` / `pkg/channels` / `pkg/config` / `pkg/httpapi` / `pkg/session` / `pkg/skills` / `pkg/tools` 已按阶段分别通过
- 2026-03-07 22:40:55 +0800，`go test -race ./pkg/session -count=1 -p 1`：通过
- 2026-03-07 22:40:55 +0800，`go test -race ./pkg/channels|./pkg/httpapi|./pkg/agent -count=1 -p 1`：当前环境中被 `SIGKILL(137)` 中断
- 2026-03-07 23:24:58 +0800，新增 `scripts/test-batches.sh`，用于在当前受限环境下分批执行 `go build` / `go vet` / compile-only / per-package 测试，并对 `pkg/agent` 做顶层测试分批
- 2026-03-07 23:24:58 +0800，文档入口已更新：`README.md` / `README.en.md` / `UNIT_TESTING.md` / `docs/architecture.md` 均补充分批测试脚本说明
- 2026-03-07 23:24:58 +0800，新鲜验证：`bash -n scripts/test-batches.sh` 通过；`X_CLAW_TEST_PKGS='github.com/xwysyy/X-Claw/pkg/config github.com/xwysyy/X-Claw/pkg/httpapi' scripts/test-batches.sh --skip-build --skip-vet` 通过
- 2026-03-07 23:24:58 +0800，新鲜验证：使用仓库内 `.cache` 目录重跑 `go build ./...` 与 `go vet ./...`，均通过
