# PicoClaw Agent 开发优化指南

> 基于项目代码深度审计，结合 Agent 工程实践经验，针对 PicoClaw 提出的系统性改进方案。
> 每一条建议都对应到具体的代码位置和可落地的实现路径。

---

## 目录

1. [思考过程设计（Agent 行为编排）](#1-思考过程设计agent-行为编排)
2. [工具系统优化](#2-工具系统优化)
3. [System Prompt 工程](#3-system-prompt-工程)
4. [记忆与上下文管理](#4-记忆与上下文管理)
5. [评估与可观测性](#5-评估与可观测性)
6. [多 Agent 协作优化](#6-多-agent-协作优化)
7. [成本控制策略](#7-成本控制策略)
8. [安全加固](#8-安全加固)
9. [实施优先级与路线图](#9-实施优先级与路线图)

---

## 1. 思考过程设计（Agent 行为编排）

### 当前状态

PicoClaw 的核心循环在 `pkg/agent/loop.go:runLLMIteration` 中实现，是一个标准的 ReAct 循环：

```
LLM 调用 → 解析工具调用 → 执行工具 → 结果回注 → 再次调用 LLM → ... → 直到无工具调用
```

这个循环缺少**显式的决策阶段**——Agent 没有被引导去"先想清楚要做什么"，而是完全依赖 LLM 的隐式推理。

### 问题

- Agent 在复杂任务中容易"跑偏"——没有明确的任务分解步骤，直接就开始调用工具。
- 没有"停下来反思"的机制——当中间步骤出错时，Agent 不会主动评估前进方向是否还正确。
- `MaxIterations`（默认 20）是唯一的终止守卫，但没有"目标达成检测"。

### 改进方案

#### 1.1 引入 Plan-Execute-Reflect 三阶段循环

在 `pkg/agent/loop.go` 的 `runLLMIteration` 中，将单一的 ReAct 循环改为三阶段：

```go
// pkg/agent/loop.go — runLLMIteration 内

for iteration < agent.MaxIterations {
    iteration++

    // === Phase 1: Plan (每 N 步或首次) ===
    if iteration == 1 || iteration%reflectInterval == 0 {
        planPrompt := buildPlanPrompt(opts.UserMessage, currentState)
        // 让 LLM 输出结构化计划，不调用工具
        planResponse := callLLMWithoutTools(ctx, agent, planPrompt)
        currentPlan = parsePlan(planResponse)
    }

    // === Phase 2: Execute (现有逻辑) ===
    response, err := callLLM()  // 带工具的调用
    // ...工具执行...

    // === Phase 3: Reflect (关键步骤后) ===
    if shouldReflect(iteration, lastToolResult) {
        reflectPrompt := buildReflectPrompt(currentPlan, executionTrace)
        reflectResponse := callLLMWithoutTools(ctx, agent, reflectPrompt)
        if reflectResponse.ShouldAdjustPlan {
            currentPlan = reflectResponse.UpdatedPlan
        }
        if reflectResponse.ShouldStop {
            break
        }
    }
}
```

**关键点**：Reflect 阶段不需要每步都做（太贵），可以在以下条件触发：
- 工具返回错误时
- 累计执行了 5 步时
- 即将达到 MaxIterations 的 80% 时

#### 1.2 实现结构化的任务状态（Working Memory）

`TaskLedger`（`pkg/tools/task_ledger.go`）已经有了很好的基础，但它目前仅用于审计跟踪。可以将其扩展为 Agent 的"工作记忆"：

```go
// pkg/agent/working_state.go — 新文件

type WorkingState struct {
    OriginalTask   string            `json:"original_task"`
    CurrentPlan    []PlanStep        `json:"current_plan"`
    CompletedSteps []CompletedStep   `json:"completed_steps"`
    CollectedData  map[string]string `json:"collected_data"`
    OpenQuestions  []string          `json:"open_questions"`
    NextAction     string            `json:"next_action"`
}

type PlanStep struct {
    ID          string `json:"id"`
    Description string `json:"description"`
    Status      string `json:"status"` // pending, running, done, failed
    ToolNeeded  string `json:"tool_needed,omitempty"`
}
```

在每轮 LLM 调用前，将 `WorkingState` 的 JSON 注入到 messages 中。这比让 LLM 从长对话中自行提取信息可靠得多——**不要让 LLM 做它不擅长的事**。

---

## 2. 工具系统优化

### 当前状态

PicoClaw 的工具系统设计已经相当成熟：
- `Tool` 接口（`pkg/tools/base.go`）清晰、最小化
- `ToolResult` 类型（`pkg/tools/result.go`）区分了 ForLLM/ForUser/Silent/Async 等语义
- 有并行执行支持、安全策略分层

但仍有可改进之处。

### 2.1 工具描述需要大幅强化

**现状问题**：许多工具的 `Description()` 过于简洁。例如：

```go
// pkg/tools/shell.go:126
func (t *ExecTool) Description() string {
    return "Execute a shell command and return its output. Use with caution."
}
```

Agent 不知道：这个工具能执行什么命令？有什么限制？输入格式是什么？出错了会返回什么？

**改进方向**：

```go
func (t *ExecTool) Description() string {
    return `Execute a shell command in the workspace directory and return stdout/stderr.
Input: command (string, required) — the shell command to run.
Output: stdout content, with stderr appended if present. Includes exit code on failure.
Constraints:
- Dangerous commands (rm -rf, sudo, etc.) are blocked by safety guard.
- Commands are restricted to the workspace directory.
- Default timeout: 60 seconds. Use timeout_seconds to override.
- Use background=true for long-running commands.
When to use: file operations, system queries, build/compile tasks.
When NOT to use: reading file content (use read_file instead).`
}
```

工具描述中**省的每一个字，都会变成 Agent 运行时犯的错**。以下工具同样需要强化描述：

| 工具 | 文件位置 | 当前描述问题 |
|------|---------|-------------|
| `exec` | `pkg/tools/shell.go:126` | 缺少输入输出格式、限制条件、使用场景 |
| `spawn` | `pkg/tools/spawn.go:34` | 没有说明异步行为、结果如何获取 |
| `subagent` | `pkg/tools/subagent.go:597` | 与 spawn 的区别不明确 |
| `web_search` | `pkg/tools/web.go` | 缺少搜索引擎后端说明、结果格式 |
| `message` | `pkg/tools/message.go` | 缺少频道路由说明 |

### 2.2 工具错误返回需要更具指导性

**现状**：`ErrorResult` 返回的错误信息是给人看的，不是给 Agent 看的。

```go
// pkg/tools/shell.go:186
return ErrorResult("Command blocked by safety guard (dangerous pattern detected)")
```

Agent 收到这个错误后不知道应该怎么办。

**改进**：错误信息应包含"下一步建议"：

```go
return ErrorResult(
    "Command blocked by safety guard: detected dangerous pattern 'rm -rf'. " +
    "This command is not allowed for security reasons. " +
    "Suggestion: Use the edit_file or write_file tool to modify files, " +
    "or use a more targeted command like 'rm specific-file.txt'.",
)
```

可以在 `pkg/tools/result.go` 中新增一个带建议的错误构造器：

```go
// pkg/tools/result.go

func ErrorWithSuggestion(message, suggestion string) *ToolResult {
    return &ToolResult{
        ForLLM:  fmt.Sprintf("%s\nSuggestion: %s", message, suggestion),
        IsError: true,
    }
}
```

### 2.3 工具粒度检查

当前工具数量约 15-20 个（取决于配置），处于合理范围。但有两个可以考虑合并：

- `spawn` 和 `subagent` 功能高度重叠（异步 vs 同步版），对 Agent 来说容易混淆。建议合并为一个工具，通过 `async` 参数切换行为。
- `read_file`、`write_file`、`edit_file`、`append_file`、`list_dir` — 五个文件工具。如果 Agent 经常在选择上犯错，可以考虑将 `write_file` 和 `append_file` 合并（通过 `mode` 参数区分）。

---

## 3. System Prompt 工程

### 当前状态

System prompt 在 `pkg/agent/context.go:getIdentity()` 中构建，内容包括：
- 身份声明
- 工作区路径
- 工具列表
- 基本规则（4 条）
- 引导文件（AGENT.md, SOUL.md, USER.md, IDENTITY.md）
- Skills 摘要
- Memory 上下文

### 3.1 缺少明确的"做事流程"

**当前的规则**（`context.go:168-174`）太宽泛：

```
1. ALWAYS use tools
2. Be helpful and accurate
3. Memory — update MEMORY.md
4. Context summaries — approximate references only
```

这相当于只告诉新员工"好好干活"，没有告诉他"先做什么、再做什么"。

**改进**：在 `getIdentity()` 中增加显式的决策流程：

```go
func (cb *ContextBuilder) getIdentity() string {
    return fmt.Sprintf(`# picoclaw 🦞

You are picoclaw, a helpful AI assistant.

## Decision Process

When you receive a task:
1. **Understand** — Identify what the user actually needs. If ambiguous, ask for clarification.
2. **Plan** — Determine which tools and steps are needed. For complex tasks, outline your approach.
3. **Execute** — Use tools one step at a time. Check each result before proceeding.
4. **Verify** — Confirm the result matches the user's intent. If not, adjust and retry.
5. **Respond** — Provide a concise summary of what was done and the outcome.

## When to Stop

- If you cannot complete the task, explain WHY clearly. Do NOT fabricate results.
- If a tool fails, analyze the error and try an alternative approach.
- If you've used 3+ tools without progress, reassess your plan.
- NEVER repeat the same failed tool call with identical arguments.

...（其余内容不变）`)
}
```

### 3.2 给 Agent "说不"的权限

当前 prompt 没有明确允许 Agent 拒绝任务或表达不确定性。Agent 默认倾向于编造看似合理的回答。

在 `workspace/AGENT.md` 中应增加：

```markdown
## Honesty Policy

- If you don't know the answer, say "I'm not sure about this" rather than guessing.
- If a task is beyond your capabilities, explain what you can and cannot do.
- If tool results are ambiguous, present the raw data and let the user decide.
- Confidence markers: Use "I believe", "Based on...", "I'm uncertain" appropriately.
```

### 3.3 动态上下文注入优化

`buildDynamicContext`（`context.go:414`）目前仅注入时间和运行时信息。可以增加：

```go
func (cb *ContextBuilder) buildDynamicContext(channel, chatID string) string {
    // ...现有内容...

    // 注入当前活跃任务状态（来自 TaskLedger）
    if activeTasks := cb.getActiveTasksSummary(); activeTasks != "" {
        fmt.Fprintf(&sb, "\n\n## Active Background Tasks\n%s", activeTasks)
    }

    // 注入最近工具使用统计（帮助 Agent 意识到自己的行为模式）
    if recentToolUsage := cb.getRecentToolUsage(); recentToolUsage != "" {
        fmt.Fprintf(&sb, "\n\n## Recent Tool Usage\n%s", recentToolUsage)
    }

    return sb.String()
}
```

---

## 4. 记忆与上下文管理

### 当前状态

PicoClaw 的记忆系统已经相当完善，包含三层：

| 层级 | 实现 | 文件 |
|------|------|------|
| 短期记忆 | Session history（完整对话） | `pkg/session/manager.go` |
| 工作记忆 | Context summary（压缩摘要） | `pkg/agent/compaction.go` |
| 长期记忆 | MEMORY.md + Daily notes + 向量检索 | `pkg/agent/memory.go`, `memory_vector.go` |

这三层设计是正确的，但实现上有可以优化的地方。

### 4.1 Compaction 策略优化

当前的 compaction（`compaction.go:99-184`）使用 `safeguard` 模式，在历史 token 超过 `MaxHistoryShare`（默认 50%）时触发。压缩流程：

1. 保留最近 N 条消息
2. 将较旧的消息发送给 LLM 生成摘要
3. 用摘要替换旧消息

**问题**：摘要生成的 prompt（`compaction.go:236-250`）要求的输出结构过于自由：

```go
sb.WriteString("Summarize this conversation segment for future continuity.\n")
sb.WriteString("Use concise markdown with sections: Intent, Decisions, Tool Results, Pending Actions, Constraints.\n")
```

这没有约束 LLM 输出的格式和长度，导致摘要质量不稳定。

**改进**：使用更结构化的摘要 prompt：

```go
sb.WriteString("Summarize this conversation for future continuity.\n")
sb.WriteString("Output EXACTLY in this format (one line per section):\n")
sb.WriteString("INTENT: <what the user wants to achieve, 1 sentence>\n")
sb.WriteString("DECISIONS: <key decisions made, bullet points>\n")
sb.WriteString("TOOL_RESULTS: <important tool outputs with key data, bullet points>\n")
sb.WriteString("PENDING: <what still needs to be done, bullet points>\n")
sb.WriteString("CONSTRAINTS: <any limitations or requirements discovered, bullet points>\n")
sb.WriteString("Keep total output under 300 words. Prioritize actionable information.\n")
```

### 4.2 Memory Flush 质量提升

`flushMemorySnapshot`（`compaction.go:48-97`）从对话中提取持久记忆。当前只取最近 12 条消息，可能遗漏重要信息。

**改进方向**：

```go
// 不仅取最近的消息，也考虑包含关键决策的消息
func (al *AgentLoop) flushMemorySnapshot(ctx context.Context, agent *AgentInstance, sessionKey string) error {
    history := agent.Sessions.GetHistory(sessionKey)

    // 策略：取最近 8 条 + 包含工具调用的消息（这些通常包含重要操作）
    recent := selectRecentAndSignificant(history, 12)

    // ...其余逻辑不变
}

func selectRecentAndSignificant(history []providers.Message, maxCount int) []providers.Message {
    // 最近的 8 条
    recentCount := 8
    if recentCount > len(history) {
        recentCount = len(history)
    }

    result := history[len(history)-recentCount:]

    // 从更早的历史中，挑选包含工具调用结果的关键消息
    remaining := maxCount - recentCount
    for i := len(history) - recentCount - 1; i >= 0 && remaining > 0; i-- {
        if history[i].Role == "assistant" && len(history[i].ToolCalls) > 0 {
            result = append([]providers.Message{history[i]}, result...)
            remaining--
        }
    }

    return result
}
```

### 4.3 向量记忆检索优化

`SearchRelevant`（`memory.go:218`）在每次构建消息时都会被调用（`context.go:474`）。对于延迟敏感的场景：

- 可以增加缓存：对相同 query（或语义相近的 query）缓存结果
- 可以做异步预加载：在收到用户消息时立即触发检索，在构建 messages 时直接使用结果

---

## 5. 评估与可观测性

### 当前状态

PicoClaw 有一个审计系统（`pkg/agent/audit.go`），功能包括：
- 定时检查 TaskLedger 中的任务状态
- 检测超时、空结果、无 evidence 的任务
- 可选的 Supervisor 模型审计
- 自动修复（重试失败任务）

日志系统（`pkg/logger/logger.go`）使用结构化 JSON 日志。

### 5.1 增加工具调用链路追踪

当前 `ToolExecutionTrace`（`pkg/tools/toolloop.go:41-49`）只记录了基本信息。需要增加"为什么调用这个工具"的上下文：

```go
type ToolExecutionTrace struct {
    Iteration    int
    ToolName     string
    Arguments    map[string]any
    Result       string
    IsError      bool
    DurationMS   int64
    ToolCallID   string
    // 新增字段
    LLMReasoning string // LLM 在选择这个工具时的推理文本
    PrecedingTools []string // 之前调用过的工具序列
}
```

在 `toolloop.go` 的循环中，可以从 `response.Content`（LLM 返回的文本部分）中提取推理信息：

```go
// toolloop.go:95-103 之后
if len(response.ToolCalls) > 0 {
    // LLM 返回工具调用的同时可能附带了思考过程
    reasoning := strings.TrimSpace(response.Content)
    // ... 在 trace 中记录 reasoning
}
```

### 5.2 建立失败模式分类

参考文章的经验，不同的失败原因需要不同的修复策略。在审计系统中增加失败模式分类：

```go
// pkg/agent/audit.go — 新增失败模式常量

const (
    FailureModeTool      = "tool_misuse"      // 工具选择或参数错误 → 改工具描述
    FailureModeReasoning = "reasoning_break"   // 推理链断裂 → 改 prompt/加 few-shot
    FailureModeHalluc    = "hallucination"     // 幻觉 → 加事实校验步骤
    FailureModeContext   = "context_loss"      // 上下文丢失关键信息 → 改记忆管理
    FailureModeLoop      = "infinite_loop"     // 重复调用相同工具 → 加循环检测
    FailureModeTimeout   = "timeout"           // 超时 → 加步数预估
)
```

在 `RunTaskAudit` 中，通过分析 `Evidence` 中的工具调用模式来自动分类：

```go
func classifyFailureMode(entry TaskLedgerEntry) string {
    evidence := entry.Evidence

    // 检测无限循环：连续 3+ 次相同工具+相同参数
    if hasRepeatedToolCalls(evidence, 3) {
        return FailureModeLoop
    }

    // 检测工具误用：工具返回错误率 > 50%
    errorRate := calculateErrorRate(evidence)
    if errorRate > 0.5 {
        return FailureModeTool
    }

    // 检测推理断裂：长时间无工具调用（LLM 在空转）
    if hasLongIdleGaps(evidence) {
        return FailureModeReasoning
    }

    return "unknown"
}
```

### 5.3 增加循环检测机制

在 `runLLMIteration`（`loop.go:672`）中增加重复行为检测：

```go
// loop.go — runLLMIteration 内

type toolCallSignature struct {
    Name string
    Args string // JSON 序列化的参数
}

recentCalls := make([]toolCallSignature, 0, 10)

for iteration < agent.MaxIterations {
    // ...LLM 调用...

    // 检测重复工具调用
    for _, tc := range normalizedToolCalls {
        sig := toolCallSignature{
            Name: tc.Name,
            Args: serializeArgs(tc.Arguments),
        }

        repeatCount := countRecentRepeats(recentCalls, sig, 3)
        if repeatCount >= 2 {
            // 注入干预提示
            messages = append(messages, providers.Message{
                Role: "user",
                Content: fmt.Sprintf(
                    "[System notice: You have called '%s' with the same arguments %d times. "+
                    "This appears to be a loop. Please try a different approach or explain why you're stuck.]",
                    tc.Name, repeatCount+1,
                ),
            })
            break // 跳过执行，让 LLM 重新思考
        }
        recentCalls = append(recentCalls, sig)
    }
}
```

---

## 6. 多 Agent 协作优化

### 当前状态

PicoClaw 已经有了完整的多 Agent 基础设施：
- `AgentRegistry`（`pkg/agent/registry.go`）管理多个 Agent 实例
- `SubagentManager`（`pkg/tools/subagent.go`）处理子 Agent 的生命周期
- `SpawnTool`（`pkg/tools/spawn.go`）和 `SubagentTool` 提供异步/同步两种调度方式
- 有 allowlist 控制、深度限制、并发限制

### 6.1 子 Agent 的 System Prompt 太弱

当前子 Agent 的 prompt（`subagent.go:289-291`）过于简单：

```go
systemPrompt := `You are a subagent. Complete the given task independently and report the result.
You have access to tools - use them as needed to complete your task.
After completing the task, provide a clear summary of what was done.`
```

这导致子 Agent 不知道自己的上下文、约束和期望输出格式。

**改进**：

```go
systemPrompt := fmt.Sprintf(`You are a subagent working under the picoclaw system.

## Your Task
Complete the given task independently and report the result.

## Guidelines
1. Use available tools as needed. Check tool results before proceeding.
2. If a tool fails, try an alternative approach.
3. If you cannot complete the task, explain what went wrong clearly.
4. Do NOT fabricate results. Only report what tools actually returned.
5. Keep your final response concise and actionable.

## Output Format
Provide a clear summary with:
- What was done (actions taken)
- What was found (key results/data)
- Any issues encountered

## Workspace
Working directory: %s
`, sm.workspace)
```

### 6.2 子 Agent 结果传递的信息损耗

当前子 Agent 完成后，结果通过 `bus.InboundMessage` 传回（`subagent.go:523-529`），格式为：

```
Task 'label' completed.

Result:
<actual content>
```

父 Agent 收到后会通过 `processSystemMessage`（`loop.go:534`）再次走一遍完整的 LLM 调用来"理解"这个结果。这导致：
- 额外的 LLM 调用成本
- 结果可能被 LLM 重新解读，产生信息损耗

**改进方向**：对于同步的 `SubagentTool`，结果已经直接返回在 `ToolResult.ForLLM` 中，不经过 bus。可以考虑让异步 `SpawnTool` 也支持结构化结果注入，而不是走 system message 路径。

### 6.3 先把单 Agent 做好

正如文章所说：**单 Agent 都没做好之前不建议上多 Agent——复杂度是指数级上升的。**

PicoClaw 的多 Agent 基础设施已经搭好，但建议当前阶段优先：
1. 强化默认 Agent 的 prompt 和决策能力
2. 确保单 Agent 能稳定处理 15+ 步的复杂任务
3. 然后再通过 Subagent 做任务分发

---

## 7. 成本控制策略

### 7.1 调试阶段使用小模型

PicoClaw 已经支持 fallback chain（`pkg/providers/fallback.go`）和 model_list 配置。可以进一步增加**按场景路由模型**的能力：

```json
{
  "agents": {
    "defaults": {
      "model_name": "claude-sonnet-4-20250514",
      "model_routing": {
        "compaction": "gpt-4o-mini",
        "memory_flush": "gpt-4o-mini",
        "audit_supervisor": "deepseek-chat",
        "subagent_default": "gpt-4o-mini"
      }
    }
  }
}
```

在代码中，compaction（`compaction.go:252`）和 memory flush（`compaction.go:78`）已经各自调用 `agent.Provider.Chat`，可以很容易地替换为小模型：

```go
// compaction.go:252 — 使用专用的 compaction model
model := agent.Model
if agent.CompactionModel != "" {
    model = agent.CompactionModel
}
resp, err := agent.Provider.Chat(ctx, messages, nil, model, options)
```

### 7.2 Token 使用监控

当前 `estimateTokens`（`loop.go` 中）使用 `chars * 2 / 5` 的粗略估算。可以在每次 LLM 调用后，记录实际的 token 使用量（如果 provider 返回了 usage 信息）：

```go
// loop.go — LLM 调用后
if response.Usage != nil {
    logger.InfoCF("agent", "Token usage", map[string]any{
        "prompt_tokens":     response.Usage.PromptTokens,
        "completion_tokens": response.Usage.CompletionTokens,
        "total_tokens":      response.Usage.TotalTokens,
        "model":             agent.Model,
        "iteration":         iteration,
        "session_key":       opts.SessionKey,
    })
}
```

这些数据可以用于：
- 按 session 统计成本
- 发现 token 使用异常高的模式
- 为 compaction 触发提供更精确的依据

### 7.3 缓存 System Prompt

`BuildSystemPromptWithCache`（`context.go:240`）已经实现了静态 prompt 缓存。同时，`SystemParts` 使用了 `CacheControl: ephemeral`（`context.go:461`），支持 Anthropic 的 prompt caching。

确保所有 provider 都正确利用了缓存机制。对于 OpenAI 兼容的 provider，检查是否支持了 prefix caching。

---

## 8. 安全加固

### 8.1 命令安全守卫优化

`ExecTool.guardCommand`（`shell.go:531-588`）使用正则拒绝危险命令。这个方案有效但存在绕过风险（正则天然难以覆盖所有变体）。

**增强方向**：增加一个"allow-list"模式，在高安全场景下只允许预定义的命令模板：

```go
// 安全级别配置
type ExecSecurityLevel string

const (
    ExecSecurityDenyList  ExecSecurityLevel = "deny_list"  // 当前行为
    ExecSecurityAllowList ExecSecurityLevel = "allow_list" // 白名单模式
    ExecSecurityReadOnly  ExecSecurityLevel = "read_only"  // 只读命令
)
```

### 8.2 子 Agent 权限隔离

当前子 Agent 继承了父 Agent 的所有工具（`subagent.go:178`：`subagentManager.SetTools(agent.Tools)`）。这意味着子 Agent 也能执行 shell 命令、发送消息等。

可以为子 Agent 提供**工具白名单**机制：

```go
type SubagentsConfig struct {
    AllowAgents   []string          `json:"allow_agents,omitempty"`
    Model         *AgentModelConfig `json:"model,omitempty"`
    AllowedTools  []string          `json:"allowed_tools,omitempty"` // 新增
    DeniedTools   []string          `json:"denied_tools,omitempty"` // 新增
}
```

---

## 9. 实施优先级与路线图

按**影响/成本**比排序，建议分三个阶段实施：

### Phase 1：快速见效（1-2 周）

| 优先级 | 改进项 | 影响 | 工作量 | 涉及文件 |
|--------|--------|------|--------|---------|
| P0 | 强化工具描述 | 高 | 小 | `pkg/tools/shell.go`, `spawn.go`, `subagent.go`, `web.go`, `message.go` |
| P0 | 改进错误返回信息 | 高 | 小 | `pkg/tools/result.go`, 各工具文件 |
| P0 | 优化 System Prompt 决策流程 | 高 | 小 | `pkg/agent/context.go`, `workspace/AGENT.md` |
| P1 | 增加循环检测 | 中 | 小 | `pkg/agent/loop.go` |
| P1 | 改进 compaction 摘要 prompt | 中 | 小 | `pkg/agent/compaction.go` |

### Phase 2：架构增强（2-4 周）

| 优先级 | 改进项 | 影响 | 工作量 | 涉及文件 |
|--------|--------|------|--------|---------|
| P1 | 实现 WorkingState 结构化状态 | 高 | 中 | 新文件 `pkg/agent/working_state.go` |
| P1 | 增加失败模式分类 | 中 | 中 | `pkg/agent/audit.go` |
| P2 | 按场景路由模型 | 中 | 中 | `pkg/agent/instance.go`, `compaction.go`, `config/config.go` |
| P2 | Token 使用监控与统计 | 中 | 小 | `pkg/agent/loop.go`, `pkg/logger/` |
| P2 | 增加工具调用链路追踪 | 中 | 中 | `pkg/tools/toolloop.go` |

### Phase 3：高级特性（4-8 周）

| 优先级 | 改进项 | 影响 | 工作量 | 涉及文件 |
|--------|--------|------|--------|---------|
| P2 | Plan-Execute-Reflect 三阶段循环 | 高 | 大 | `pkg/agent/loop.go` |
| P2 | 子 Agent 权限隔离 | 中 | 中 | `pkg/tools/subagent.go`, `config/config.go` |
| P3 | 自我纠错（Reflect 机制） | 中 | 大 | `pkg/agent/loop.go` |
| P3 | 向量记忆检索缓存与预加载 | 低 | 中 | `pkg/agent/memory_vector.go` |

---

## 附：与文章核心观点的对应关系

| 文章观点 | PicoClaw 现状 | 本文建议 |
|---------|--------------|---------|
| "做Agent的第一步不是写代码，是设计思考过程" | 有 ReAct 循环但缺显式决策设计 | §1 Plan-Execute-Reflect |
| "工具描述比工具实现更重要" | 描述过于简洁 | §2.1 强化工具描述 |
| "工具要有明确的错误返回" | 错误信息缺少行动建议 | §2.2 ErrorWithSuggestion |
| "System Prompt要明确定义做事流程" | 规则过于宽泛 | §3.1 决策流程 |
| "给Agent说不的权限" | 未显式授权 | §3.2 Honesty Policy |
| "分层记忆" | ✅ 三层已实现 | §4 优化各层质量 |
| "结构化的state对象" | TaskLedger 偏审计，缺工作状态 | §1.2 WorkingState |
| "评估——定义什么叫成功" | 有审计但缺失败模式分析 | §5.2 失败模式分类 |
| "日志要详细" | ✅ 结构化 JSON 日志 | §5.1 增加推理上下文 |
| "人类兜底机制" | 有 MaxIterations 和安全守卫 | §8 进一步强化 |
| "调试阶段用小模型" | 支持 fallback 但缺按场景路由 | §7.1 模型路由 |
| "给Agent设置步数上限" | ✅ MaxIterations=20 | §5.3 增加循环检测 |
| "先让它在小数据上跑通" | 无显式的测试用例集 | §5 建立测试集 |
| "先把单Agent做好" | 多Agent基础设施已有 | §6.3 优先强化单Agent |

---

> "做Agent不像写普通代码，你面对的不是一台机器，你面对的是一个大部分时候很聪明、偶尔很蠢的东西。" —— 这种心态在 PicoClaw 的开发中同样适用。每一个改进都是在缩小"偶尔很蠢"的概率空间。
