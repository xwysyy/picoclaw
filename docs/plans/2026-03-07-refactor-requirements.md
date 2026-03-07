# X-Claw 重构优化需求文档

> **文档编号**: REQ-2026-0307
> **版本**: v1.1
> **日期**: 2026-03-07
> **关联文档**: [技术实现文档](./2026-03-07-refactor-implementation.md) | [审查设计文档](./2026-03-07-comprehensive-refactor-design.md) | [运行时风险加固设计](./2026-03-07-runtime-risk-hardening-design.md) | [运行时风险加固实现](./2026-03-07-runtime-risk-hardening.md) | [Gateway 入口重连](./2026-03-07-gateway-entry-rewire.md)
>
> **v1.1 变更说明**: 整合已有的 runtime-risk-hardening 设计文档中识别的 5 个运行时风险（跨 provider fallback、session 浅拷贝、resume 语义、策略硬拦截、loop 检测误伤），新增 REQ-RT-001 ~ REQ-RT-005。

---

## 1. 项目背景

X-Claw 是一个使用 Go 编写的 Gateway-first AI 助手服务，当前代码规模为 209 个 Go 文件、约 73,000 行代码。经过全面的代码审查（覆盖架构、代码质量、Bug/安全、性能/依赖 4 个维度），并结合已有的 [运行时风险加固设计](./2026-03-07-runtime-risk-hardening-design.md) 中识别的 5 类中高优先级运行时风险，统一整理为以下需求清单。

### 1.1 核心约束

- **功能不变**: 所有重构必须保持现有功能完全不变
- **渐进式**: 按 Phase 独立推进，每个 Phase 可独立验证
- **向后兼容**: 配置文件格式不做 breaking change
- **测试保障**: 每个变更都必须通过 `go build ./...` + `go test ./...` + `go vet ./...`

---

## 2. 需求总览

| 需求类别 | 需求数量 | 优先级分布 |
|---------|---------|-----------|
| 运行时正确性 | 5 | P0: 3, P1: 2 |
| Bug 修复 | 11 | P0: 5, P1: 6 |
| 安全加固 | 3 | P0: 1, P1: 2 |
| 架构重构 | 6 | P1: 2, P2: 4 |
| 代码质量 | 8 | P1: 4, P2: 4 |
| 性能优化 | 7 | P1: 3, P2: 4 |
| 测试补充 | 4 | P1: 2, P2: 2 |

---

## 3. 运行时正确性需求

> **来源**: 已有设计文档 [runtime-risk-hardening-design.md](./2026-03-07-runtime-risk-hardening-design.md)，状态: Approved approach B。
> 以下 5 个需求已有详细的设计方案和 TDD 实现计划（见 [runtime-risk-hardening.md](./2026-03-07-runtime-risk-hardening.md)），本节将其纳入统一的需求追踪体系。

### REQ-RT-001: 跨 provider fallback 必须切换 provider 实例

- **优先级**: P0
- **影响**: 当 fallback 候选跨 provider 族（如 `openai/... → anthropic/...`）时，当前实现只切模型名而复用原 provider 实例，导致实际调用仍发到错误的 API 端点
- **位置**: `pkg/agent/loop.go`, `pkg/agent/instance.go`, `pkg/providers/fallback.go`
- **设计方案**: 在 agent runtime 内将 fallback candidate 解析为"可执行候选"，执行时为每个候选选择对应 provider 实例；同 provider 复用，跨 provider 必须切换
- **验收标准**:
  1. 新增回归测试证明跨 provider fallback 使用目标 provider 实例（而非源 provider）
  2. `usedModel` 与实际使用的 provider 一致
  3. `go test ./pkg/agent -run 'TestAgentLoop_.*Fallback.*Provider' -count=1` 通过
- **详细实现**: 见 [runtime-risk-hardening.md Task 1](./2026-03-07-runtime-risk-hardening.md)

### REQ-RT-002: resume_last_task 语义收紧

- **优先级**: P0
- **影响**: `run.error` 可能被当成未完成 run 反复 resume（鉴权错误、配置错误等不该自动恢复的 run）；扫描范围仅限默认 agent workspace，多 agent 部署时遗漏其他 workspace 的 unfinished run
- **位置**: `pkg/agent/run_pipeline_impl.go`, `pkg/agent/instance.go`
- **设计方案**:
  - 明确 run 终态: `run.end`（正常完成）和 `run.error`（默认不可自动恢复）均为终态；仅无终态事件的 run 才视为可恢复候选
  - 扫描范围从默认 workspace 扩展为 agent registry 中所有可见 workspace
- **验收标准**:
  1. `run.error` 不进入 resume 候选
  2. 多 workspace 扫描能发现非默认 agent 的 unfinished run
  3. `go test ./pkg/agent -run 'TestFindLastUnfinishedRun|TestResumeLastTask' -count=1` 通过
- **详细实现**: 见 [runtime-risk-hardening.md Task 2](./2026-03-07-runtime-risk-hardening.md)
- **与 REQ-PERF-002 的关系**: REQ-PERF-002 关注扫描性能（反向扫描），本需求关注语义正确性（终态判定 + 多 workspace）；两者可合并实现

### REQ-RT-003: Session 快照必须深拷贝

- **优先级**: P0
- **影响**: 当前 session 对外返回的 message 快照只做了顶层 slice 复制，嵌套字段（`Media`, `ToolCalls`, `Function`）共享底层对象；外部修改会污染 SessionManager 内存态
- **位置**: `pkg/session/manager.go`, `pkg/session/manager_mutations.go`
- **设计方案**: 新增统一 clone helper，覆盖 `providers.Message` 及嵌套引用字段；所有对外快照 API（`GetHistory`, `SetHistory`, `GetSessionSnapshot`, `ListSessionSnapshots`）统一走 clone
- **验收标准**:
  1. 新增测试: 修改返回的 `Media`/`ToolCalls` 不影响原 session
  2. `go test ./pkg/session -run 'Test.*DeepCopy' -count=1` 通过
- **详细实现**: 见 [runtime-risk-hardening.md Task 3](./2026-03-07-runtime-risk-hardening.md)

### REQ-RT-004: PlanMode / Estop / ToolPolicy 执行层硬拦截

- **优先级**: P1
- **影响**: 这些限制当前更像 prompt-layer advisory，模型仍然可以发起工具调用并被执行器实际执行
- **位置**: `pkg/tools/toolcall_executor.go`
- **设计方案**:
  - 在 `ExecuteToolCalls` 统一 chokepoint 前置判断
  - 命中时直接返回结构化 `ToolResult` 错误，不执行工具
  - 执行优先级: `Estop`（全局硬刹车）→ `PlanMode`（运行模式）→ `ToolPolicy`（细粒度策略）
  - 错误输出对模型可恢复（明确拒绝原因、触发策略、建议下一步）
- **验收标准**:
  1. `PlanMode` 命中时工具被拒绝而非执行
  2. `Estop` 命中时工具被拒绝
  3. `ToolPolicy` 命中时工具被拒绝
  4. `go test ./pkg/tools -run 'TestExecuteToolCalls_(PlanMode|Estop|ToolPolicy)' -count=1` 通过
- **详细实现**: 见 [runtime-risk-hardening.md Task 4](./2026-03-07-runtime-risk-hardening.md)

### REQ-RT-005: Tool loop 检测改为保守策略

- **优先级**: P1
- **影响**: 当前按全历史累计重复次数判断 loop，容易误伤轮询/重复检查类合法行为（如 `web_search` 连续查不同关键词）
- **位置**: `pkg/agent/loop.go`
- **设计方案**:
  - 改为"最近窗口 + 连续重复 + 无明显进展"导向的保守判定
  - 只看最近 N 次工具签名，只在连续重复达到阈值时触发
  - 不再按整个 run 累计重复次数判 loop
  - 保持返回语义不变（向模型回灌 loop-detected tool error）
- **验收标准**:
  1. 连续重复 N 次相同工具签名触发 loop 检测
  2. 非连续重复/有变化的重复不触发
  3. `go test ./pkg/agent -run 'TestDetectToolCallLoop' -count=1` 通过
- **详细实现**: 见 [runtime-risk-hardening.md Task 5](./2026-03-07-runtime-risk-hardening.md)

---

## 4. Bug 修复需求

### REQ-BUG-001: 修复 Placeholder 调度 Goroutine 泄漏

- **优先级**: P0
- **影响**: Gateway 长期运行时 goroutine 累积，最终导致 OOM
- **位置**: `pkg/channels/manager.go:210-251` (`SchedulePlaceholder`)
- **现象**: 当 `send(sendCtx)` 阻塞时，goroutine 无超时退出路径；在高频消息场景下（每条入站消息都会触发），goroutine 逐渐累积
- **验收标准**:
  1. `send` 操作有明确的超时保护（建议 30s）
  2. `scheduleCtx` 从调用者 `ctx` 继承（而非 `context.Background()`）
  3. goroutine 退出路径覆盖所有分支
  4. 现有测试 `TestSchedulePlaceholder_CancelBeforeDelay`、`TestSchedulePlaceholder_RescheduleReplacesExisting` 继续通过
  5. 新增测试覆盖 send 超时场景

### REQ-BUG-002: 修复 HTTP Body 读取错误被忽略

- **优先级**: P0
- **影响**: OAuth 流程中网络异常时静默失败，错误信息丢失
- **位置**: `pkg/auth/oauth.go` 5 处 `io.ReadAll` 调用（行 215, 303, 363, 404, 497）
- **现象**: `body, _ := io.ReadAll(resp.Body)` 忽略了 ReadAll 的 error
- **验收标准**:
  1. 所有 `io.ReadAll` 调用检查 error
  2. 网络错误时返回有意义的错误消息
  3. 现有测试 `pkg/auth/oauth_test.go` 继续通过

### REQ-BUG-003: 修复 Console API JSON 解码错误被忽略

- **优先级**: P0
- **影响**: 客户端发送畸形 JSON 时无错误反馈
- **位置**: `pkg/httpapi/console.go` 多处（行 95, 106, 123, 135, 169, 861, 1903 等 20+ 处）
- **现象**: `_ = json.NewDecoder(r.Body).Decode(...)` 和 `_ = json.NewEncoder(w).Encode(...)` 的错误被显式忽略
- **验收标准**:
  1. 请求体解码失败返回 HTTP 400
  2. 响应编码失败至少记录 error 级别日志
  3. 关键路径（`/api/resume_last_task` 行 1903）必须返回正确的错误码
  4. 现有测试 `pkg/httpapi/console_test.go` 继续通过

### REQ-BUG-004: 修复 parseFlexibleInt 错误处理

- **优先级**: P1
- **影响**: 非数字字符串输入时返回含义不明的错误
- **位置**: `pkg/auth/oauth.go:266-285` (`parseFlexibleInt`)
- **现象**: `strconv.Atoi(intervalStr)` 的错误直接返回，与前面空字符串处理逻辑不一致
- **验收标准**:
  1. 所有错误路径返回清晰的错误消息
  2. 添加单元测试覆盖各种输入（null, 空字符串, 数字, 非数字字符串, 数字字符串）

### REQ-BUG-005: 添加会话垃圾回收机制

- **优先级**: P0
- **影响**: Gateway 长期运行时会话 map 无限增长导致 OOM
- **位置**: `pkg/session/manager.go:24` (`sessions map[string]*Session`)
- **现象**: `SessionManager.sessions` 只有写入（`ensureSessionLocked` 行 48, 69），**完全没有 delete() 操作**，无 TTL/LRU/MaxSize
- **验收标准**:
  1. 支持配置 `session.max_sessions`（默认 1000）和 `session.ttl_hours`（默认 168，即 7 天）
  2. 超出限制时按 LRU（最后活跃时间）驱逐
  3. 被驱逐的会话元数据仍保留在磁盘（仅从内存中移除）
  4. 驱逐时记录 info 日志
  5. 现有测试继续通过，新增 GC 相关测试

### REQ-BUG-006: 修复 Context 未从父级继承

- **优先级**: P1
- **影响**: `SchedulePlaceholder` 中调用者传递的 ctx 超时信息被丢弃
- **位置**: `pkg/channels/manager.go:190, 202`
- **现象**: `context.WithTimeout(context.Background(), 10*time.Second)` 和 `context.WithCancel(context.Background())` 未继承调用者 ctx
- **验收标准**:
  1. 所有 `context.Background()` 替换为基于调用者 `ctx` 的派生 context
  2. 现有占位符测试继续通过

### REQ-BUG-007: 修复 DB 连接泄漏

- **优先级**: P1
- **影响**: `openDBLocked` 中 sql.Open 成功后若后续操作失败，连接未释放
- **位置**: `pkg/agent/memory.go:2761-2789` (`openDBLocked`)
- **现象**: `sql.Open` 成功但未分配给 `fs.db` 时的错误路径缺少 `db.Close()`
- **验收标准**:
  1. 错误路径中调用 `db.Close()`
  2. 为 `memoryFTSStore` 添加 `Close()` 方法

### REQ-BUG-008: 修复 Model Downgrade 状态竞态

- **优先级**: P1
- **影响**: 多 goroutine 在 mutex 间隙中可能读取过时的降级状态
- **位置**: `pkg/agent/loop.go:61-66, 372-375`
- **现象**: `modelAutoDowngradeMap` 的读写操作分散在多个临界区
- **验收标准**:
  1. 相关读写操作合并到单个临界区
  2. 添加 `-race` 测试用例

### REQ-BUG-009: 修复 copyFile 错误语义

- **优先级**: P1
- **影响**: `io.Copy` 成功但 `Close()` 失败时，返回的字节数 n 可能误导调用方
- **位置**: `pkg/agent/run_pipeline_impl.go:1342-1352`
- **验收标准**:
  1. Close 失败时返回 `(0, closeErr)` 而非 `(n, closeErr)`
  2. 或使用 `errors.Join` 合并两个错误

### REQ-BUG-010: 消除 JSON 编码错误忽略

- **优先级**: P1
- **影响**: JSON 编码失败时客户端收不到任何响应
- **位置**: `pkg/httpapi/console.go` 20+ 处 `_ = json.NewEncoder(w).Encode(...)`
- **验收标准**:
  1. 提取 `writeJSON(w, status, data)` 辅助函数统一处理
  2. 编码失败时记录 error 日志并返回 500

### REQ-BUG-011: 修复 Timer 清理边界条件

- **优先级**: P1
- **影响**: `SchedulePlaceholder` 中 timer 与 goroutine 的清理时序竞态
- **位置**: `pkg/channels/manager.go:211-225`
- **验收标准**:
  1. defer 中的 token 匹配检查和 map 清理逻辑无竞态
  2. 现有 Janitor 测试继续通过

---

## 5. 安全加固需求

### REQ-SEC-001: Shell 命令过滤增强

- **优先级**: P1
- **影响**: 正则黑名单可能被嵌套/转义绕过
- **位置**: `pkg/tools/shell.go:53-100` (`defaultDenyPatterns`)
- **现状**: 46 个 deny pattern + 可选 allow pattern，执行顺序: Custom Allow > Deny > General Allow > Workspace
- **验收标准**:
  1. 评估并文档化已知绕过场景
  2. 对高危操作增加二次确认机制（与 tool policy confirm 集成）
  3. 不改变现有执行顺序和兼容性

### REQ-SEC-002: 日志脱敏

- **优先级**: P1
- **影响**: API Key/Token 可能通过 debug 日志泄漏
- **位置**: `pkg/providers/anthropic/provider.go:44-45` 及其他 provider
- **验收标准**:
  1. logger 包增加敏感字段过滤（`api_key`, `token`, `secret`, `pat` 等）
  2. provider 初始化日志不打印完整 token

### REQ-SEC-003: 消除直接 stdout 输出

- **优先级**: P0
- **影响**: `fmt.Printf`/`fmt.Println` 绕过日志系统，不受日志级别控制
- **位置**: `pkg/tools/shell.go:148, 159`
- **验收标准**:
  1. 替换为 `logger.InfoCF` / `logger.WarnCF`
  2. 添加 `pkg/logger` 包导入

---

## 6. 架构重构需求

### REQ-ARCH-001: 拆分超大配置文件

- **优先级**: P1
- **影响**: `config.go` 1200 行、48+ struct 在单文件中，维护困难
- **位置**: `pkg/config/config.go`
- **验收标准**:
  1. 按域拆分为 6 个文件: `config.go`(顶级+通用), `agents.go`, `channels.go`, `providers.go`, `tools.go`, `gateway.go`
  2. 不改变包名、导出 API 和 JSON tag
  3. `go build ./...` 和 `go test ./pkg/config/...` 通过

### REQ-ARCH-002: 拆分 memory.go 超大文件

- **优先级**: P1
- **影响**: 2900 行单文件包含 5 类不同职责，阅读和测试极其困难
- **位置**: `pkg/agent/memory.go`
- **当前职责**:
  - `MemoryStore` (核心 struct, 行 43-49, 方法到 ~384)
  - `memoryVectorStore` (向量索引)
  - `memoryFTSStore` (全文搜索, struct 行 2614-2623)
  - 嵌入器 (hashed/openai_compat)
  - `MemorySearchTool`/`MemoryGetTool`
- **验收标准**:
  1. 拆分为 5 个文件，每个 400-600 行
  2. 不改变任何导出 API
  3. 所有现有测试通过

### REQ-ARCH-003: 拆分 loop.go 超大文件

- **优先级**: P2
- **影响**: 4194 行单文件，`handleCommand` 150+ 行包含所有命令分支
- **位置**: `pkg/agent/loop.go`
- **当前核心结构**: `AgentLoop` struct 20 个字段（行 45-66），核心方法 `Run()`(554), `handleCommand()`(935-1480), `maybeSummarize()`(1615), `compactWithSafeguard()`(2082)
- **验收标准**:
  1. 拆分为 5 个文件: `loop.go`(核心), `loop_commands.go`, `loop_compaction.go`, `loop_token_usage.go`, `loop_model_downgrade.go`
  2. `handleCommand` 重构为命令分发 + 独立处理函数
  3. 不改变任何导出 API

### REQ-ARCH-004: 拆分 manager.go 超大文件

- **优先级**: P2
- **影响**: 1762 行，9 个接口定义与实现逻辑混杂
- **位置**: `pkg/channels/manager.go`
- **当前核心结构**: `Manager` struct 15 个字段（行 96-113），10 个接口定义（行 1026-1496）
- **验收标准**:
  1. 拆分为: `manager.go`(核心), `manager_placeholder.go`, `manager_typing.go`, `manager_dispatch.go`, `interfaces.go`
  2. 不改变任何导出 API

### REQ-ARCH-005: 拆分 console.go 和 toolcall_executor.go

- **优先级**: P2
- **影响**: console.go ~1500 行, toolcall_executor.go ~1000 行
- **位置**: `pkg/httpapi/console.go`, `pkg/tools/toolcall_executor.go`
- **验收标准**:
  1. console.go 按 API 域拆分（sessions, traces, stream, file）
  2. toolcall_executor.go 按关注点拆分（core, policy, hooks, error_template）
  3. 不改变任何导出 API

### REQ-ARCH-006: pkg/ 到 internal/ 的迁移规划

- **优先级**: P2
- **影响**: 11 个包不应作为公开 API 暴露
- **范围**: `channels`, `providers`, `session`, `memory`, `httpapi`, `mcp`, `bus`, `state`, `media`, `auditlog`, `identity`
- **注意**: 当前 `pkg/agent/run_pipeline_impl.go:4` 存在 `pkg/ → internal/core/` 的反向依赖需一并解决
- **验收标准**:
  1. 制定详细迁移计划（分批，每批 2-3 包）
  2. 每批迁移后全量测试通过
  3. 保留 `pkg/agent/`, `pkg/tools/`, `pkg/config/`, `pkg/routing/` 为公开 API

---

## 7. 代码质量需求

### REQ-QUAL-001: 提取魔法数字为命名常量

- **优先级**: P1
- **位置**: `pkg/channels/manager.go`(行 37,42,43,80-85,97), `pkg/agent/loop.go`(行 1620) 等
- **验收标准**:
  1. 所有业务相关的数值字面量提取为命名常量
  2. 速率限制值集中到 `rate_config.go`
  3. Discord 速率从 1 调整为 5（当前过于保守）

### REQ-QUAL-002: 消除 Channel 间重复代码

- **优先级**: P1
- **影响**: Feishu/Telegram 的 init.go、媒体处理、Markdown 转换存在重复
- **验收标准**:
  1. 提取通用注册辅助函数
  2. 提取通用媒体类型分发函数
  3. 减少重复代码行数 ≥30%

### REQ-QUAL-003: 统一错误处理规范

- **优先级**: P1
- **验收标准**:
  1. 所有 error 使用 `fmt.Errorf("...: %w", err)` wrapping
  2. best-effort 操作使用 `logger.DebugCF` 记录错误
  3. 消除所有不当的 `_ = <关键操作>` 模式

### REQ-QUAL-004: 降低函数圈复杂度

- **优先级**: P1
- **位置**: `handleCommand()` ~30+, `ExecuteToolCalls()` ~25+
- **验收标准**:
  1. 所有函数圈复杂度 ≤ 20（与 `.golangci.yaml` 一致）
  2. 使用命令分发 + 独立处理函数降低复杂度

### REQ-QUAL-005: 添加 Goroutine 生命周期管理

- **优先级**: P2
- **位置**: `AgentLoop`, `ChannelManager`
- **验收标准**:
  1. 添加 `sync.WaitGroup` 追踪后台 goroutine
  2. `Stop()`/`Shutdown()` 中等待所有 goroutine 退出
  3. 所有后台 goroutine 有超时或取消路径

### REQ-QUAL-006: 规范化 sync.Map 使用

- **优先级**: P2
- **位置**: `pkg/channels/manager.go` 4 个 sync.Map 字段（行 107-110）
- **验收标准**:
  1. 评估是否可替换为 typed map + RWMutex（类型安全更好）
  2. 文档化每个 sync.Map 的键值类型约定

### REQ-QUAL-007: 消除嵌套过深的函数

- **优先级**: P2
- **位置**: `pkg/channels/telegram/telegram.go:418-582` (5 层嵌套)
- **验收标准**: 所有函数嵌套深度 ≤ 4 层，使用 early return 或辅助函数

### REQ-QUAL-008: Options struct 优化

- **优先级**: P2
- **位置**: `ToolCallExecutionOptions` 15+ 字段（行 33-96）
- **验收标准**:
  1. 相关字段分组为子 struct（如 `PlanModeOptions`, `PolicyOptions`）
  2. 添加 `Validate()` 方法

---

## 8. 性能优化需求

### REQ-PERF-001: 字符串拼接优化

- **优先级**: P1
- **位置**: `pkg/agent/run_pipeline_impl.go:277-280` 及其他高频路径
- **验收标准**:
  1. 高频路径的 `+=` 字符串拼接替换为 `strings.Builder`
  2. Builder 使用 `Grow()` 预分配

### REQ-PERF-002: 会话恢复扫描优化

- **优先级**: P1
- **位置**: `pkg/agent/run_pipeline_impl.go:935-1074` (`findLastUnfinishedRun`)
- **现状**: 正向遍历所有运行目录，O(n) 复杂度
- **验收标准**:
  1. 改为反向扫描（最新优先）
  2. 找到第一个未完成 run 即返回
  3. 大量 run 目录（>100）场景下性能提升 ≥50%

### REQ-PERF-003: JSON 序列化优化

- **优先级**: P1
- **位置**: `pkg/memory/jsonl.go:120`
- **验收标准**:
  1. `json.MarshalIndent` 替换为 `json.Marshal`（元数据文件）
  2. 磁盘 IO 减少 ≥5%

### REQ-PERF-004: HTTP 客户端复用

- **优先级**: P2
- **位置**: `pkg/skills/registry.go`, `pkg/mcp/manager.go`
- **验收标准**:
  1. 提供包级或注入的 HTTP client 单例
  2. 配置合理的连接池参数

### REQ-PERF-005: 内存向量存储驱逐

- **优先级**: P2
- **位置**: `pkg/agent/memory.go` memoryVectorStore
- **验收标准**:
  1. 支持配置 `memory_vector.max_entries`
  2. 超出时按 LRU 驱逐

### REQ-PERF-006: Bus 缓冲区可配置

- **优先级**: P2
- **位置**: `pkg/bus/bus.go:14` (`defaultBusBufferSize = 64`)
- **验收标准**: 支持从 config 读取 `bus.buffer_size`

### REQ-PERF-007: 文件同步可选化

- **优先级**: P2
- **位置**: `pkg/fileutil/file.go:88`
- **验收标准**: `WriteAtomic` 支持可选 sync（批量写入场景可跳过中间 sync）

---

## 9. 测试补充需求

### REQ-TEST-001: memory 模块测试

- **优先级**: P1
- **位置**: `pkg/agent/memory.go` (2900 行，无专用测试)
- **验收标准**:
  1. 覆盖 MemoryStore CRUD 操作
  2. 覆盖向量搜索和 FTS 搜索
  3. 覆盖混合搜索 SearchRelevant
  4. 覆盖 embedder 切换
  5. 行覆盖率 ≥60%

### REQ-TEST-002: session GC 测试

- **优先级**: P1
- **前置**: REQ-BUG-005 完成
- **验收标准**:
  1. 覆盖 LRU 驱逐逻辑
  2. 覆盖 TTL 过期
  3. 覆盖 max_sessions 限制
  4. 覆盖并发 GC + 读写

### REQ-TEST-003: 并发安全 race 测试

- **优先级**: P2
- **验收标准**:
  1. 为 `SessionManager`、`AgentLoop`、`ChannelManager` 添加 `-race` 测试
  2. CI 中启用 `-race` flag

### REQ-TEST-004: Console API 端点测试

- **优先级**: P2
- **位置**: `pkg/httpapi/console.go` 12+ 个 HTTP handler
- **验收标准**:
  1. 每个 API 端点至少 1 个正常 + 1 个异常测试用例
  2. 覆盖认证/授权路径

---

## 10. 需求优先级与排期

### P0 — 必须立即处理（Phase 1: 2-3 天）

**Phase 1a: 运行时正确性修复**（已有详细 TDD 实现计划）

| 编号 | 需求 | 预估工时 | 实现计划 |
|------|------|---------|---------|
| REQ-RT-001 | 跨 provider fallback 切换实例 | 3h | [Task 1](./2026-03-07-runtime-risk-hardening.md) |
| REQ-RT-002 | resume_last_task 语义收紧 | 2h | [Task 2](./2026-03-07-runtime-risk-hardening.md) |
| REQ-RT-003 | Session 快照深拷贝 | 3h | [Task 3](./2026-03-07-runtime-risk-hardening.md) |

**Phase 1b: Bug 修复与安全加固**

| 编号 | 需求 | 预估工时 |
|------|------|---------|
| REQ-BUG-001 | Goroutine 泄漏修复 | 2h |
| REQ-BUG-002 | HTTP Body 错误处理 | 1h |
| REQ-BUG-003 | JSON 解码错误处理 | 2h |
| REQ-BUG-005 | 会话 GC 机制 | 4h |
| REQ-SEC-003 | 消除 stdout 输出 | 0.5h |

### P1 — 应尽快处理（Phase 2-3: 4-6 天）

| 编号 | 需求 | 预估工时 | 备注 |
|------|------|---------|------|
| REQ-RT-004 | PlanMode/Estop/ToolPolicy 硬拦截 | 3h | [Task 4](./2026-03-07-runtime-risk-hardening.md) |
| REQ-RT-005 | Tool loop 检测保守策略 | 2h | [Task 5](./2026-03-07-runtime-risk-hardening.md) |
| REQ-BUG-004,006,007,008,009,010,011 | 其余 Bug 修复 | 4h | |
| REQ-SEC-001,002 | 安全加固 | 3h | |
| REQ-ARCH-001,002 | 配置+memory 拆分 | 4h | |
| REQ-QUAL-001,002,003,004 | 代码质量 | 6h | |
| REQ-PERF-001,002,003 | 性能优化 | 3h | REQ-PERF-002 与 REQ-RT-002 合并 |
| REQ-TEST-001,002 | 测试补充 | 6h | |

### P2 — 可规划到后续迭代（Phase 4-6: 5-8 天）

| 编号 | 需求 | 预估工时 |
|------|------|---------|
| REQ-ARCH-003,004,005,006 | 文件拆分 + pkg 迁移 | 8h |
| REQ-QUAL-005,006,007,008 | 代码质量 | 4h |
| REQ-PERF-004,005,006,007 | 性能优化 | 4h |
| REQ-TEST-003,004 | 测试补充 | 4h |

---

## 11. 验收检查清单

每个 Phase 完成后需通过以下检查：

- [ ] `go build ./...` 成功
- [ ] `go vet ./...` 无新增警告
- [ ] `go test ./...` 全部通过
- [ ] `go test -race ./...` 无数据竞争（针对并发相关变更）
- [ ] 功能回归测试（gateway 启动 + health check + agent 单轮对话）
- [ ] 无新增 `golangci-lint` 警告
- [ ] git diff 确认变更范围与需求一致
