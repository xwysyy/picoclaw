# X-Claw 重构优化技术实现文档

> **文档编号**: IMPL-2026-0307
> **版本**: v1.1
> **日期**: 2026-03-07
> **关联文档**: [需求文档](./2026-03-07-refactor-requirements.md) | [审查设计文档](./2026-03-07-comprehensive-refactor-design.md) | [运行时风险加固设计](./2026-03-07-runtime-risk-hardening-design.md) | [运行时风险加固实现](./2026-03-07-runtime-risk-hardening.md)
>
> **v1.1 变更说明**: 整合已有的 runtime-risk-hardening 实现计划，新增 Phase 1a（运行时正确性），调整 Phase 编号。

---

## 目录

- [Phase 1a: 运行时正确性修复](#phase-1a-运行时正确性修复)
- [Phase 1b: Bug 修复与安全加固](#phase-1b-bug-修复与安全加固)
- [Phase 2: 文件拆分](#phase-2-文件拆分)
- [Phase 3: 代码质量提升](#phase-3-代码质量提升)
- [Phase 4: 性能优化](#phase-4-性能优化)
- [Phase 5: 架构改善](#phase-5-架构改善)
- [Phase 6: 测试补充](#phase-6-测试补充)
- [附录: 工具与命令速查](#附录-工具与命令速查)

---

## Phase 1a: 运行时正确性修复

> 本 Phase 对应已有的 [runtime-risk-hardening.md](./2026-03-07-runtime-risk-hardening.md) 中的 Task 1-5。
> 已有文档提供了完整的 TDD 实现步骤（写失败测试 → 验证失败 → 最小实现 → 验证通过），以下仅做概要说明和与本轮重构其他需求的关联分析。

### 1a.1 REQ-RT-001: 跨 provider fallback 切换 provider 实例

**文件**: `pkg/agent/loop.go`, `pkg/agent/instance.go`, `pkg/providers/fallback.go`

**问题本质**: fallback candidate 能表达 `provider/model` 组合，但调用路径复用原 provider 实例只改 model 名。

**实现要点**:
- 在 agent runtime 增加最小的 provider 选择层
- 为每个 fallback candidate 解析对应 provider 实例
- 跨 provider 候选必须切到目标 provider，同 provider 可复用

**测试**: `go test ./pkg/agent -run 'TestAgentLoop_.*Fallback.*Provider' -count=1`

**详细步骤**: 见 [runtime-risk-hardening.md Task 1](./2026-03-07-runtime-risk-hardening.md)

---

### 1a.2 REQ-RT-002: resume_last_task 语义收紧

**文件**: `pkg/agent/run_pipeline_impl.go`, `pkg/agent/instance.go`

**问题本质**: `run.error` 不应进入 resume 候选；扫描范围应覆盖所有 agent workspace。

**实现要点**:
- `run.end` 和 `run.error` 均为终态
- 候选扫描范围扩展为 agent registry 可见 workspace 集合
- **与 REQ-PERF-002 合并**: 扫描时采用反向遍历（最新优先），找到第一个可恢复候选即返回

**测试**: `go test ./pkg/agent -run 'TestFindLastUnfinishedRun|TestResumeLastTask' -count=1`

**详细步骤**: 见 [runtime-risk-hardening.md Task 2](./2026-03-07-runtime-risk-hardening.md)

---

### 1a.3 REQ-RT-003: Session 快照深拷贝

**文件**: `pkg/session/manager.go`, `pkg/session/manager_mutations.go`

**问题本质**: 当前只复制顶层 slice，嵌套 Media/ToolCalls/Function 共享底层对象。

**实现要点**:
- 新增 clone helper: `cloneMessage(providers.Message) providers.Message`
- 递归深拷贝 Media、ToolCalls、Function 等引用字段
- 所有对外 API 统一走 clone: `GetHistory`, `SetHistory`, `GetSessionSnapshot`, `ListSessionSnapshots`

**测试**: `go test ./pkg/session -run 'Test.*DeepCopy' -count=1`

**详细步骤**: 见 [runtime-risk-hardening.md Task 3](./2026-03-07-runtime-risk-hardening.md)

---

### 1a.4 REQ-RT-004: PlanMode / Estop / ToolPolicy 硬拦截

**文件**: `pkg/tools/toolcall_executor.go`

**问题本质**: 限制当前是提示词级 advisory，执行器仍实际执行工具。

**实现要点**:
- 在 `ExecuteToolCalls`（行 107-444）的 `runOne()` 前置判断
- 执行顺序: Estop → PlanMode → ToolPolicy
- 命中时返回结构化 JSON 错误（`kind=tool_policy_denied`），不执行工具
- **与 Phase 2 toolcall_executor.go 拆分的关系**: 本需求先实现逻辑，Phase 2 再拆分文件

**测试**: `go test ./pkg/tools -run 'TestExecuteToolCalls_(PlanMode|Estop|ToolPolicy)' -count=1`

**详细步骤**: 见 [runtime-risk-hardening.md Task 4](./2026-03-07-runtime-risk-hardening.md)

---

### 1a.5 REQ-RT-005: Tool loop 检测保守策略

**文件**: `pkg/agent/loop.go`

**问题本质**: 全历史累计重复次数判 loop 导致误伤。

**实现要点**:
- 改为最近窗口（如最近 10 次工具调用）内连续重复检测
- 阈值可配置（建议默认连续 3 次相同签名）
- 保持返回语义不变（loop-detected tool error）

**测试**: `go test ./pkg/agent -run 'TestDetectToolCallLoop' -count=1`

**详细步骤**: 见 [runtime-risk-hardening.md Task 5](./2026-03-07-runtime-risk-hardening.md)

---

### Phase 1a 验证清单

```bash
# 运行时正确性专项回归
go test ./pkg/agent -run 'TestAgentLoop_.*Fallback.*Provider|TestFindLastUnfinishedRun|TestResumeLastTask|TestDetectToolCallLoop' -count=1
go test ./pkg/tools -run 'TestExecuteToolCalls_(PlanMode|Estop|ToolPolicy)' -count=1
go test ./pkg/session -run 'Test.*DeepCopy' -count=1

# 三包全量测试
go test ./pkg/agent ./pkg/tools ./pkg/session ./pkg/providers -count=1
```

---

## Phase 1b: Bug 修复与安全加固

### 1.1 REQ-BUG-001: 修复 Placeholder Goroutine 泄漏

**文件**: `pkg/channels/manager.go`

**当前代码** (行 166-252):

```go
func (m *Manager) SchedulePlaceholder(
    ctx context.Context, channel string, chatID string,
    send func(context.Context) (string, error), delay time.Duration,
) {
    // ...
    if delay == 0 {
        sendCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // 行 190: BUG
        defer cancel()
        // ...
        return
    }

    scheduleCtx, cancel := context.WithCancel(context.Background()) // 行 202: BUG
    // ...
    go func() { // 行 210: goroutine 启动
        defer func() { /* 清理逻辑 */ }()
        select {
        case <-timer.C:
            sendCtx, sendCancel := context.WithTimeout(scheduleCtx, 10*time.Second)
            defer sendCancel()
            if id, err := send(sendCtx); /* ... */ { /* ... */ }
        case <-scheduleCtx.Done():
            return
        case <-ctx.Done():        // 行 245
            return
        }
    }()
}
```

**修复方案**:

```go
func (m *Manager) SchedulePlaceholder(
    ctx context.Context, channel string, chatID string,
    send func(context.Context) (string, error), delay time.Duration,
) {
    // ...
    if delay == 0 {
        // 修复: 从调用者 ctx 派生，保留超时继承
        sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
        defer cancel()
        // ... (其余不变)
        return
    }

    // 修复: 从调用者 ctx 派生
    scheduleCtx, cancel := context.WithCancel(ctx)
    // ...
    go func() {
        defer func() { /* 清理逻辑不变 */ }()
        select {
        case <-timer.C:
            // 修复: send 操作加超时保护
            sendCtx, sendCancel := context.WithTimeout(scheduleCtx, 30*time.Second)
            defer sendCancel()
            if id, err := send(sendCtx); /* ... */ { /* ... */ }
        case <-scheduleCtx.Done():
            return
        }
        // 注意: 移除独立的 case <-ctx.Done()，因为 scheduleCtx 已从 ctx 派生
    }()
}
```

**变更点**:
1. 行 190: `context.Background()` → `ctx`
2. 行 202: `context.Background()` → `ctx`
3. 行 237: send 超时从 10s 改为 30s
4. 行 245: 移除独立的 `case <-ctx.Done()`（已通过 scheduleCtx 继承）

**测试**:
```go
func TestSchedulePlaceholder_SendTimeout(t *testing.T) {
    // 模拟 send 永远阻塞的场景
    // 验证 goroutine 在超时后退出
}
```

**验证命令**: `go test ./pkg/channels/ -run TestSchedulePlaceholder -count=1 -race`

---

### 1.2 REQ-BUG-002: 修复 HTTP Body 读取错误

**文件**: `pkg/auth/oauth.go`

**变更**: 5 处 `io.ReadAll` 调用

**修复模式** (以行 215 为例):

```go
// 修复前
body, _ := io.ReadAll(resp.Body)

// 修复后
body, err := io.ReadAll(resp.Body)
if err != nil {
    return nil, fmt.Errorf("read response body: %w", err)
}
```

**所有需要修改的位置**:

| 行号 | 函数 | 修复 |
|------|------|------|
| 215 | `RequestDeviceCode` | 添加 err 检查 |
| 303 | `LoginDeviceCode` | 添加 err 检查 |
| 363 | `pollDeviceCode` | 添加 err 检查 |
| 404 | `RefreshAccessToken` | 添加 err 检查 |
| 497 | `ExchangeCodeForTokens` | 添加 err 检查 |

**验证命令**: `go test ./pkg/auth/ -count=1`

---

### 1.3 REQ-BUG-003 + REQ-BUG-010: Console JSON 错误处理

**文件**: `pkg/httpapi/console.go`

**实现**: 提取 `writeJSON` 辅助函数

```go
// 在 console.go 顶部添加辅助函数
func writeJSON(w http.ResponseWriter, status int, data any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    if err := json.NewEncoder(w).Encode(data); err != nil {
        logger.ErrorCF("httpapi", "json encode failed", map[string]any{"error": err.Error()})
    }
}

func readJSON(r *http.Request, dst any) error {
    if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
        return fmt.Errorf("invalid JSON: %w", err)
    }
    return nil
}
```

**批量替换**:

```go
// 修复前 (行 95, 106, 123, 135, 169, 861 等 20+ 处)
_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "method not allowed"})

// 修复后
writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
```

**关键修复** — `/api/resume_last_task` (行 1903):

```go
// 修复前
_ = json.NewDecoder(r.Body).Decode(&map[string]any{})

// 修复后
var reqBody map[string]any
if err := readJSON(r, &reqBody); err != nil {
    writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
    return
}
```

**验证命令**: `go test ./pkg/httpapi/ -count=1`

---

### 1.4 REQ-BUG-005: 会话垃圾回收

**文件**: `pkg/session/manager.go`

**当前 struct** (行 23-27):

```go
type SessionManager struct {
    sessions map[string]*Session
    mu       sync.RWMutex
    storage  string
}
```

**实现方案**:

#### 步骤 1: 扩展配置

在 `pkg/config/config.go` 的 `SessionConfig` 中添加:

```go
type SessionConfig struct {
    Scope       string `json:"scope,omitempty"`        // 现有字段
    MaxSessions int    `json:"max_sessions,omitempty"` // 新增: 最大内存会话数, 默认 1000
    TTLHours    int    `json:"ttl_hours,omitempty"`    // 新增: 会话 TTL(小时), 默认 168 (7天)
}
```

#### 步骤 2: 扩展 SessionManager

```go
type SessionManager struct {
    sessions    map[string]*Session
    mu          sync.RWMutex
    storage     string
    maxSessions int           // 从 config 读取
    ttl         time.Duration // 从 config 读取
}
```

#### 步骤 3: 添加 GC 方法

```go
// evictExpired 驱逐过期会话（仅从内存移除，磁盘保留）
func (sm *SessionManager) evictExpired() int {
    sm.mu.Lock()
    defer sm.mu.Unlock()

    now := time.Now()
    evicted := 0
    for key, s := range sm.sessions {
        if sm.ttl > 0 && now.Sub(s.UpdatedAt) > sm.ttl {
            delete(sm.sessions, key)
            evicted++
        }
    }
    return evicted
}

// evictLRU 按最后活跃时间驱逐，直到数量 <= maxSessions
func (sm *SessionManager) evictLRU() int {
    sm.mu.Lock()
    defer sm.mu.Unlock()

    if sm.maxSessions <= 0 || len(sm.sessions) <= sm.maxSessions {
        return 0
    }

    // 收集所有会话按 UpdatedAt 排序
    type entry struct {
        key       string
        updatedAt time.Time
    }
    entries := make([]entry, 0, len(sm.sessions))
    for k, s := range sm.sessions {
        entries = append(entries, entry{k, s.UpdatedAt})
    }
    sort.Slice(entries, func(i, j int) bool {
        return entries[i].updatedAt.Before(entries[j].updatedAt)
    })

    evicted := 0
    toEvict := len(sm.sessions) - sm.maxSessions
    for i := 0; i < toEvict && i < len(entries); i++ {
        delete(sm.sessions, entries[i].key)
        evicted++
    }
    return evicted
}
```

#### 步骤 4: 在 GetOrCreate 中触发 GC

```go
func (sm *SessionManager) GetOrCreate(key string) *Session {
    sm.mu.Lock()
    defer sm.mu.Unlock()

    if s, ok := sm.sessions[key]; ok {
        return s
    }

    // 新会话前触发 GC（低频，不影响性能）
    if sm.maxSessions > 0 && len(sm.sessions) >= sm.maxSessions {
        // 解锁后再加锁的方式不安全，直接在这里做内联 evict
        sm.inlineEvictLRULocked()
    }

    s := sm.ensureSessionLocked(key)
    return s
}
```

**验证命令**: `go test ./pkg/session/ -count=1 -race`

---

### 1.5 REQ-BUG-007: DB 连接泄漏修复

**文件**: `pkg/agent/memory.go`

**当前代码** (行 2761-2789):

```go
func (fs *memoryFTSStore) openDBLocked() (*sql.DB, error) {
    if fs.db != nil {
        return fs.db, nil
    }
    // ...
    db, err := sql.Open(memoryFTSDriver, dsn)
    if err != nil {
        return nil, err
    }
    db.SetMaxOpenConns(1)
    db.SetMaxIdleConns(1)
    // ... pragmas ...
    fs.db = db
    return fs.db, nil
}
```

**修复方案**: 添加错误路径关闭 + Close 方法

```go
func (fs *memoryFTSStore) openDBLocked() (*sql.DB, error) {
    if fs.db != nil {
        return fs.db, nil
    }
    // ...
    db, err := sql.Open(memoryFTSDriver, dsn)
    if err != nil {
        return nil, err
    }
    db.SetMaxOpenConns(1)
    db.SetMaxIdleConns(1)

    // pragmas (best-effort)
    if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
        db.Close() // 修复: 错误路径关闭连接
        return nil, fmt.Errorf("set WAL mode: %w", err)
    }

    fs.db = db
    return fs.db, nil
}

// Close 关闭 FTS 数据库连接
func (fs *memoryFTSStore) Close() error {
    fs.mu.Lock()
    defer fs.mu.Unlock()
    if fs.db != nil {
        err := fs.db.Close()
        fs.db = nil
        return err
    }
    return nil
}
```

**联动修改**: 在 `MemoryStore` 中添加 `Close()` 方法，转发调用到 `fts.Close()`

---

### 1.6 REQ-SEC-003: 消除 stdout 输出

**文件**: `pkg/tools/shell.go`

**修复** (行 148, 159):

```go
// 修复前
fmt.Printf("Using custom deny patterns: %v\n", execConfig.CustomDenyPatterns)
fmt.Println("Warning: deny patterns are disabled. All commands will be allowed.")

// 修复后
logger.InfoCF("tools/shell", "Using custom deny patterns", map[string]any{
    "patterns": execConfig.CustomDenyPatterns,
})
logger.WarnCF("tools/shell", "Deny patterns disabled, all commands allowed", nil)
```

**需要添加 import**:

```go
import "github.com/xwysyy/X-Claw/pkg/logger"
```

---

### 1.7 REQ-BUG-008: Model Downgrade 竞态修复

**文件**: `pkg/agent/loop.go`

**当前代码** (行 64-65, 372-375):

```go
// 字段定义
modelAutoMu           sync.Mutex
modelAutoDowngradeMap map[string]sessionModelAutoDowngradeState

// 使用处 (多处分散的 Lock/Unlock)
al.modelAutoMu.Lock()
delete(al.modelAutoDowngradeMap, sessionKey)
al.modelAutoMu.Unlock()
```

**修复**: 将相关的读-判断-写操作合并到单个临界区

```go
// 提取辅助方法，确保原子操作
func (al *AgentLoop) getModelDowngrade(sessionKey string) (sessionModelAutoDowngradeState, bool) {
    al.modelAutoMu.Lock()
    defer al.modelAutoMu.Unlock()
    s, ok := al.modelAutoDowngradeMap[sessionKey]
    return s, ok
}

func (al *AgentLoop) setModelDowngrade(sessionKey string, state sessionModelAutoDowngradeState) {
    al.modelAutoMu.Lock()
    defer al.modelAutoMu.Unlock()
    al.modelAutoDowngradeMap[sessionKey] = state
}

func (al *AgentLoop) clearModelDowngrade(sessionKey string) {
    al.modelAutoMu.Lock()
    defer al.modelAutoMu.Unlock()
    delete(al.modelAutoDowngradeMap, sessionKey)
}
```

---

### 1.8 REQ-BUG-009: copyFile 错误语义修复

**文件**: `pkg/agent/run_pipeline_impl.go` (行 1342-1352)

```go
// 修复前
n, copyErr := io.Copy(out, in)
closeErr := out.Close()
if copyErr != nil {
    _ = os.Remove(dstPath)
    return n, copyErr
}
if closeErr != nil {
    _ = os.Remove(dstPath)
    return n, closeErr  // n 可能误导
}

// 修复后
n, copyErr := io.Copy(out, in)
closeErr := out.Close()
if copyErr != nil {
    _ = os.Remove(dstPath)
    return 0, fmt.Errorf("copy: %w", copyErr)
}
if closeErr != nil {
    _ = os.Remove(dstPath)
    return 0, fmt.Errorf("close destination: %w", closeErr)
}
```

---

### Phase 1 验证清单

```bash
# 全量构建
go build ./...

# 全量测试
go test ./... -count=1

# 竞态检测（核心包）
go test -race ./pkg/channels/ ./pkg/session/ ./pkg/agent/ ./pkg/httpapi/ -count=1

# 静态分析
go vet ./...

# 功能验证
./build/x-claw version
./build/x-claw agent -m "hello"
```

---

## Phase 2: 文件拆分

### 2.1 REQ-ARCH-001: 拆分 pkg/config/config.go

**当前结构** (~1200 行, 48+ struct):

根据行号范围，按域拆分:

| 目标文件 | 源行号范围 | 包含的 struct |
|---------|-----------|-------------|
| `config.go` | 1-70 | `FlexibleStringSlice`, `Config` 顶级 struct, `Load/Save` 方法 |
| `config_notify.go` | 72-130 | `NotifyConfig`, `SecurityConfig`, `BreakGlassConfig` |
| `config_agents.go` | 132-337 | `AgentsConfig`, `AgentConfig`, `AgentDefaults`, `AgentCompactionConfig`, `AgentContextPruningConfig`, `AgentBootstrapSnapshotConfig`, `EmbeddingConfig`, `AgentMemoryVectorConfig`, `AgentMemoryHybridConfig`, `SessionModelAutoDowngradeConfig`, `SessionConfig` |
| `config_channels.go` | 340-555 | `ChannelsConfig`, `GroupTriggerConfig`, `TypingConfig`, `PlaceholderConfig`, `WhatsAppConfig`, `TelegramConfig`, `FeishuConfig`, `DiscordConfig`, `MaixCamConfig`, `QQConfig`, `DingTalkConfig`, `SlackConfig`, `LINEConfig`, `OneBotConfig`, `WeComConfig`, `WeComAppConfig`, `WeComAIBotConfig`, `PicoConfig` |
| `config_providers.go` | 556-770 | `HeartbeatConfig`, `OrchestrationConfig`, `AuditConfig`, `AuditSupervisorConfig`, `LimitsConfig`, `AuditLogConfig`, `ProvidersConfig`, `ProviderConfig`, `OpenAIProviderConfig`, `ModelConfig` |
| `config_gateway.go` | 772-807 | `GatewayConfig`, `GatewayInboundQueueConfig`, `GatewayReloadConfig` |
| `config_tools.go` | 809-EOF | `ToolsConfig`, `BraveConfig`, `TavilyConfig`, 及其他工具相关配置 |

**操作步骤**:

```bash
# 1. 创建新文件（仅移动 struct 定义和关联方法）
# 2. 保持 package config 不变
# 3. 保持所有 json tag 不变
# 4. 验证
go build ./pkg/config/...
go test ./pkg/config/... -count=1
```

**关键原则**: 纯粹的文件级拆分，不改变任何 struct 名称、字段、方法签名或 json tag。

---

### 2.2 REQ-ARCH-002: 拆分 pkg/agent/memory.go

**当前结构** (~2900 行):

| 目标文件 | 内容 | 预估行数 |
|---------|------|---------|
| `memory.go` | `MemoryStore` struct (行 43-49) + 所有导出方法 (`NewMemoryStore` 行 76, `ReadLongTerm` 行 112, `WriteLongTerm` 行 120, `ReadToday` 行 132, `AppendToday` 行 142, `GetRecentDailyNotes` 行 176, `GetMemoryContext` 行 200, `OrganizeWriteback` 行 230, `SetVectorSettings` 行 257, `SearchRelevant` 行 271, `GetBySource` 行 299) + 段顺序常量 (行 51-58) + 辅助类型 (`memoryScope`, `memoryReadStack`, `memoryBlockSpec`) | ~600 |
| `memory_vector.go` | `memoryVectorStore` struct + 所有方法 | ~500 |
| `memory_fts.go` | `memoryFTSStore` struct (行 2614-2623) + `openDBLocked` (行 2761) + `Search` + `ensureIndexLocked` + `Close`（新增） | ~500 |
| `memory_embedder.go` | hashed embedder + openai_compat embedder + 嵌入接口 | ~400 |
| `memory_tools.go` | `MemorySearchTool` + `MemoryGetTool` + 工具注册辅助 | ~500 |

**操作步骤**:

```bash
# 1. 确认所有 unexported 类型的引用范围（仅在 pkg/agent 内部）
# 2. 按上表移动代码到新文件
# 3. 确保所有文件 package 声明为 package agent
# 4. 验证
go build ./pkg/agent/...
go test ./pkg/agent/... -count=1
```

---

### 2.3 REQ-ARCH-003: 拆分 pkg/agent/loop.go

**当前结构** (~4194 行):

| 目标文件 | 内容 | 预估行数 |
|---------|------|---------|
| `loop.go` | `AgentLoop` struct (行 45-66) + `New*` 构造函数 + `Config`/`SetConfig` (行 199-252) + `Run`/`Stop` (行 554-851) + `ProcessDirect`/`ProcessSessionMessage`/`ProcessHeartbeat` (行 852-934) | ~900 |
| `loop_commands.go` | `handleCommand` (行 935-1480) → 重构为分发函数 + 各命令独立函数 (`handlePlanCmd`, `handleApproveCmd`, `handleSwitchCmd`, `handleTreeCmd`, `handleResumeCmd` 等) | ~600 |
| `loop_compaction.go` | `maybeSummarize` (行 1615-1675) + `forceCompression` (行 1676-1727) + `summarizeSession` (行 1815-1896) + `compactWithSafeguard` (行 2082-2168) + `safeCompactionContext` | ~600 |
| `loop_token_usage.go` | `tokenUsageStore` struct + `tokenUsageMu` 相关方法 + token 统计逻辑 | ~400 |
| `loop_model_downgrade.go` | `sessionModelAutoDowngradeState` + `modelAutoMu` 相关方法（含 Phase 1 修复的辅助方法） + 模型降级判断逻辑 | ~300 |

---

### 2.4 REQ-ARCH-004: 拆分 pkg/channels/manager.go

**当前结构** (~1762 行):

| 目标文件 | 内容 | 预估行数 |
|---------|------|---------|
| `manager.go` | `Manager` struct (行 96-113) + `New`/`StartAll`/`StopAll` (行 454-515) + `SendMessage` (行 973+) + 核心调度逻辑 | ~500 |
| `manager_placeholder.go` | `SchedulePlaceholder` (行 166-252) + `RecordPlaceholder` (行 121) + `CancelPlaceholder` + `scheduledPlaceholderEntry` (行 72-77) + placeholder janitor | ~350 |
| `manager_typing.go` | `RecordTypingStop` (行 128) + `RecordReactionUndo` (行 135) + `typingEntry` (行 55) + typing indicator 管理 | ~200 |
| `manager_dispatch.go` | `dispatchMessage` (行 775-794) + 重试逻辑 + rate limiting + `channelRateConfig` (行 79-85) | ~400 |
| `interfaces.go` | 全部 10 个接口定义 (`Channel` 行 1026, `MessageLengthProvider` 行 1072, `TypingCapable` 行 1454, `MessageEditor` 行 1460, `ReactionCapable` 行 1467, `PlaceholderCapable` 行 1472, `PlaceholderRecorder` 行 1477, `PlaceholderScheduler` 行 1484, `WebhookHandler` 行 1490, `HealthChecker` 行 1496) | ~150 |

---

### 2.5 REQ-ARCH-005: 拆分 console.go 和 toolcall_executor.go

#### console.go 拆分

| 目标文件 | Handler | 行号 |
|---------|---------|------|
| `console.go` | `NewConsoleHandler`, `ServeHTTP`, `serveAPI`, `servePage`, `writeJSON`(新), `readJSON`(新) + 认证逻辑 | 62-165 |
| `console_status.go` | `handleStatus`(253), `handleState`(288), `handleCron`(297), `handleTokens`(328) | 253-370 |
| `console_sessions.go` | `handleSessions`(371), `handleTraceList`(603) | 371-776 |
| `console_file.go` | `handleFile`(778), `handleTail`(811) | 778-855 |
| `console_stream.go` | `handleStream`(856) — SSE 实现 | 856-1010 |
| `console_notify.go` | `NotifyHandler`(1603-1837) + `ResumeLastTaskHandler`(1838-1978) + `SessionModelHandler`(1979+) | 1603-EOF |

#### toolcall_executor.go 拆分

| 目标文件 | 内容 |
|---------|------|
| `toolcall_executor.go` | `ToolCallExecutionOptions`(33-96), `ExecuteToolCalls`(107-444) 核心流程 |
| `toolcall_policy.go` | Plan Mode 检查, Estop, Tool Policy 决策逻辑 |
| `toolcall_hooks.go` | `ToolHook` 接口, `BuildDefaultToolHooks`(935-952), Before/After hook 逻辑 |
| `toolcall_error.go` | `ToolErrorTemplateOptions`(542-559), `applyToolErrorTemplate`(584-676), hint 构建(678-719) |

---

### Phase 2 验证清单

每个文件拆分后立即验证:

```bash
go build ./...
go test ./... -count=1
go vet ./...
```

**关键原则**: 纯粹的文件级重组，不改变任何公开 API、函数签名或行为。

---

## Phase 3: 代码质量提升

### 3.1 REQ-QUAL-001: 提取魔法数字

**文件**: `pkg/channels/manager.go`

```go
// 在文件顶部添加命名常量
const (
    defaultWorkerQueueSize      = 16              // 行 37 的原值
    typingIndicatorTTL          = 5 * time.Minute // 行 42 的原值
    placeholderTTL              = 10 * time.Minute // 行 43 的原值
    telegramMaxMessageLength    = 4096            // 行 97 的原值
    defaultPlaceholderSendTimeout = 10 * time.Second
    placeholderScheduleTimeout   = 30 * time.Second
)
```

**文件**: `pkg/channels/manager.go` (拆分后为 `manager_dispatch.go`)

```go
// 速率限制配置提升为命名常量
var defaultChannelRates = map[string]float64{
    "telegram": 20,
    "discord":  5,  // 从 1 调整为 5
    "slack":    1,
    "line":     10,
}
```

**文件**: `pkg/agent/loop.go`

```go
const memorySummaryThreshold = 100 // 行 1620 的原值
```

---

### 3.2 REQ-QUAL-002: 消除 Channel 间重复代码

#### 3.2.1 通用 Channel 注册

**新建文件**: `pkg/channels/register.go`

```go
package channels

// RegisterFactory 是各 channel init.go 的通用注册辅助
func RegisterFactory(name string, factory ChannelFactory) {
    channelFactories[name] = factory
}
```

各 channel 的 `init.go` 简化为:

```go
func init() {
    channels.RegisterFactory("telegram", NewTelegramChannel)
}
```

#### 3.2.2 通用媒体发送

**新建文件**: `pkg/channels/media_helpers.go`

```go
package channels

// MediaTypeFromMIME 返回统一的媒体类型分类
func MediaTypeFromMIME(mime string) string {
    switch {
    case strings.HasPrefix(mime, "image/"):
        return "image"
    case strings.HasPrefix(mime, "audio/"):
        return "audio"
    case strings.HasPrefix(mime, "video/"):
        return "video"
    default:
        return "file"
    }
}
```

---

### 3.3 REQ-QUAL-003: 统一错误处理

**全局搜索替换**:

```bash
# 找出所有不当的错误忽略
rg '_ = ' --type go -l --glob '!*_test.go' --glob '!vendor/*'
```

**替换规则**:

| 模式 | 替换 |
|------|------|
| `_ = c.bot.SendChatAction(...)` | `if err := c.bot.SendChatAction(...); err != nil { logger.DebugCF(...) }` |
| `_ = al.bus.PublishOutbound(...)` | `if err := al.bus.PublishOutbound(...); err != nil { logger.DebugCF(...) }` |
| `_ = terminateProcessTree(...)` | `if err := terminateProcessTree(...); err != nil { logger.DebugCF(...) }` |

**error wrapping 统一**:

```bash
# 找出使用 errors.New 的地方（应改为 fmt.Errorf + %w）
rg 'errors\.New\(' --type go -l
```

---

### 3.4 REQ-QUAL-004: 降低 handleCommand 复杂度

**文件**: `pkg/agent/loop_commands.go`（Phase 2 拆分后）

**重构方案**: 命令分发表

```go
type commandHandler func(ctx context.Context, msg bus.InboundMessage, args string) error

var commandDispatch = map[string]commandHandler{
    "/plan":    (*AgentLoop).handlePlanCmd,
    "/approve": (*AgentLoop).handleApproveCmd,
    "/run":     (*AgentLoop).handleApproveCmd, // alias
    "/cancel":  (*AgentLoop).handleCancelCmd,
    "/mode":    (*AgentLoop).handleModeCmd,
    "/switch":  (*AgentLoop).handleSwitchCmd,
    "/tree":    (*AgentLoop).handleTreeCmd,
    "/resume":  (*AgentLoop).handleResumeCmd,
}

func (al *AgentLoop) handleCommand(ctx context.Context, msg bus.InboundMessage, ...) {
    cmd, args := parseCommand(msg.Content)
    if handler, ok := commandDispatch[cmd]; ok {
        if err := handler(al, ctx, msg, args); err != nil {
            logger.ErrorCF("agent", "command failed", map[string]any{"cmd": cmd, "error": err})
        }
        return
    }
    // 未知命令的默认处理
    al.handleUnknownCommand(ctx, msg)
}
```

每个 `handle*Cmd` 函数独立，控制在 30-50 行以内。

---

## Phase 4: 性能优化

### 4.1 REQ-PERF-001: 字符串拼接优化

**文件**: `pkg/agent/run_pipeline_impl.go`

**修改点 1** (行 277-280):

```go
// 修复前
if strings.TrimSpace(userMessage) != "" {
    userMessage += "\n\n" + note
} else {
    userMessage = note
}

// 修复后
var msgBuilder strings.Builder
if strings.TrimSpace(userMessage) != "" {
    msgBuilder.Grow(len(userMessage) + len(note) + 2)
    msgBuilder.WriteString(userMessage)
    msgBuilder.WriteString("\n\n")
    msgBuilder.WriteString(note)
    userMessage = msgBuilder.String()
} else {
    userMessage = note
}
```

**修改点 2** — `notifyLastActiveOnInternalRun` (行 556-560):

```go
// 修复前
notifyText := fmt.Sprintf(
    "✅ Task complete\n\nTask:\n%s\n\nResult:\n%s",
    utils.Truncate(strings.TrimSpace(opts.UserMessage), 240),
    utils.Truncate(strings.TrimSpace(finalContent), 1200),
)

// 修复后（fmt.Sprintf 在这种单次调用场景下开销可接受，保持不变）
// 注: 此处为低频路径（任务完成时），fmt.Sprintf 的开销可忽略
```

**修改点 3** — `WorkingState.FormatForContext` (行 768-809): 已使用 Builder，添加 Grow:

```go
var b strings.Builder
b.Grow(256) // 预分配估算大小
```

---

### 4.2 REQ-PERF-002: 会话恢复优化（与 REQ-RT-002 合并实现）

**文件**: `pkg/agent/run_pipeline_impl.go` (行 935-1074)

**当前**: 正向遍历所有 run 目录

**优化**: 反向扫描（最新优先）+ 终态语义修正（`run.error` 也视为终态）

> **注意**: 本优化与 Phase 1a 的 REQ-RT-002 合并实现。性能优化（反向扫描）和语义修正（终态判定 + 多 workspace）在同一次变更中完成。

```go
func findLastUnfinishedRun(root string) (*unfinishedRun, error) {
    entries, err := os.ReadDir(root)
    if err != nil {
        return nil, err
    }

    // 修复: 从最新目录开始反向扫描
    for i := len(entries) - 1; i >= 0; i-- {
        e := entries[i]
        if !e.IsDir() {
            continue
        }
        eventsPath := filepath.Join(root, e.Name(), "events.jsonl")
        run, err := checkRunFinished(eventsPath)
        if err != nil {
            continue
        }
        if !run.finished {
            return run, nil // 找到第一个未完成的即返回
        }
    }
    return nil, nil
}
```

---

### 4.3 REQ-PERF-003: JSON 序列化优化

**文件**: `pkg/memory/jsonl.go` (行 120)

```go
// 修复前
data, err := json.MarshalIndent(meta, "", "  ")

// 修复后
data, err := json.Marshal(meta)
```

---

### 4.4 REQ-PERF-004: HTTP 客户端复用

**文件**: `pkg/skills/registry.go`

```go
// 在包级别定义共享 client
var sharedHTTPClient = &http.Client{
    Timeout: 15 * time.Second,
    Transport: &http.Transport{
        MaxIdleConns:        20,
        MaxIdleConnsPerHost: 5,
        IdleConnTimeout:     90 * time.Second,
    },
}

// 使用时
func (r *Registry) fetchSkill(ctx context.Context, url string) ([]byte, error) {
    req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
    if err != nil {
        return nil, err
    }
    resp, err := sharedHTTPClient.Do(req) // 替换 &http.Client{...}
    // ...
}
```

---

## Phase 5: 架构改善

### 5.1 REQ-ARCH-006: pkg/ 到 internal/ 迁移

**迁移批次规划**:

| 批次 | 包 | 依赖方 | 风险 |
|------|------|--------|------|
| Batch 1 | `pkg/bus/`, `pkg/state/`, `pkg/identity/` | 内部引用少 | 低 |
| Batch 2 | `pkg/media/`, `pkg/auditlog/`, `pkg/memory/` | 被 agent 引用 | 低 |
| Batch 3 | `pkg/session/`, `pkg/mcp/`, `pkg/httpapi/` | 被 gateway 引用 | 中 |
| Batch 4 | `pkg/channels/`, `pkg/providers/` | 广泛引用 | 中 |

**每批迁移步骤**:

```bash
# 1. 创建目标目录
mkdir -p internal/<pkg_name>

# 2. 移动文件
mv pkg/<pkg_name>/*.go internal/<pkg_name>/

# 3. 更新 import 路径
# 使用 sed 或 IDE 全局替换:
#   github.com/xwysyy/X-Claw/pkg/<pkg_name>
#   → github.com/xwysyy/X-Claw/internal/<pkg_name>
find . -name '*.go' -not -path './.cache/*' -exec \
    sed -i 's|github.com/xwysyy/X-Claw/pkg/<pkg_name>|github.com/xwysyy/X-Claw/internal/<pkg_name>|g' {} +

# 4. 验证
go build ./...
go test ./... -count=1
```

**反向依赖修复** (pkg/ → internal/core/):

```go
// 当前问题: pkg/agent/run_pipeline_impl.go:4
import "github.com/xwysyy/X-Claw/internal/core/events"

// 方案 A: 将 events 中被 pkg 使用的类型提升到 pkg/core/events
// 方案 B: 在 pkg/agent 中定义本地事件常量（消除跨边界依赖）
```

**推荐方案 B**（最小变更）:

```go
// pkg/agent/events.go
package agent

// 事件类型常量（与 internal/core/events 保持同步）
const (
    EventRunStart    = "run_start"
    EventRunEnd      = "run_end"
    EventToolCall    = "tool_call"
    // ...
)
```

---

## Phase 6: 测试补充

### 6.1 REQ-TEST-001: memory 模块测试

**新建文件**: `pkg/agent/memory_test.go`

```go
package agent

import (
    "os"
    "path/filepath"
    "testing"
)

func TestMemoryStore_ReadWriteLongTerm(t *testing.T) {
    dir := t.TempDir()
    ms := NewMemoryStoreAt(dir)

    // 写入
    content := "# Test Memory\n\n## Profile\nTest user"
    if err := ms.WriteLongTerm(content); err != nil {
        t.Fatalf("WriteLongTerm: %v", err)
    }

    // 读取
    got, err := ms.ReadLongTerm()
    if err != nil {
        t.Fatalf("ReadLongTerm: %v", err)
    }
    if got != content {
        t.Errorf("content mismatch: got %q, want %q", got, content)
    }
}

func TestMemoryStore_DailyNotes(t *testing.T) {
    dir := t.TempDir()
    ms := NewMemoryStoreAt(dir)

    if err := ms.AppendToday("note 1"); err != nil {
        t.Fatalf("AppendToday: %v", err)
    }
    if err := ms.AppendToday("note 2"); err != nil {
        t.Fatalf("AppendToday: %v", err)
    }

    content, err := ms.ReadToday()
    if err != nil {
        t.Fatalf("ReadToday: %v", err)
    }
    if !strings.Contains(content, "note 1") || !strings.Contains(content, "note 2") {
        t.Errorf("expected both notes, got: %s", content)
    }
}

func TestMemoryStore_SearchRelevant(t *testing.T) {
    dir := t.TempDir()
    ms := NewMemoryStoreAt(dir)

    // 写入测试数据
    ms.WriteLongTerm("# Memory\n\n## Profile\nGo developer\n\n## Active Goals\nLearn Rust")

    // 搜索
    results, err := ms.SearchRelevant(context.Background(), "developer", 5, MemoryVectorSettings{})
    if err != nil {
        t.Fatalf("SearchRelevant: %v", err)
    }
    if len(results) == 0 {
        t.Error("expected at least 1 result")
    }
}
```

### 6.2 REQ-TEST-002: Session GC 测试

**新建文件/追加到**: `pkg/session/manager_test.go`

```go
func TestSessionManager_GC_MaxSessions(t *testing.T) {
    dir := t.TempDir()
    sm := NewSessionManager(dir)
    sm.maxSessions = 3

    // 创建 5 个会话
    for i := 0; i < 5; i++ {
        key := fmt.Sprintf("session_%d", i)
        sm.GetOrCreate(key)
        time.Sleep(10 * time.Millisecond) // 确保 UpdatedAt 不同
    }

    // 验证内存中最多 3 个
    sm.mu.RLock()
    count := len(sm.sessions)
    sm.mu.RUnlock()

    if count > 3 {
        t.Errorf("expected max 3 sessions in memory, got %d", count)
    }
}

func TestSessionManager_GC_TTL(t *testing.T) {
    dir := t.TempDir()
    sm := NewSessionManager(dir)
    sm.ttl = 50 * time.Millisecond

    sm.GetOrCreate("old_session")
    time.Sleep(100 * time.Millisecond)

    evicted := sm.evictExpired()
    if evicted != 1 {
        t.Errorf("expected 1 evicted, got %d", evicted)
    }
}
```

---

## 附录: 工具与命令速查

### 构建与测试

```bash
# 完整构建
make build

# 完整测试
make test

# 带竞态检测的测试
go test -race ./... -count=1

# 覆盖率
make cover

# 静态分析
make vet
make lint

# 快速检查（vet + fmt + test）
make check
```

### 文件拆分验证

```bash
# 单包验证
go build ./pkg/config/...
go test ./pkg/config/... -count=1

# 全量验证
go build ./...
go test ./... -count=1
go vet ./...
```

### 功能回归测试

```bash
# 本地构建
make build

# Version
./build/x-claw version

# Agent 单轮
./build/x-claw agent -m "hello"

# Gateway 启动
./build/x-claw gateway &
sleep 2
curl -sS http://127.0.0.1:18790/health
kill %1
```

### Import 路径全局替换

```bash
# 迁移 pkg/bus -> internal/bus
find . -name '*.go' -not -path './.cache/*' -not -path './vendor/*' \
    -exec sed -i 's|"github.com/xwysyy/X-Claw/pkg/bus"|"github.com/xwysyy/X-Claw/internal/bus"|g' {} +
```

---

> **实现约束**:
> 1. 每个 Phase 独立提交，commit message 格式: `refactor: <phase> <简述>`
> 2. 不在同一 commit 中混合 Bug 修复和重构
> 3. 每次文件拆分只做文件移动，不改变逻辑
> 4. 所有性能优化需在高频路径上做，低频路径保持可读性优先
