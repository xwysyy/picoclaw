# X-Claw 全面重构优化设计文档

> **日期**: 2026-03-07
> **范围**: 全仓库审查（209 Go 源文件，~73K LOC）
> **目标**: 在保持现有功能不变的前提下，改善架构、消除 Bug、提升性能与可维护性
> **审查维度**: 架构/目录结构、代码质量/重复代码、Bug/安全漏洞、性能/依赖关系

---

## 目录

- [一、项目现状总览](#一项目现状总览)
- [二、发现的 Bug 与安全漏洞（按严重等级）](#二发现的-bug-与安全漏洞按严重等级)
- [三、架构层面问题与重构方案](#三架构层面问题与重构方案)
- [四、代码质量问题与优化方案](#四代码质量问题与优化方案)
- [五、性能问题与优化方案](#五性能问题与优化方案)
- [六、依赖关系优化](#六依赖关系优化)
- [七、测试覆盖改进计划](#七测试覆盖改进计划)
- [八、重构执行路线图](#八重构执行路线图)
- [附录 A：超大文件清单](#附录-a超大文件清单)
- [附录 B：架构评分卡](#附录-b架构评分卡)

---

## 一、项目现状总览

### 1.1 项目概况

| 指标 | 值 |
|------|------|
| 语言 | Go 1.25.7 |
| 源文件数 | 209 (.go) |
| 代码行数 | ~73,000 |
| 测试文件数 | ~72 |
| 直接依赖 | 28 |
| 间接依赖 | ~200 |
| CLI 命令 | `gateway` / `agent` / `version` |

### 1.2 核心架构

```
cmd/x-claw/main.go
    ├── gateway → internal/gateway/server.go → HTTP 服务
    └── agent   → pkg/agent/loop.go → Agent 主循环

核心依赖链：
    internal/core/ports/    (零外部依赖，接口定义)
         ↑
    pkg/agent/loop.go       (编排器)
    ├── pkg/providers/      (LLM 提供商)
    ├── pkg/tools/          (工具框架)
    ├── pkg/session/        (会话管理)
    ├── pkg/channels/       (通道适配)
    ├── pkg/bus/            (事件总线)
    ├── pkg/mcp/            (MCP 协议)
    └── pkg/config/         (配置系统)
```

### 1.3 架构优点

- **Ports 模式**（`internal/core/ports/`）成功隔离了核心域与基础设施
- **架构守卫测试**（`internal/archcheck/`）确保 `pkg/agent` 不依赖 `pkg/channels/httpapi/media`
- **`internal/core/` 零外部依赖**约束得到严格执行
- **无循环依赖**
- **原子文件写入**（temp+sync+rename）保障持久化安全
- **锁分片**（FNV 哈希，64 分片）用于会话并发控制

---

## 二、发现的 Bug 与安全漏洞（按严重等级）

### 2.1 HIGH — 必须修复

#### BUG-H1: Goroutine 泄漏 — Placeholder 调度

- **文件**: `pkg/channels/manager.go:210-251`
- **问题**: `SchedulePlaceholder` 中启动的 goroutine，当 `send(sendCtx)` 阻塞时无超时保护，goroutine 可能永远不退出
- **影响**: 长期运行的 Gateway 累积泄漏 goroutine，最终 OOM
- **修复方案**: 为 `sendCtx` 添加超时（如 30s），确保 goroutine 有退出路径

```go
// 修复前
send(sendCtx)

// 修复后
sendCtx, sendCancel := context.WithTimeout(sendCtx, 30*time.Second)
defer sendCancel()
send(sendCtx)
```

#### BUG-H2: HTTP 响应 Body 读取错误被忽略

- **文件**: `pkg/auth/oauth.go:201-217, 289-305, 343-363`
- **问题**: `io.ReadAll(resp.Body)` 的错误被 `_` 忽略
- **影响**: 网络异常时丢失错误信息，可能导致空指针或静默失败
- **修复方案**: 检查并处理 `io.ReadAll` 的错误

#### BUG-H3: JSON 反序列化错误被显式忽略

- **文件**: `pkg/httpapi/console.go:1903`
- **问题**: `_ = json.NewDecoder(r.Body).Decode(...)` 忽略了格式错误的请求体
- **影响**: 客户端发送畸形 JSON 时不会收到错误反馈
- **修复方案**: 检查 error 并返回 400 Bad Request

#### BUG-H4: `strconv.Atoi` 错误未处理

- **文件**: `pkg/auth/oauth.go:277`
- **问题**: `parseFlexibleInt` 中 `strconv.Atoi(intervalStr)` 的错误直接返回，但之前对空字符串的处理逻辑与此不一致
- **影响**: 非数字字符串输入时返回含义不明的错误
- **修复方案**: 统一错误路径，对 `Atoi` 失败返回清晰的错误消息

#### BUG-H5: 会话 Map 无 GC 机制（OOM 风险）

- **文件**: `pkg/session/manager.go:24`
- **问题**: `sessions map[string]*Session` 无界增长，无驱逐策略
- **影响**: 长期运行的 Gateway 累积会话数据，最终 OOM
- **修复方案**: 添加 LRU 驱逐或 TTL 过期机制，可配置最大会话数

### 2.2 MEDIUM — 应尽快修复

#### BUG-M1: 共享字段并发竞态

- **文件**: `pkg/agent/memory.go:2612-2623`
- **问题**: `memoryFTSStore.lastFingerprint` 可能在 mutex 保护范围之外被读写
- **修复**: 确保所有对 `lastFingerprint` 的访问都在 `mu.Lock()` 保护下

#### BUG-M2: Model Downgrade 状态竞态

- **文件**: `pkg/agent/loop.go:61-66, 372-375`
- **问题**: `modelAutoDowngradeMap` 在 mutex 间隙中可能读取过时状态
- **修复**: 将读写操作合并到单个临界区

#### BUG-M3: Database 连接泄漏

- **文件**: `pkg/agent/memory.go:2761-2779`
- **问题**: `openDBLocked()` 中 `sql.Open()` 成功后，后续操作失败时连接未释放
- **修复**: 在错误路径中添加 `db.Close()`

#### BUG-M4: Context 未从父级继承

- **文件**: `pkg/channels/manager.go:202`
- **问题**: `context.WithCancel(context.Background())` 应从调用者 context 继承
- **修复**: 改为 `context.WithCancel(ctx)`

#### BUG-M5: JSON 编码错误被大量忽略

- **文件**: `pkg/httpapi/console.go:95, 106, 123, 135, 169, 494` 等 20+ 处
- **问题**: `_ = json.NewEncoder(w).Encode(...)` 编码失败时客户端无响应
- **修复**: 至少记录日志；对关键路径返回 500

#### BUG-M6: `io.Copy` 后的 `Close` 错误处理

- **文件**: `pkg/agent/run_pipeline_impl.go:1342-1352`
- **问题**: `copyFile` 中如果 `io.Copy` 成功但 `Close()` 失败，返回的字节数 `n` 可能误导调用方
- **修复**: 在 `Close` 失败时也设 `n = 0` 或合并错误

### 2.3 安全漏洞

#### SEC-M1: Shell 命令注入防护依赖正则黑名单

- **文件**: `pkg/tools/shell.go:53-100`
- **问题**: 使用 regex 黑名单过滤危险命令，可能被嵌套/转义绕过
- **建议**: 考虑补充白名单机制或沙箱执行
- **严重等级**: MEDIUM（已有 Docker sandbox 选项作为缓解）

#### SEC-M2: API Key 可能泄漏到日志

- **文件**: `pkg/providers/anthropic/provider.go:44-45`
- **问题**: 无显式的日志过滤确保 token 不被记录
- **建议**: 添加日志脱敏中间件

#### SEC-M3: `fmt.Printf` 直接输出到 stdout

- **文件**: `pkg/tools/shell.go:148`
- **问题**: `fmt.Printf("Using custom deny patterns: %v\n", ...)` 和 `fmt.Println("Warning: deny patterns are disabled...")` 不经过 logger
- **修复**: 替换为 `logger.Warn(...)` / `logger.Debug(...)`

---

## 三、架构层面问题与重构方案

### 3.1 pkg/ 暴露过多内部实现

**问题**: 以下包放在 `pkg/` 中但实际上是内部实现，不应作为公开 API：

| 当前位置 | 推荐 | 理由 |
|---------|------|------|
| `pkg/channels/` | `internal/channels/` | 通道适配器是内部实现 |
| `pkg/providers/` | `internal/providers/` | LLM 集成是内部实现 |
| `pkg/session/` | `internal/session/` | 会话管理是内部实现 |
| `pkg/memory/` | `internal/memory/` | 存储适配是内部实现 |
| `pkg/httpapi/` | `internal/httpapi/` | HTTP 处理是内部实现 |
| `pkg/mcp/` | `internal/mcp/` | MCP 适配是内部实现 |
| `pkg/bus/` | `internal/bus/` | 事件总线是内部实现 |
| `pkg/state/` | `internal/state/` | 状态管理是内部实现 |
| `pkg/media/` | `internal/media/` | 媒体处理是内部实现 |
| `pkg/auditlog/` | `internal/auditlog/` | 审计是内部实现 |
| `pkg/identity/` | `internal/identity/` | 身份解析是内部实现 |

**保留在 `pkg/` 的核心公开 API**：
- `pkg/agent/` — Agent 核心循环
- `pkg/tools/` — 工具框架（可扩展）
- `pkg/config/` — 配置结构体
- `pkg/routing/` — 路由 facade

**重构方案**:
1. 分批迁移，每批 2-3 个包
2. 使用 `gomvpkg` 或手动 rename + sed 更新 import
3. 每批迁移后运行全量测试确认

### 3.2 配置膨胀（48+ struct 单文件）

**文件**: `pkg/config/config.go` (~1200 行)

**问题**: 48+ 个 Config 结构体混在单文件中，包含 15+ 种通道配置和 18+ 种提供商配置。

**重构方案**: 按域拆分

```
pkg/config/
├── config.go          # 顶级 Config + 通用类型 (~200 行)
├── agents.go          # AgentsConfig, AgentDefaults (~150 行)
├── channels.go        # ChannelsConfig + 各通道配置 (~300 行)
├── providers.go       # ProvidersConfig + 各提供商配置 (~200 行)
├── tools.go           # ToolsConfig + 子配置 (~200 行)
├── gateway.go         # GatewayConfig + 子配置 (~100 行)
└── defaults.go        # 默认值（保持不变）
```

**注意**: 拆分仅是文件级别的，不改变包名和导出 API，对外部使用者零影响。

### 3.3 超大文件需要拆分

#### `pkg/agent/memory.go` (~2900 行)

**当前职责混杂**: MemoryStore + 向量索引 + FTS + 搜索工具 + 嵌入器

**拆分方案**:

```
pkg/agent/
├── memory.go              # MemoryStore 核心 (~600 行)
├── memory_vector.go       # memoryVectorStore 实现 (~500 行)
├── memory_fts.go          # memoryFTSStore 实现 (~500 行)
├── memory_embedder.go     # 嵌入器 (hashed/openai_compat) (~400 行)
├── memory_tools.go        # MemorySearchTool/MemoryGetTool (~500 行)
└── memory_test.go         # 补充测试
```

#### `pkg/agent/loop.go` (~4194 行)

**主要问题**: `handleCommand()` 函数 150+ 行，包含所有命令分支

**拆分方案**:

```
pkg/agent/
├── loop.go                # AgentLoop 核心 + 消息分发 (~1500 行)
├── loop_commands.go       # handleCommand + 各命令处理函数 (~800 行)
├── loop_compaction.go     # 上下文压缩逻辑 (~300 行)
├── loop_token_usage.go    # TokenUsage 追踪 (~300 行)
└── loop_model_downgrade.go # 模型自动降级 (~200 行)
```

#### `pkg/channels/manager.go` (~1762 行)

**拆分方案**:

```
pkg/channels/
├── manager.go             # ChannelManager 核心 (~600 行)
├── manager_placeholder.go # 占位符调度 (~300 行)
├── manager_typing.go      # Typing indicator 管理 (~200 行)
├── manager_dispatch.go    # 消息派发 + 重试 (~400 行)
├── interfaces.go          # 9 个通道能力接口 (~150 行)
└── rate_config.go         # 速率限制配置 (~50 行)
```

#### `pkg/httpapi/console.go` (~1500 行)

**拆分方案**:

```
pkg/httpapi/
├── console.go             # Console API 注册 + 通用 handler (~300 行)
├── console_sessions.go    # Session 相关 API (~300 行)
├── console_traces.go      # Trace/Audit 相关 API (~400 行)
├── console_stream.go      # SSE/tail 流式 API (~300 行)
└── console_file.go        # 文件下载 API (~200 行)
```

#### `pkg/tools/toolcall_executor.go` (~1000 行)

**拆分方案**:

```
pkg/tools/
├── toolcall_executor.go   # 执行器核心 (~400 行)
├── toolcall_policy.go     # 策略层 (confirm/idempotency/redact) (~300 行)
├── toolcall_hooks.go      # Hook 机制 (~150 行)
└── toolcall_error.go      # 错误模板 (~150 行)
```

---

## 四、代码质量问题与优化方案

### 4.1 重复代码消除

#### 4.1.1 Channel init.go 注册样板

**问题**: `pkg/channels/feishu/init.go` 和 `pkg/channels/telegram/init.go` 等结构完全相同

**方案**: 提取通用注册辅助函数到 `pkg/channels/register.go`

#### 4.1.2 媒体处理重复

**问题**: Feishu (`feishu_64.go:587-650`) 和 Telegram (`telegram.go:354-415`) 的媒体类型判断（image/audio/video/file switch）重复

**方案**: 提取 `pkg/channels/media_helpers.go`，提供通用的媒体类型分发函数

#### 4.1.3 Markdown 转换重复

**问题**: Feishu 和 Telegram 各自实现了 Markdown 转换

**方案**: 提取通用 Markdown 处理到 `pkg/channels/markdown.go`，各通道仅实现差异部分

### 4.2 魔法数字提取

**问题**: 大量未命名常量散布在代码中

**涉及文件及修复**:

```go
// pkg/channels/manager.go - 提取为命名常量
const (
    defaultQueueSize           = 16           // 第 37 行
    typingIndicatorTTL         = 5 * time.Minute  // 第 42 行
    placeholderTTL             = 10 * time.Minute // 第 43 行
    telegramMaxMessageLength   = 4096         // 第 97 行
)

// pkg/channels/manager.go - 速率配置
var defaultChannelRates = map[string]float64{
    "telegram": 20,
    "discord":  5,   // 当前值 1 过于保守
    "slack":    1,
    "line":     10,
}

// pkg/agent/loop.go
const memorySummaryThreshold = 100  // 第 1620 行
```

### 4.3 错误处理标准化

#### 4.3.1 统一 error wrapping

**规则**: 所有 error 必须使用 `fmt.Errorf("...: %w", err)` 保留错误链

**涉及文件**: `pkg/channels/feishu/feishu_32.go:19,23` 等使用 `errors.New()` 的地方

#### 4.3.2 消除不当的 `_ =` 忽略

**规则**: best-effort 操作至少 debug 级别记录错误

```go
// 不推荐
_ = c.bot.SendChatAction(ctx, ...)

// 推荐
if err := c.bot.SendChatAction(ctx, ...); err != nil {
    logger.DebugCF("telegram", "send chat action", map[string]any{"error": err})
}
```

**涉及文件**:
- `pkg/channels/telegram/telegram.go:275,286`
- `pkg/channels/feishu/feishu_64.go:282`
- `pkg/tools/shell.go:516,521`
- `pkg/agent/loop.go:612,1044`

### 4.4 函数复杂度降低

#### `handleCommand()` 重构

**文件**: `pkg/agent/loop.go:935-1130+`
**当前圈复杂度**: ~30+（多个 switch/case 分支）

**方案**: 提取各命令为独立函数

```go
// 重构后
func (al *AgentLoop) handleCommand(ctx context.Context, msg bus.InboundMessage, ...) {
    cmd := parseCommand(msg.Content)
    switch cmd.Name {
    case "/plan":
        al.handlePlanCommand(ctx, msg, cmd)
    case "/approve", "/run":
        al.handleApproveCommand(ctx, msg, cmd)
    case "/cancel":
        al.handleCancelCommand(ctx, msg, cmd)
    case "/mode":
        al.handleModeCommand(ctx, msg, cmd)
    // ...
    }
}
```

### 4.5 Goroutine 生命周期管理

**问题**: 后台 goroutine 缺乏 WaitGroup 或统一的生命周期管理

**涉及文件**:
- `pkg/agent/loop.go:1623` — 压缩 goroutine
- `pkg/channels/telegram/telegram.go:162-168` — Bot handler goroutine
- `pkg/channels/manager.go:210-251` — Placeholder goroutine

**方案**: 为 `AgentLoop` 和 `ChannelManager` 添加 `sync.WaitGroup`，在 `Close()`/`Shutdown()` 中等待所有 goroutine 退出

---

## 五、性能问题与优化方案

### 5.1 HIGH — 字符串拼接效率

**文件**: `pkg/agent/run_pipeline_impl.go:277-280`

```go
// 修复前: 每次 += 产生新分配
userMessage += "\n\n" + note

// 修复后: 使用 Builder
var b strings.Builder
b.Grow(len(userMessage) + len(note) + 2)
b.WriteString(userMessage)
b.WriteString("\n\n")
b.WriteString(note)
userMessage = b.String()
```

**预估收益**: 高频路径减少 5-10% 内存分配

### 5.2 HIGH — 会话恢复全文件扫描

**文件**: `pkg/agent/run_pipeline_impl.go:935-1074`

**问题**: `findLastUnfinishedRun` 遍历所有运行目录的 events.jsonl，O(n) 复杂度

**方案**:
1. 维护一个轻量级索引文件（如 `runs/index.json`），记录每个 run 的状态
2. 或使用 SQLite 索引（已有 SQLite 依赖）
3. 短期: 从最新目录开始反向扫描（当前是正向）

### 5.3 HIGH — JSON 序列化缩进浪费

**文件**: `pkg/memory/jsonl.go:120`

```go
// 修复前: 缩进增加 IO 和内存
data, err := json.MarshalIndent(meta, "", "  ")

// 修复后: 紧凑格式
data, err := json.Marshal(meta)
```

**预估收益**: 磁盘 IO 减少 5-15%

### 5.4 MEDIUM — HTTP 客户端未复用

**涉及文件**:
- `pkg/skills/registry.go` — 每次请求新建 client
- `pkg/mcp/manager.go` — 每个 SSE 连接新建 client

**方案**: 提取全局或包级别的 `http.Client` 单例，配置合理的连接池

### 5.5 MEDIUM — 内存向量存储无驱逐

**文件**: `pkg/agent/memory.go`

**问题**: 向量索引在内存中无限增长，无 LRU 或 TTL

**方案**: 实现有界缓存（可配置 `max_entries`），超出时按 LRU 驱逐

### 5.6 MEDIUM — 文件同步过于频繁

**文件**: `pkg/fileutil/file.go:88`

**问题**: 每次写入都调用 `tmpFile.Sync()`

**方案**: 提供 `WriteAtomicOptions` 支持可选的 sync 行为，批量写入时跳过中间 sync

### 5.7 LOW — Bus 缓冲区可配置化

**文件**: `pkg/bus/bus.go:26-28`

**当前**: 固定 64 缓冲

**方案**: 从 config 读取 `bus.buffer_size`，默认 64

---

## 六、依赖关系优化

### 6.1 应清理的依赖

| 依赖 | 问题 | 建议 |
|------|------|------|
| `gogo/protobuf` | 已停止维护 | 迁移到官方 `google.golang.org/protobuf` |
| `fastjson` / `grbit/go-json` / `sonic` | 多个 JSON 库共存但未充分使用（Makefile 用 `stdjson` 标签） | 移除未使用的 JSON 库 |
| `go-resty` + `fasthttp` + 标准 `net/http` | 三个 HTTP 库 | 统一到标准库，仅在性能关键路径用 fasthttp |

### 6.2 `pkg/` 层依赖 `internal/` 的反向依赖

**文件**: `pkg/agent/run_pipeline_impl.go:4`

```go
import "github.com/xwysyy/X-Claw/internal/core/events"
```

**问题**: `pkg/` 不应依赖 `internal/`

**方案**: 将 `internal/core/events` 中被 `pkg/` 使用的类型提升到 `pkg/core/events` 或作为 `pkg/agent` 的本地类型

---

## 七、测试覆盖改进计划

### 7.1 当前测试覆盖

| 模块 | 测试文件数 | 覆盖状态 |
|------|-----------|---------|
| `pkg/agent/` | 4 | 较好，但 memory.go 2900 行无专用测试 |
| `pkg/channels/` | 6 | 较好 |
| `pkg/providers/` | 15+ | 很好 |
| `pkg/tools/` | 10+ | 很好 |
| `pkg/memory/` | 1 | **严重不足** |
| `pkg/media/` | 1 | 不足 |
| `pkg/mcp/` | 1 | 不足 |
| `pkg/session/` | 0-1 | **严重不足** |
| `pkg/state/` | 1 | 不足 |

### 7.2 补充测试计划

**优先级 P0（与 Bug 修复同步）**:
- `pkg/agent/memory_test.go` — MemoryStore 核心功能 + 向量搜索 + FTS
- `pkg/session/manager_test.go` — 会话生命周期 + 并发安全 + GC 机制

**优先级 P1（重构后补充）**:
- `pkg/channels/manager_dispatch_test.go` — 消息派发 + 重试 + 速率限制
- `pkg/httpapi/console_test.go` — Console API 各端点

**优先级 P2（增量添加）**:
- `pkg/memory/jsonl_test.go` — JSONL 读写边界条件
- `pkg/mcp/manager_test.go` — MCP 连接 + 工具注册
- 跨模块集成测试（agent + channels 联动）

### 7.3 测试质量提升

- 将超过 1000 行的测试文件拆分
- 为并发相关代码添加 `-race` 测试
- 使用 `t.Parallel()` 加速测试执行

---

## 八、重构执行路线图

### Phase 1: Bug 修复与安全加固（1-2 天）

**目标**: 消除所有 HIGH 级别 Bug，不改变架构

| 编号 | 任务 | 文件 | 风险 |
|------|------|------|------|
| 1.1 | 修复 Goroutine 泄漏 (BUG-H1) | `pkg/channels/manager.go` | 低 |
| 1.2 | 修复 HTTP Body 读取错误处理 (BUG-H2) | `pkg/auth/oauth.go` | 低 |
| 1.3 | 修复 JSON 解码错误忽略 (BUG-H3) | `pkg/httpapi/console.go` | 低 |
| 1.4 | 修复 strconv.Atoi 错误处理 (BUG-H4) | `pkg/auth/oauth.go` | 低 |
| 1.5 | 添加会话 GC 机制 (BUG-H5) | `pkg/session/manager.go` | 中 |
| 1.6 | 修复 fmt.Printf → logger (SEC-M3) | `pkg/tools/shell.go` | 低 |
| 1.7 | 修复 MEDIUM 级 Bug (BUG-M1~M6) | 多处 | 低-中 |

### Phase 2: 文件拆分（2-3 天）

**目标**: 拆分超大文件，不改变公开 API

| 编号 | 任务 | 原文件 | 拆分为 |
|------|------|--------|--------|
| 2.1 | 拆分配置文件 | `pkg/config/config.go` | 6 个文件 |
| 2.2 | 拆分内存模块 | `pkg/agent/memory.go` | 5 个文件 |
| 2.3 | 拆分主循环 | `pkg/agent/loop.go` | 5 个文件 |
| 2.4 | 拆分通道管理器 | `pkg/channels/manager.go` | 5 个文件 |
| 2.5 | 拆分控制台 API | `pkg/httpapi/console.go` | 5 个文件 |
| 2.6 | 拆分工具执行器 | `pkg/tools/toolcall_executor.go` | 4 个文件 |

**验证**: 每个拆分后运行 `go build ./...` + `go test ./...` + `go vet ./...`

### Phase 3: 代码质量提升（2-3 天）

**目标**: 消除代码异味，提升可维护性

| 编号 | 任务 |
|------|------|
| 3.1 | 提取魔法数字为命名常量 |
| 3.2 | 消除 Channel 间的重复代码 |
| 3.3 | 统一 error wrapping（`%w`） |
| 3.4 | 将 `_ =` 替换为 error 日志记录 |
| 3.5 | 降低 `handleCommand()` 圈复杂度 |
| 3.6 | 添加 Goroutine WaitGroup 生命周期管理 |

### Phase 4: 性能优化（1-2 天）

| 编号 | 任务 | 预估收益 |
|------|------|---------|
| 4.1 | 字符串拼接 → Builder | 内存 -5~10% |
| 4.2 | JSON 缩进 → 紧凑格式 | 磁盘 IO -5~15% |
| 4.3 | HTTP 客户端单例化 | 连接复用 |
| 4.4 | 会话恢复反向扫描 | 延迟 -30~50% |

### Phase 5: 架构改善（3-5 天，可选）

| 编号 | 任务 | 风险 |
|------|------|------|
| 5.1 | 迁移 `pkg/channels/` → `internal/channels/` | 中 |
| 5.2 | 迁移 `pkg/providers/` → `internal/providers/` | 中 |
| 5.3 | 迁移其余内部包到 `internal/` | 中 |
| 5.4 | 解决 `pkg/` → `internal/core/` 反向依赖 | 低 |
| 5.5 | 依赖清理（gogo/protobuf, 多余 JSON 库） | 低 |

### Phase 6: 测试补充（持续）

与各 Phase 并行执行，确保每次重构都有对应的测试覆盖。

---

## 附录 A：超大文件清单

| 文件 | 行数 | 严重度 | Phase |
|------|------|--------|-------|
| `pkg/agent/loop.go` | ~4194 | 高 | 2.3 |
| `pkg/agent/memory.go` | ~2900 | 高 | 2.2 |
| `pkg/channels/manager.go` | ~1762 | 中 | 2.4 |
| `pkg/httpapi/console.go` | ~1500 | 中 | 2.5 |
| `pkg/config/config.go` | ~1200 | 中 | 2.1 |
| `pkg/tools/toolcall_executor.go` | ~1000 | 中 | 2.6 |
| `pkg/session/manager.go` | ~800 | 低 | — |

## 附录 B：架构评分卡

| 维度 | 当前评分 | 目标评分 | 关键改进 |
|------|---------|---------|---------|
| 分层清晰度 | 7/10 | 9/10 | pkg/ → internal/ 迁移 |
| 耦合度 | 8/10 | 8/10 | 已有 ports 模式，保持 |
| 模块独立性 | 6/10 | 8/10 | 文件拆分 + 接口隔离 |
| 配置管理 | 5/10 | 7/10 | 按域拆分配置文件 |
| 接口设计 | 7/10 | 8/10 | 通道接口分组 |
| 可维护性 | 6/10 | 8/10 | 消除超大文件 |
| 可扩展性 | 6/10 | 7/10 | 配置注册表化 |
| 测试覆盖 | 5/10 | 7/10 | 补充核心模块测试 |
| 安全性 | 7/10 | 8/10 | Bug 修复 + 日志脱敏 |
| 性能 | 6/10 | 8/10 | 内存 + IO 优化 |
| **总体** | **6.3/10** | **7.8/10** | — |

---

> **文档审查人**: Claude Opus 4.6
> **审查方法**: 源代码静态分析（4 并行 Agent：架构/代码质量/Bug安全/性能依赖）
> **约束**: 所有重构保持现有功能不变，每个 Phase 独立可验证
