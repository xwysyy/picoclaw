# PicoClaw Roadmap（集百家之长 / 差异化特性）

这是一份**可持续维护**的路线图：目标不是“功能堆满”，而是把 PicoClaw 打造成在 **pico-scale（轻量、随处部署）** 前提下，具备 **可靠性 / 可解释性 / 可复现性 / 生态可扩展性** 的个人 AI 助手。

本路线图会持续参考 `ref/` 里已 clone 的同类项目与组件实现（Go 优先，其次迁移性强的 Python/TS 项目）。

> 约束：`ref/` 不进 git（已在 `.gitignore`），因此本文件是唯一“在仓库根目录可追踪”的监督文档。

---

## 0) 监督 TODO：外部项目逐个过一遍（禁止一次性打完）

规则：
- 每次**只勾选本次确实写完分析**的项目（写完 = 本文件中该项目小节内容完整）。
- 勾选时必须写上日期（YYYY-MM-DD）。
- 其余项目保持未勾选，直到真的过完。

当前待分析的 `ref/` 项目清单（按“对 PicoClaw 可落地价值”排序）：

- [x] `ref/nagobot`（Go，轻量 personal assistant）— 2026-03-01
- [x] `ref/clawlet`（Go，SQLite + sqlite-vec 混合记忆）— 2026-03-01
- [x] `ref/nanobot`（Python，~4k 核心 + MCP）— 2026-03-01
- [x] `ref/blades`（Go，tool schema + middleware + flow）— 2026-03-01
- [x] `ref/trpc-agent-go`（Go，LangGraph-like + checkpoint + MCP）— 2026-03-01
- [x] `ref/openai-swarm`（Python，极简 multi-agent handoff）— 2026-03-01
- [x] `ref/langgraph`（Python，durable execution / checkpoint / interrupt）— 2026-03-01
- [x] `ref/mem0`（Python，scope 化记忆 API：user/agent/run）— 2026-03-01
- [x] `ref/letta`（Python，MemGPT：memory blocks + 只读约束）— 2026-03-01
- [x] `ref/chroma`（Python，向量库/服务化；作为取舍对照）— 2026-03-01
- [x] `ref/feishu-openclaw`（Node，飞书本地长连接桥接 + 媒体最稳）— 2026-03-01
- [x] `ref/CoPaw`（Python，console/分发/多渠道工程化；偏产品化）— 2026-03-01
- [x] `ref/open-cowork`（TS/Electron，桌面端沙箱 + GUI 操作 + Trace Panel）— 2026-03-01
- [x] `ref/poco-agent`（Python/TS，Poco：安全沙箱 + UI + 异步/定时任务）— 2026-03-01
- [x] `ref/pi-mono`（TS，Pi Coding Agent：会话树 JSONL + steering/follow-up）— 2026-03-01
- [x] `ref/clawdbot-feishu`（TS，OpenClaw 飞书插件：消息/媒体/权限/策略都很全）— 2026-03-01
- [x] `ref/UltimateSearchSkill`（Shell，双引擎搜索 Skill：Grok + Tavily + Fetch/Map）— 2026-03-01
- [x] `ref/nanoclaw`（TS，容器隔离的轻量 assistant：group queue + scheduler + SQLite）— 2026-03-01
- [x] `ref/zeroclaw`（Rust，pico-scale 竞品：MCP 多传输 + estop + runtime trace）— 2026-03-01

---

## 1) 北极星：我们要做出的“独特 feature”

对标 claw 类竞品时，PicoClaw 的差异化不应该是“更多按钮”，而是：**把真实长期使用的痛点做成一等公民能力**。

### 1.1 5 个差异化支柱（建议写进 README 的卖点）

1) **Replayable Runs（可回放执行）**  
   每次 tool 调用、关键决策与产物都可追溯：能复盘、能重放、能定位问题（而不是“我也不知道刚才发生了啥”）。

2) **Policy-first Tools（工具策略层）**  
   工具不是“裸奔函数”，而是带策略：安全边界、脱敏、限流、确认、人类介入点（Human-in-the-loop）可配置。

3) **Structured Memory（结构化记忆资产）**  
   记忆检索不是返回一坨散文，而是稳定的 JSON hits；记忆有 scope（user/session/agent），有 blocks（persona/human/projects/facts…），可编辑、可回滚、可只读保护。

4) **MCP Bridge（生态护城河）**  
   不靠维护 1000+ 内置工具取胜，而是用 MCP 标准桥接外部工具生态（企业内/社区都可复用），并套上 PicoClaw 的策略层。

5) **Durable Long Tasks（可恢复长任务）**  
   长任务不怕重启：checkpoint/resume、幂等保护、外部副作用可控（“断点续跑”是能长期用的关键）。

---

## 2) Roadmap（按可合并 PR 粒度拆分）

> 原则：每个阶段都能独立上线；不做半年大重构；默认关闭重功能，按配置启用。

### Phase A — 可观测性与可复现（先把“能用”变成“可长期用”）

- A1：Tool Trace（每次 tool call 落盘 md/json；可选开启；带耗时/截断/错误摘要）✅（done: 2026-03-01；落点：`pkg/tools/toolcall_executor.go` + `pkg/tools/tool_trace.go`）
- A2：Run/Session 导出（最少：导出当前 session + tool traces；便于 bug report）✅（done: 2026-03-01；落点：`cmd/picoclaw/internal/export/` + `pkg/tools/tool_trace.go`）
- A3：统一错误提示模板（让模型更会自救：换参数/换工具/先读后写）✅（done: 2026-03-01；落点：`pkg/tools/tool_error_template.go` + `tools.error_template`）
- A4：Token Usage（按 workspace/按模型累计 token；Console 可视化）✅（done: 2026-03-03；落点：`pkg/agent/token_usage.go` + `pkg/httpapi/console.go` + `web/picoclaw-console/src/app/page.tsx`）

### Phase B — 结构化记忆（先输出稳定，再做更强检索）

- B1：`memory_search` / `memory_get` 输出升级为 JSON hits（同时保留 ForUser 摘要）✅（done: 2026-03-01；落点：`pkg/agent/memory_tool.go`）
- B2：Memory blocks（persona/human/projects/facts）+ 只读约束 + 长度上限 ✅（done: 2026-03-02；落点：`pkg/agent/memory_blocks.go` + `pkg/agent/memory.go` + `pkg/agent/memory_vector.go` + `pkg/agent/memory_fts.go`）
- B3：Scope 化（user/session/agent）避免跨渠道污染 ✅（done: 2026-03-02；落点：`pkg/agent/memory_scope.go` + `pkg/agent/context.go` + `pkg/agent/memory_tool.go` + `pkg/agent/compaction.go` + `pkg/tools/toolcall_executor.go`）

### Phase C — 本地确定性检索（SQLite FTS）

- C1：workspace 级 SQLite FTS5 索引（无需向量也能明显提升可用性）✅（done: 2026-03-02；落点：`pkg/agent/memory_fts.go` + `pkg/agent/memory.go`）
- C2：可选混合召回（FTS + 简单 rerank + JSON signals）✅（done: 2026-03-02；落点：`pkg/agent/memory.go` + `pkg/agent/memory_helpers.go` + `pkg/agent/memory_tool.go` + `pkg/config/config.go`）

### Phase D — MCP Bridge（生态接入）

- D1：`tools.mcp.servers[]` 配置 + 动态工具发现/注册（命名空间隔离）✅（done: 2026-03-02；落点：`pkg/mcp/*` + `pkg/agent/loop.go` + `pkg/config/config.go`）
- D2：MCP 工具调用统一走策略层（allow/deny、超时、脱敏、审计）✅（done: 2026-03-02；落点：`pkg/tools/toolcall_executor.go` + `pkg/tools/tool_policy.go` + `pkg/tools/tool_policy_store.go` + `pkg/tools/tool_confirm.go` + `pkg/tools/tool_trace.go` + `pkg/config/config.go`）

### Phase E — Durable execution（checkpoint/resume）

- E1：线性 checkpoint（run 级 JSONL 事件；与 tool trace 互补）✅（done: 2026-03-02；落点：`pkg/agent/run_trace.go` + `cmd/picoclaw/internal/export/helpers.go`）
- E2：`resume_last_task` + “副作用确认”机制（写操作两段提交 / 幂等键）✅（done: 2026-03-02；落点：`pkg/agent/resume_last.go` + `pkg/httpapi/resume_last_task.go` + `cmd/picoclaw/internal/gateway/helpers.go` + `pkg/agent/run_trace.go` + `pkg/tools/tool_policy_store.go`）

### Phase F — 多 Agent 协作（极简 handoff）

- F1：`handoff(agent_name, reason)` 工具（切换 active agent）✅（done: 2026-03-04；落点：`pkg/tools/handoff.go` + `pkg/tools/agents_list.go` + `pkg/agent/loop.go` + `pkg/session/manager.go` + `pkg/routing/session_key.go`）
- F2：与 subagent/并发任务融合（完成后接管/汇总）✅（done: 2026-03-04；落点：`pkg/agent/loop.go` + `pkg/tools/subagent.go` + `pkg/session/manager.go`）

### Phase G — 渠道与媒体（“本地桥接”与“媒体稳”）

- G1：飞书等渠道的“本地长连接桥接”模式（避免公网 Webhook）✅（done: 2026-03-02；落点：`pkg/channels/feishu/feishu_64.go`）
- G2：图片/音频/附件的端到端可靠通路（下载、大小限制、临时文件、回传）✅（done: 2026-03-02；落点：`pkg/channels/feishu/feishu_64.go` + `pkg/media/store.go`）

---

## 3) 外部项目逐个分析（完成一个勾一个）

> 说明：这里的目标不是写“项目介绍”，而是写：**它解决了什么痛点 → 关键实现在哪里 → PicoClaw 如何落地（文件落点/PR 切法/风险护栏）**。

---

### 3.1 ✅ nagobot（Go）— “线程化会话 + 写前日志 + 工具审计” 的轻量实现（已完成：2026-03-01）

仓库：`ref/nagobot`

#### (1) 一句话价值

nagobot 把“真实使用会遇到的工程问题”做到极简但有效：**并发会话调度、消息不丢（WAL）、工具可观测（tool logs）、长对话压缩提示（context pressure）**。

这类能力非常适合 PicoClaw 的定位：不追求框架化，而追求**长期跑得住**。

#### (2) 最值得抄的 5 个点（从高 ROI 到低）

1) **写前日志（WAL）式的 user message 持久化**  
   - 解决的问题：LLM/tool loop 还没跑完就崩溃/重启 → 用户消息丢失。  
   - 关键位置：`ref/nagobot/thread/run.go`（LLM 调用前 append user message 并落盘）

2) **Thread Manager 并发调度模型（每 sessionKey 一个 thread + inbox）**  
   - 解决的问题：多渠道/多会话同时进来时，避免一个全局 loop 拖死。  
   - 关键位置：`ref/nagobot/thread/manager.go`

3) **工具执行单点审计日志（每次 tool call 一份 md）**  
   - 解决的问题：线上 bug/用户反馈无法复盘。  
   - 关键位置：`ref/nagobot/tools/tools.go`（`Registry.Run()` 统一计时、截断、落盘）

4) **mid-execution 消息注入（把新消息插到合理位置）**  
   - 解决的问题：工具迭代期间有新消息/子任务回写，append 到末尾会变噪声。  
   - 关键位置：`ref/nagobot/thread/runner.go`（把 injected user msgs 插在原 user 后）

5) **context pressure hook（token 估算 → 强制 compaction 提醒）**  
   - 解决的问题：上下文快爆了，模型又不愿意主动压缩。  
   - 关键位置：`ref/nagobot/thread/context_pressure.go`

#### (3) 对 PicoClaw 的落地映射（改哪里最合理）

> 这里不写“应该这样”，而写“落在哪个模块最不违背现有架构”。

- WAL（用户消息先落盘）
  - PicoClaw 落点候选：
    - inbound 进入 agent loop 的入口处（尽量早）
    - session 保存相关：`pkg/session/manager.go`（提供安全写盘能力）
  - ✅ 已落地（done: 2026-03-02）：`pkg/agent/loop.go` 在进入 LLM/tool loop 前对 user message 做一次 `Sessions.Save()`（best-effort，不阻断对话）
  - 风险与护栏：
    - 只对 user messages 做 WAL（assistant/tool messages 仍按 turn 结束写），避免写放大
    - WAL 失败只 warn，不应阻断对话

- Tool 审计日志（每 call 一份 md/json）
  - PicoClaw 落点候选：`pkg/tools/toolcall_executor.go`
  - 设计建议：
    - 默认关闭，`config.tools.audit.enabled=true` 时启用
    - 目录建议跟 workspace 走：`<workspace>/.picoclaw/audit/tools/`
    - 同时写 `*.md`（人读）+ `*.json`（未来做 replay/统计）

- Thread Manager（并发会话调度）
  - PicoClaw 已有 `channels.Manager` + bus，建议不要照搬 thread 架构
  - 借鉴点：
    - 把“每个 sessionKey 的执行串行化”做成明确约束（避免同 session 并发写 memory/state）
    - 全局并发额度用 config 控制（你已具备并发工具 executor，可复用思路）

- mid-execution injection / context pressure
  - 这两项更像“未来能力的地基”：
    - 为 durable tasks / 子代理回写 / 自动续跑做准备
  - 落地顺序建议：先做 tool audit + WAL，再做 injection/hook（避免一次改太多）

#### (4) 能直接转化成 PicoClaw 独特 feature 的点

把 nagobot 的工程点组合起来，PicoClaw 可以对外说一句很硬的卖点：

- **“任何一次工具执行都可追溯可复盘（Tool Trace） + 关键用户消息不丢（WAL）”**

这类能力通常只有“重平台”才做，但你可以在 pico-scale 下做得更轻更干净。

#### (5) 建议从 nagobot 引出的最小 PR 切法

- PR-A（最小且最值）：Tool 审计日志（executor 层）+ 统一错误 hint
- PR-B（可靠性）：WAL user message（只落 user，不落 assistant/tool）
- PR-C（增强）：context pressure hook（将 compaction 变成“技能化步骤”）

---

### 3.2 ✅ clawlet（Go）— “本地混合记忆检索（FTS+向量）+ 安全默认值” 的范本（已完成：2026-03-01）

仓库：`ref/clawlet`

#### (1) 一句话价值

clawlet 的核心价值是：它把 **“本地、零依赖、可控的长期记忆检索”** 做成了产品级形态（可选启用、结构化输出、混合检索、可增量更新），同时把 **安全默认值**（restrictToWorkspace / localhost bind）当成一等公民。

这非常贴合 PicoClaw 的路线：我们不需要引入重型向量服务，也能把记忆与安全做得更“可用且可控”。

#### (2) 最值得抄的 6 个点（从高 ROI 到低）

1) **`memory_search`/`memory_get` 的结构化 JSON 输出（让 LLM 能稳定消费）**  
   - 解决的问题：记忆检索结果“给人看”但“不给模型用”（难以二次处理/过滤/引用）。  
   - 关键位置：`ref/clawlet/tools/tool_memory.go`

2) **SQLite FTS + sqlite-vec 混合检索（hybrid merge + minScore/maxResults）**  
   - 解决的问题：仅向量检索不稳定、仅关键词检索召回差；混合能让结果“更确定”。  
   - 关键位置：`ref/clawlet/memory/index_manager.go`（`Search()` + `mergeHybrid()`）

3) **memory search 可选启用：未启用时不暴露工具（减少 prompt 体积与误调用）**  
   - 解决的问题：所有会话都暴露 memory tools 会增加 token 与误用概率。  
   - 关键位置：`ref/clawlet/README.md`（`memorySearch.enabled` 默认 false 的设计）

4) **索引的增量同步与“meta 变更触发重建”**  
   - 解决的问题：workspace 文件变更后索引滞后/污染；embedding 模型/dims 变化后结果混乱。  
   - 关键位置：`ref/clawlet/memory/index_manager.go`（sync / reset 逻辑）

5) **工具 allowlist + 默认 restrictToWorkspace（安全边界先行）**  
   - 解决的问题：工具面太大导致越权/误操作；“默认最小权限”能显著降低风险。  
   - 关键位置：`ref/clawlet/tools/registry.go` + README Security 部分

6) **consolidation 固定 JSON schema（history_entry + memory_update）**  
   - 解决的问题：LLM 压缩/沉淀记忆时输出不稳定，难以测试与回归。  
   - 关键位置：`ref/clawlet/agent/consolidation.go`

#### (3) 对 PicoClaw 的落地映射（按 Phase 对齐）

- 对齐 Phase B（结构化记忆）：
  - 先做 **B1：`memory_search`/`memory_get` 输出升级为 JSON hits**  
    - PicoClaw 落点：`pkg/agent/memory_tool.go`
    - 输出字段建议：`hits[]: {id, score, snippet, source_path, created_at, tags}`（先留扩展位）

- 对齐 Phase C（SQLite FTS）：
  - 先做 C1：只做 FTS5（不依赖 embedding），让检索“确定性”先上一个台阶  
  - 再做 C2：可选 sqlite-vec + hybrid（作为增强；默认关闭）

- 对齐 Phase A（安全 + 可复现）：
  - 引入 tool allowlist 配置（按 agent/按 profile）：
    - “某些 agent 只暴露 read-only tools”
    - “写操作工具必须走确认/策略层”

#### (4) 建议从 clawlet 引出的最小 PR 切法

- PR-1（最小且强感知）：`memory_search`/`memory_get` 结构化 JSON 输出（不改底层存储）
- PR-2（本地检索确定性）：SQLite FTS 索引（workspace 内 `memory/**/*.md` → index.sqlite）
- PR-3（可选增强）：sqlite-vec + hybrid merge（带 embedding provider 可配置）
- PR-4（安全边界）：工具 allowlist + restrictToWorkspace 强化（可按 agent 覆盖）

---

### 3.3 ✅ nanobot（Python）— “MCP 桥接 + 参数校验 + 运行时元信息防注入” 的轻量实现（已完成：2026-03-01）

仓库：`ref/nanobot`

#### (1) 一句话价值

nanobot 虽然是 Python，但它非常“懂生产”：用极小体量把 **MCP 接入、参数校验、错误自救提示、runtime metadata 防注入、provider 元数据归档** 这类工程细节做好了。

对 PicoClaw 来说，nanobot 更像一套“把工具生态接入做对”的参考实现，而不是要抄它的语言/框架。

#### (2) 最值得抄的 5 个点

1) **MCP Tool Wrapper（discover → 动态注册 → call 转发）**  
   - 解决的问题：外部工具生态接入成本高；每加一个工具都要手写适配。  
   - 关键位置：`ref/nanobot/nanobot/agent/tools/mcp.py`

2) **MCP 命名空间策略（`mcp_{server}_{tool}`）**  
   - 解决的问题：工具重名冲突、审计难追踪。  
   - 关键位置：同上（注册阶段）

3) **工具参数 JSON schema 校验 + 统一错误 hint**  
   - 解决的问题：LLM 传参类型错/缺字段导致工具失败后“原地打转”。  
   - 关键位置：`ref/nanobot/nanobot/agent/tools/base.py` + `.../tools/registry.py`

4) **runtime metadata 作为“非指令元信息”注入（防 prompt injection）**  
   - 解决的问题：把 channel/chat/time 等塞进 system prompt 容易被当成指令、污染上下文。  
   - 关键位置：`ref/nanobot/nanobot/agent/context.py`

5) **provider 元数据 registry（prefix/gateway/caching 能力集中管理）**  
   - 解决的问题：provider 逻辑散落在 adapter；网关/缓存能力不统一。  
   - 关键位置：`ref/nanobot/nanobot/providers/registry.py`

#### (3) 对 PicoClaw 的落地映射（对应 Phase）

- 对齐 Phase D（MCP Bridge）：
  - 先做最小 MCP：HTTP/SSE（优先），stdio（后置）
  - 动态工具注册到 registry，命名空间 `mcp.<server>.<tool>`
  - per-call timeout + 失败降级（list_tools 失败返回缓存）

- 对齐 Phase A（可复现 / 自愈）：
  - 高风险工具（exec/web/write/edit）先补 schema 校验与统一错误提示模板
  - “错误响应不落盘”作为兜底策略：避免把 provider 错误写进 session 导致循环 400（nanobot 的经验点）

- 对齐 Phase B（结构化记忆）：
  - nanobot 的 memory consolidate 思路可以迁移为“固定 schema 的 consolidation tool 输出”，降低幻觉与回归成本

#### (4) 建议从 nanobot 引出的最小 PR 切法

- PR-1（生态瞬间变强）：MCP（HTTP/SSE）+ 动态工具注册（带 allow/deny）
- PR-2（稳定性提升）：高风险工具参数 schema 校验 + 统一错误 hint
- PR-3（安全与一致性）：runtime metadata 注入方式调整（明确标注“元信息非指令”）

---

### 3.4 ✅ blades（Go）— “Tool Middleware + Flow 组合 + 可恢复 Runner” 的组件库思路（已完成：2026-03-01）

仓库：`ref/blades`

#### (1) 一句话价值

blades 更像 Go 的 agent 组件库：它不强调“成品助手”，而强调 **可组合能力**（tool schema 自动生成、middleware 横切能力、flow 编排、runner 可恢复）。

对 PicoClaw 来说，最值钱的是：它提供了一套把“策略层/确认/重试/审计”从工具实现里剥离出来的范式，非常适合做我们要的 **Policy-first Tools**。

#### (2) 最值得抄的 5 个点

1) **结构体 → JSON schema → ToolDef 自动生成（降低手写 schema 失误）**  
   - 解决的问题：工具参数 schema 手写易漏字段/类型错，导致 tool calling 不稳定。  
   - 关键位置：`ref/blades/tools/tool.go`（`NewFunc[I,O]` + jsonschema）

2) **Tool middleware chain（Confirm/Retry/ConversationBuffer…）**  
   - 解决的问题：限流、鉴权、确认、脱敏、重试等横切逻辑散落在每个工具里。  
   - 关键位置：`ref/blades/tools/middleware.go` + `ref/blades/middleware/*`

3) **Confirmation middleware（human-in-loop 的最小形态）**  
   - 解决的问题：危险工具调用（exec/write/edit）缺少“真实确认”，只能靠 prompt 约束。  
   - 关键位置：`ref/blades/middleware/confirm.go`

4) **Flow 组合原语（Sequential/Parallel/Routing/Loop）**  
   - 解决的问题：多 agent/多步骤任务难以测试与复用；flow 让编排变成“可测组合”。  
   - 关键位置：`ref/blades/flow/*`

5) **Runner resumable（InvocationID + history 去重，天然支持断点续跑）**  
   - 解决的问题：重启后重复生成/重复执行；无法从中间恢复。  
   - 关键位置：`ref/blades/runner.go`

#### (3) 对 PicoClaw 的落地映射

- 对齐 Phase A（可观测/可复现）+ Phase E（durable execution）：
  - blades 的 “InvocationID + 去重” 思路可用于 PicoClaw 的 checkpoint/resume：  
    - 每个 tool call / step 生成稳定 ID  
    - 重启后按 ledger/checkpoint 跳过已完成 step（避免重复副作用）

- 对齐 Phase B（结构化）：
  - schema 自动生成对 “结构化输出工具”（memory_search JSON / MCP tools）非常关键：减少兼容 bug

- 对齐 Phase F（多 agent）：
  - blades 的 Routing/flow 是“可借鉴但不照搬”的：PicoClaw 可保持轻量，只抄 handoff/路由提示套路

#### (4) 建议从 blades 引出的最小 PR 切法

- PR-1（最值钱）：ToolMiddleware 框架（Confirm/审计/脱敏/限流的插拔点）
- PR-2（安全可用）：给 `exec`/写文件类工具接 Confirm（Gateway/CLI 都能工作）
- PR-3（质量提升）：新增工具优先用“结构体+schema 生成”方式定义（试点 1~2 个工具即可）

---

### 3.5 ✅ trpc-agent-go（Go）— “工程级 MCP ToolSet + SQLite CheckpointSaver” 的参考实现（已完成：2026-03-01）

仓库：`ref/trpc-agent-go`

#### (1) 一句话价值

trpc-agent-go 是更框架化的 Go agent 系统，但它给了我们两块非常“能直接抄套路”的成熟实现：

- **MCP ToolSet**：多 transport（stdio/SSE/streamable HTTP）+ refresh 缓存 + 自动重连 + schema 转换
- **SQLite CheckpointSaver**：durable execution 的存储基石（checkpoint + step writes + lineage/namespace）

对 PicoClaw 来说，价值在“实现方式”，不在“整套 GraphAgent 框架”。

#### (2) 最值得抄的 5 个点

1) **MCP ConnectionConfig 覆盖主流 transport（stdio/SSE/streamable HTTP）**  
   - 解决的问题：接 MCP server 时环境差异大（本地进程/远端服务），缺少统一配置形态。  
   - 关键位置：`ref/trpc-agent-go/tool/mcp/config.go`

2) **ToolSet.Tools() 的 refresh + 缓存兜底（list_tools 失败不致命）**  
   - 解决的问题：MCP server 重启/网络抖动 → tool 列表暂时拉不下来，导致系统整体不可用。  
   - 关键位置：`ref/trpc-agent-go/tool/mcp/toolset.go`

3) **“只对特定错误重连”的 session reconnect（避免无限重试）**  
   - 解决的问题：长跑 gateway 场景下，MCP session 断开是常态；但盲目重试会雪崩。  
   - 关键位置：同上（session manager 相关逻辑）

4) **StructuredContent 兼容（空 content 时保底返回结构化 JSON 文本）**  
   - 解决的问题：MCP 工具可能返回结构化数据；只取 text 会丢信息。  
   - 关键位置：`ref/trpc-agent-go/tool/mcp/toolset.go`（structured fallback）

5) **SQLite checkpoint 表结构清晰：checkpoints + checkpoint_writes**  
   - 解决的问题：断点续跑需要“状态快照 + 每步写入事件”，否则恢复很难做正确。  
   - 关键位置：`ref/trpc-agent-go/graph/checkpoint/sqlite/sqlite.go`

#### (3) 对 PicoClaw 的落地映射

- 对齐 Phase D（MCP Bridge）：
  - PicoClaw 的 MCP 最小版本建议直接对齐 trpc-agent-go 的关键工程点：
    - transport 先做 SSE/streamable HTTP（部署最简单）
    - tools refresh 失败返回缓存
    - 对“可识别的断链错误”做有限次数重连
    - structured content 保留（ToolResult 里保存 raw JSON/meta）

- 对齐 Phase E（Durable execution）：
  - 不引入 GraphAgent，但借鉴它的 checkpoint saver 形态：
    - 用 SQLite 作为可选存储（默认可用 JSONL）
    - 每个 tool call / subagent step 写一条 step write
    - lineage_id = sessionKey 或 taskID（保证隔离）

#### (4) 建议从 trpc-agent-go 引出的最小 PR 切法

- PR-1（MCP 工程化）：MCP servers 配置 + tool refresh 缓存 + 限次重连
- PR-2（checkpoint 基座）：checkpoint store（SQLite/JSONL 二选一）+ step writes（tool call 级别）
- PR-3（human-in-loop）：interrupt/resume 的最小实现（配合 Confirm middleware）

---

### 3.6 ✅ openai-swarm（Python）— “handoff 作为一等工具结果” 的极简原语（已完成：2026-03-01）

仓库：`ref/openai-swarm`

#### (1) 一句话价值

Swarm 的代码量很小，但它把 multi-agent 的本质抽象得非常干净：  
**handoff 不是一堆规则，而是一个明确的“可审计动作”（工具结果触发执行者切换）**。

对 PicoClaw 来说，这能把“多 agent 协作”从“隐式路由”升级为“显式、可回放、可控”。

#### (2) 最值得抄的 2 个点（够用就好）

1) **Handoff = tool call 的结果里返回另一个 Agent（执行者切换）**  
   - 关键位置：`ref/openai-swarm/swarm/core.py`
   - PicoClaw 启发：把 agent 切换做成显式工具 `handoff(agent_name, reason)`，并写入审计/ledger

2) **context_variables 运行时注入但从 tools schema 隐藏**  
   - 关键位置：同上（schema 构建时 pop，执行时注入）
   - PicoClaw 启发：runtime context（workspace/channel/chat/sessionKey）只由执行层注入，永远不要让模型“自己传”

#### (3) 对 PicoClaw 的落地映射

- 对齐 Phase F（多 Agent 协作）：
  - 新增 `handoff` tool（或扩展 `subagent` 支持“接管”模式）
  - 切换后立刻继续 LLM iteration（handoff 是执行流的一部分，而不是“结束本轮”）

- 对齐 Phase A（可回放）：
  - handoff 必须写入 Tool Trace / Task Ledger（否则无法复盘“为什么换了 agent”）

#### (4) 建议从 Swarm 引出的最小 PR 切法

- PR-1：`handoff(agent_name, reason)` 工具 + session active agent 字段
- PR-2：handoff 与 `subagent` 融合（子代理完成后可接管/继续跑）

---

### 3.7 ✅ langgraph（Python）— durable execution 的“机制拆解”与 SQLite checkpointer（已完成：2026-03-01）

仓库：`ref/langgraph`

#### (1) 一句话价值

LangGraph 的价值不在“图 API”，而在它把长任务可恢复（durable execution）拆成了三件可独立实现的机制：

- **checkpoint**（保存状态快照）
- **interrupt**（中断等待外部输入/确认）
- **resume**（从 checkpoint 继续）

PicoClaw 不需要引入 LangGraph，但可以直接抄这套拆解方式，用最小代码做出 Phase E 的核心卖点。

#### (2) 最值得抄的 4 个点

1) **SQLite checkpointer 的最小实现（thread_id / namespace / checkpoint_id）**  
   - 解决的问题：只要有 checkpoint，你就能在崩溃/重启后恢复，而不是重跑整条链。  
   - 关键位置：`ref/langgraph/libs/checkpoint-sqlite/langgraph/checkpoint/sqlite/__init__.py`

2) **Store 的 key 校验与 TTL/GC 思路（安全工程点）**  
   - 解决的问题：namespace/key 缺校验会变成 SQL 注入/路径污染；无 TTL 会无限增长。  
   - 关键位置：`ref/langgraph/libs/checkpoint-sqlite/langgraph/store/sqlite/base.py`

3) **interrupt/resume 的理念（human-in-loop 的正确打开方式）**  
   - 解决的问题：靠 prompt 让模型“假装确认”是不可靠的；必须有真实暂停点与恢复输入。  
   - LangGraph 方式：在执行到危险/缺参节点时中断，外部注入 resume value

4) **同步实现明确限制并发（锁保护连接）**  
   - 解决的问题：SQLite 并发写在不加锁场景下会随机炸；明确边界反而更稳。  
   - 启发：PicoClaw 的 checkpoint store 也要明确并发模型（按 session 串行最简单）

#### (3) 对 PicoClaw 的落地映射

- 对齐 Phase E（Durable execution）：
  - 不做图编排，只做线性 durable：
    - checkpoint：每个 tool call / step 写入（JSONL/SQLite）
    - interrupt：Confirm middleware 触发“等待确认”
    - resume：用户确认后继续从最后一步往下跑

- 对齐 Phase A（安全与可控）：
  - checkpoint/key/namespace 的校验与 TTL/GC 必须一开始就做，否则后面补会很痛

#### (4) 建议从 LangGraph 引出的最小 PR 切法

- PR-1：checkpoint store（SQLite）+ key/namespace 校验 + 简单 GC
- PR-2：interrupt/resume（与 Tool Confirm 打通）

---

### 3.8 ✅ mem0（Python）— “记忆作用域（user/agent/run）+ 结构化增删改” 的产品化边界（已完成：2026-03-01）

仓库：`ref/mem0`

#### (1) 一句话价值

mem0 把“记忆”当成一层独立产品来设计：最重要的不是向量库实现，而是 **作用域边界（scope）** 与 **结构化增删改流程**。

PicoClaw 不需要变成 mem0，但必须借鉴它的边界，否则随着渠道/多 agent 增加，记忆一定会“混锅”。

#### (2) 最值得抄的 4 个点

1) **强制至少提供一个 scope ID（user_id / agent_id / run_id）**  
   - 解决的问题：没有 scope 的记忆检索/写入会跨用户/跨会话污染。  
   - PicoClaw 启发：记忆 API 不允许“无 scope 默认全局”，必须显式。

2) **LLM 驱动的 memory upsert/delete：固定结构输入输出**  
   - 解决的问题：让模型“自由写 MEMORY.md”会幻觉、不可测；结构化决策更稳定。  
   - PicoClaw 启发：consolidation/记忆写入要有 schema（可单测、可回归）。

3) **UUID → short id 映射（防模型幻觉 memory_id）**  
   - 解决的问题：模型很容易编一个不存在的 UUID；短 id mapping 能彻底规避。  
   - PicoClaw 启发：只给模型短 id，真实 id 不暴露。

4) **metadata filters 一等公民（后置但要留坑位）**  
   - 解决的问题：只靠相似度无法区分“同名不同人/不同项目”；filters 能做强隔离。  
   - PicoClaw 启发：先把字段设计出来，底层存储后置。

#### (3) 对 PicoClaw 的落地映射

- 对齐 Phase B3（Scope 化）：
  - 将现有 sessionKey 拆解成 3 个逻辑 scope：
    - `user_scope`：同一个人（未来做跨渠道 identity link）
    - `agent_scope`：同一个 persona/agent
    - `run_scope`：单次会话/单次任务（sessionKey）
  - memory 的 add/search/update 都必须带至少一个 scope（默认可推导，但不能空）

- 对齐 Phase B2（记忆资产化）：
  - 引入“短 id”作为模型侧可见 ID，用于引用/编辑/删除记忆条目（防幻觉）

#### (4) 建议从 mem0 引出的最小 PR 切法

- PR-1：memory_search/memory_write API 增加 scope 字段（先透传/落盘，不改检索算法）
- PR-2：记忆条目 ID 映射层（真实 id ↔ short id），为“可编辑记忆”铺路
- PR-3：结构化 consolidation 输出（固定 JSON schema），替代自由文本更新

---

### 3.9 ✅ letta（Python）— memory blocks + 只读约束：把“长期记忆”变成可控资产（已完成：2026-03-01）

仓库：`ref/letta`

#### (1) 一句话价值

Letta（MemGPT）最可迁移的价值只有一句：**把记忆拆成 blocks，并且对关键 blocks 做只读与长度限制**。  
这能把“人格漂移 / prompt 膨胀 / 记忆被模型改坏”这三类长期痛点直接解决一大半。

#### (2) 最值得抄的 4 个点

1) **Block schema：label/description/value/limit/read_only**  
   - 解决的问题：记忆不是一坨散文，而是一组有边界、有约束的资产。  
   - 关键位置：`ref/letta/letta/schemas/block.py`

2) **字符数上限（limit）在校验阶段强制执行**  
   - 解决的问题：记忆无限膨胀导致 prompt 爆炸；后期很难收。  
   - 启发：PicoClaw 的 blocks 也必须有 hard limit。

3) **read-only 防护：拒绝修改核心 blocks（persona 等）**  
   - 解决的问题：模型“顺手改 persona/human”会导致行为漂移、不可控。  
   - 关键位置：`ref/letta/letta/agent.py`（read-only 检查）

4) **清洗非法字符（例如 \\x00）**  
   - 解决的问题：落盘/数据库/序列化的边界 bug（属于“迟早遇到”的工程坑）。  
   - 启发：PicoClaw 写入记忆时也应做最小清洗。

#### (3) 对 PicoClaw 的落地映射

- 对齐 Phase B2（Memory blocks）：
  - 不需要引入数据库/ORM；先用文件约定即可：
    - `memory/MEMORY.md` 以固定标题切 blocks：`## persona` / `## human` / `## projects` / `## facts`
    - 每个 block 有 `limit`（字符数）与 `read_only`（persona 默认只读）
  - 所有“写记忆”的路径（consolidation / memory_write / skills）都必须走同一套 BlockGuard：
    - 解析 blocks → 应用更新 → 校验 read-only/limit → 原子写回

#### (4) 建议从 letta 引出的最小 PR 切法

- PR-1：定义 blocks 解析/渲染（不改现有 MEMORY.md 内容，只加结构与校验）
- PR-2：给 persona/human 上只读/长度上限（发现违规直接拒绝并提示模型修正）
- PR-3：consolidation 输出改为固定 schema（只允许改可写 blocks）

---

### 3.10 ✅ chroma（Python）— 向量库“服务化路线”的取舍对照（已完成：2026-03-01）

仓库：`ref/chroma`

#### (1) 一句话价值

Chroma 的价值对 PicoClaw 来说主要是“边界对照”：当你走到一定规模，本地 sqlite-vec/FTS 可能不够用，这时才需要外部向量服务；但 **把它做成默认依赖会直接破坏 pico-scale**。

所以对 PicoClaw 来说：Chroma 不是“要集成的组件”，而是帮助我们明确“什么时候不该自己造轮子”的参照物。

#### (2) 从 Chroma 得出的 3 条结论（对 roadmap 的约束）

1) **默认不依赖外部向量服务**  
   - Phase C 优先做 SQLite FTS（确定性）+ 可选 sqlite-vec（增强）

2) **向量存储必须抽象成接口（否则未来不可替换）**  
   - 先定义最小接口（Upsert/Search/Delete + filters + namespace），底层实现可本地/远端二选一

3) **把“重能力”做成 profile/可选配置**  
   - Chroma 这种服务化依赖只能是 opt-in：配置启用、健康检查、失败降级

#### (3) 建议从 chroma 引出的最小 PR 切法

- PR-1：定义 `VectorStore` 接口（先不接任何外部服务，只做适配层）
- PR-2：把现有本地 memory_vector/index 适配到该接口（便于未来替换）
- PR-3（可选）：实现 Chroma HTTP client（仅在启用配置时生效）

---

### 3.11 ✅ feishu-openclaw（Node）— “无需公网服务器的飞书长连接 + 媒体端到端” 桥接实现（已完成：2026-03-01）

仓库：`ref/feishu-openclaw`

#### (1) 一句话价值

feishu-openclaw 的价值非常“实战”：它证明了飞书机器人不一定要 Webhook + 公网服务器，也可以用 **WebSocket 长连接（客户端外连）** 在本地跑一个桥接进程，做到：

- **无需公网 IP / 域名 / ngrok**
- 媒体链路更稳（收图/收视频/发图回传）
- 桥接进程与 Gateway 隔离，崩溃互不影响

这对 PicoClaw 的 Phase G（渠道与媒体）是强参考：我们可以把“渠道接入”做成可选的独立 bridge，而不是把所有复杂性都塞进 gateway。

#### (2) 最值得抄的 7 个点（都是线上会遇到的坑）

1) **长连接架构：Feishu Cloud ⇄ Bridge ⇄ Gateway（本机 127.0.0.1）**  
   - 关键位置：`ref/feishu-openclaw/bridge.mjs`（WSClient + 本地 WS 转发）

2) **消息去重（Feishu 事件可能重复投递）+ TTL cache**  
   - 关键位置：同文件（`seen` + TTL）
   - PicoClaw 启发：我们的 Feishu channel 也应加“事件去重层”，避免重复回复

3) **“正在思考…”占位符（阈值触发，后续 update/delete）**  
   - 关键位置：同文件（`THINKING_THRESHOLD_MS`）
   - PicoClaw 启发：Channel UX 三件套已经有（typing/placeholder），这个实现细节非常接近生产需求

4) **群聊回复策略：只有 @、问号、动词触发才回应（避免刷屏）**  
   - 关键位置：`shouldRespondInGroup()`  
   - PicoClaw 启发：群聊“触发条件”最好做成可配置策略（每个 channel 统一抽象）

5) **媒体端到端：入站下载（image/file/audio/video）+ 出站上传类型映射**  
   - 入站：`downloadFeishuImageAsDataUrl()` / `downloadFeishuFileToPath()`（大小限制 + TTL 清理）  
   - 出站：`uploadAndSendMedia()`（根据后缀映射 Feishu msg_type，避免 230055）

6) **“MEDIA:” 约定（agent 输出一行 MEDIA: /path 或 URL）→ 桥接自动识别并回传**  
   - 关键位置：`parseMediaLines()` + 回传逻辑
   - PicoClaw 启发：这是跨渠道的好约定，适合把“媒体回传”做成统一协议

7) **本地文件外发白名单（防止误外传敏感文件）**  
   - 关键位置：`ALLOWED_OUTBOUND_MEDIA_DIRS` + `isAllowedOutboundPath()`  
   - PicoClaw 启发：任何“从本机读取并发送到外部渠道”的能力都必须先有 allowlist

#### (3) 对 PicoClaw 的落地映射

- 对齐 Phase G（渠道与媒体）：
  - 两条路线二选一（都能做，但建议先做更小的）：
    1) **增强现有 `pkg/channels/feishu`**：补齐去重、占位符策略、媒体上传类型映射、`MEDIA:` 协议解析  
    2) **提供 `picoclaw bridge feishu` 独立桥接（长期更稳）**：让 gateway 更轻，bridge 专注渠道复杂性

- 对齐 Phase A（安全/可控）：
  - 媒体与本地文件外发必须走 allowlist + size limit + TTL 清理（桥接做得很完整）

#### (4) 建议从 feishu-openclaw 引出的最小 PR 切法

- PR-1（立即提升 Feishu 体验）：`MEDIA:` 协议 + 媒体上传类型映射 + 群聊触发策略可配置
- PR-2（可靠性）：事件去重（message_id TTL cache）+ placeholder/update 的失败兜底
- PR-3（架构增强，可选）：独立 `feishu-bridge` 子命令/伴生进程（本地长连接）

---

### 3.12 ✅ CoPaw（Python）— “Console + 配置热更新 + 分发安装” 的产品化路径（已完成：2026-03-01）

仓库：`ref/CoPaw`

#### (1) 一句话价值

CoPaw 对 PicoClaw 的启发不在算法/agent 框架，而在“产品化工程”：  
它把个人助手从“能跑起来”做到了“能长期用、能扩展、能运维”——核心抓手是 **Console（可视化）、安装/卸载体验、配置热更新、MCP 热更新、渠道管理**。

如果我们想在 claw 类竞品里做出独特卖点，CoPaw 告诉我们：  
除了 agent 能力，还要把 **可观测、可配置、可维护** 做成用户能感知的体验。

#### (2) 最值得抄的 6 个点（偏产品与运维）

1) **Web Console（静态前端 + API 后端）一体化**  
   - 关键位置：`ref/CoPaw/src/copaw/app/_app.py`（FastAPI + mount 静态 console）
   - PicoClaw 启发：Gateway 可以提供一个“只读为主”的 Console：sessions、tool traces、cron jobs、health/ready

2) **ConfigWatcher：配置文件变更后按 channel 粒度热重载**  
   - 关键位置：`ref/CoPaw/src/copaw/config/watcher.py`
   - PicoClaw 启发：我们可以先做最小版本（SIGHUP 或轮询），避免每次改 config 都要重启 gateway

3) **MCP client 热更新（独立模块管理 + watcher）**  
   - 关键位置：`ref/CoPaw/src/copaw/app/_app.py`（MCPClientManager + MCPConfigWatcher）
   - PicoClaw 启发：MCP servers 的可用性是“运维问题”，热更新能显著降低停机成本

4) **Console push 消息（小而美的 bounded store，前端轮询/SSE）**  
   - 关键位置：`ref/CoPaw/src/copaw/app/console_push_store.py`
   - PicoClaw 启发：这正好对应我们要做的 Tool Trace / Task Ledger 可视化：先做轻量 push，再谈全量 observability

5) **一键安装/卸载脚本（把依赖、目录、服务管理都封装掉）**  
   - 关键位置：`ref/CoPaw/README*.md`（install.sh / install.ps1 思路）
   - PicoClaw 启发：Go 项目更适合做“一键下载 release 二进制 + 初始化 workspace + systemd/launchd 服务”

6) **渠道/技能/cron 的“可管理性”优先（CLI + Console 双入口）**  
   - 关键位置：`ref/CoPaw/src/copaw/cli/*` + console
   - PicoClaw 启发：我们已经有 CLI/Gateway 双入口，再补“管理面”会非常加分

#### (3) 对 PicoClaw 的落地映射（如何“抄体验但不变重”）

- 对齐 Phase A（可观测/可复现）：
  - Tool Trace 一旦落盘，就很适合加一个 Console 页面：按 session/task 浏览、下载一键打包

- 对齐 Phase D（MCP Bridge）：
  - MCP servers 列表与状态最好可视化（enabled/healthy/tools count/last error），并支持热更新

- 对齐 Phase G（渠道）：
  - Console 不必一开始就“可聊天”，先做配置/状态/日志查看即可（pico-scale 也能有好 UX）

#### (4) 建议从 CoPaw 引出的最小 PR 切法

- PR-1（轻量 Console）：在 gateway 增加只读管理页（sessions/tool trace/cron list）
- PR-2（热更新）：Config reload（SIGHUP 或 watcher）+ 只重载变更的 channels/MCP
- PR-3（分发）：一键安装脚本（下载 release + 初始化 + systemd/launchd 模板）

---

### 3.13 ✅ open-cowork（TS/Electron）— “桌面端沙箱 + GUI 操作 + Trace Panel + 技能库/MCP” 的产品化参考（已完成：2026-03-01）

仓库：`ref/open-cowork`

#### (1) 一句话价值

open-cowork 是 “Claude Cowork 的开源实现”，本质是把 agent 能力做成一个**桌面端产品**：  
它把三类“用户强感知”的能力做得很完整：

- **安全的可执行环境**：路径守卫 +（可选）VM 级隔离（WSL2 / Lima）+ 回退模式
- **可见的执行过程**：Trace Panel（thinking / tool_call / tool_result）实时展示
- **能产出专业文件的 Skills**：PPTX/DOCX/XLSX/PDF 等工作流 + MCP connectors

虽然 PicoClaw 不打算走“直接操作用户桌面 GUI”的路线，但 open-cowork 的“产品化方法论”非常值得借鉴：  
把 agent 的核心抽象（Tools/Skills/MCP/Trace/Permission）做成可视、可控、可审计的系统。

#### (2) 最值得抄的 7 个点（偏“机制/体验”，不抄其重依赖）

1) **PathGuard：明确的危险命令/路径拦截（默认拒绝）**  
   - 关键位置：`ref/open-cowork/src/main/sandbox/path-guard.ts`
   - 启发：exec/web/file 写工具必须有“拒绝列表 + 规则化解释”，不能只靠 prompt

2) **PathResolver：挂载点（virtual↔real）+ traversal/symlink escape 防护**  
   - 关键位置：`ref/open-cowork/src/main/sandbox/path-resolver.ts`
   - 启发：把“workspace 限制”做成核心安全边界，并作为所有 file 工具的单入口

3) **SandboxToolExecutor：把工具执行统一走 sandbox adapter（可切后端）**  
   - 关键位置：`ref/open-cowork/src/main/tools/sandbox-tool-executor.ts`
   - 启发：工具执行的“后端”可替换（host / docker / VM / remote），上层 API 不变

4) **PermissionDialog：危险工具调用的真实人类确认（allow/deny/always allow）**  
   - 关键位置：`ref/open-cowork/src/renderer/components/PermissionDialog.tsx`
   - 启发：PicoClaw 的 Confirm middleware 可以做到同样体验（CLI 用确认提示，Gateway 用 UI/按钮）

5) **TracePanel：执行 trace 的数据模型与 UI 呈现（强 UX）**  
   - 关键位置：`ref/open-cowork/src/renderer/components/TracePanel.tsx`
   - 启发：我们的 Phase A1 “Tool Trace 落盘”一旦有了，就可以很自然做一个 Gateway Trace 页面

6) **MCP tools prompt 的“可解释列表”（告诉模型有哪些 server/tools）**  
   - 关键位置：`ref/open-cowork/src/main/claude/agent-runner.ts`（`getMCPToolsPrompt()`）
   - 启发：PicoClaw 的 MCP Bridge 不仅要“能用”，还要“让模型知道怎么用”（减少失败率）

7) **Skills 作为“带脚本/资源的技能包”交付（但注意 License）**  
   - 关键位置：`ref/open-cowork/.claude/skills/*`  
   - 注意：这些 skill 的 SKILL.md 中有 “proprietary” 标注，**只能借鉴结构与工作流，不应直接拷贝内容到 PicoClaw 仓库**。
   - 启发：PicoClaw 可以自己做一套“文档产出技能包”（pptx/docx/xlsx/pdf），这是非常强的差异化卖点

#### (3) 对 PicoClaw 的落地映射（怎么“借鉴但不变重”）

- 对齐 Phase A（可观测/可复现）：
  - Tool Trace → 直接复用 open-cowork TracePanel 的 step 模型（type/status/duration/input/output）

- 对齐 Phase B/C（记忆）：
  - open-cowork 的记忆不是核心亮点，但它的 Trace + Permission 会让记忆写入更可控（可审计）

- 对齐 Phase D（MCP Bridge）：
  - MCP 工具列表要可视化（server 状态、工具数、最近错误），并支持热更新（参考 CoPaw + open-cowork）

- 对齐 Phase G（渠道与媒体）：
  - open-cowork 的 remote control 体系很重，但它说明“把渠道接入做成独立模块”更稳

#### (4) 建议从 open-cowork 引出的最小 PR 切法（PicoClaw 版本）

- PR-1（强感知）：Tool Trace 落盘 + Gateway Trace 页面（只读即可）
- PR-2（安全）：Confirm middleware（allow/deny/always allow）+ 高风险工具白名单
- PR-3（差异化）：技能包体系升级（把 PPTX/DOCX/XLSX/PDF 做成可安装 skills，脚本化产出）

---

### 3.14 ✅ pi-mono（TS）— “会话树 JSONL + steering/follow-up + 事件流工具执行” 的交互范式（已完成：2026-03-01）

仓库：`ref/pi-mono`

#### (1) 一句话价值

Pi（pi-mono）把“**人在终端里持续协作**”这件事做到非常顺：  
它并不追求大而全，而是把 **会话管理（tree / fork / compact）**、**消息排队（steering/follow-up）**、**工具执行可视化（event stream）** 这三件最影响体验的事情做成了“强默认”。

对 PicoClaw 的启发很明确：我们不缺工具，缺的是“**可控的交互回路**”和“**可复盘的会话资产**”。

#### (2) 最值得抄的 6 个点（都是“能落地的机制”）

1) **Agent 事件流（message/tool 的 start/update/end）作为 UI 基座**  
   - 关键位置：`ref/pi-mono/packages/agent/src/agent-loop.ts`（`agent_start/turn_start/message_update/tool_execution_*`）
   - 启发：PicoClaw 的 Tool Trace（Phase A1）不只落盘，也应该能“流式推送”给 CLI/Gateway UI（占位符/进度条/折叠输出）

2) **Steering message：用户在 agent 运行时插话，并“中断剩余 tool calls”**  
   - 关键位置：`ref/pi-mono/packages/agent/src/agent-loop.ts`（`getSteeringMessages()` + `skipToolCall()`）
   - 启发：这是“活人感”的关键：用户不是只能等 agent 跑完；而是可以插话纠偏。并且 Pi 会为被跳过的 toolCall 生成 toolResult，保证协议闭环（避免 provider 报错）

3) **Follow-up message：把后续请求排队到 agent 完成后再投递**  
   - 关键位置：`ref/pi-mono/packages/agent/src/agent-loop.ts`（外层 while：`getFollowUpMessages()`）
   - 启发：对 Gateway 场景非常重要：群聊里你想“补一句”或“追加任务”，不应打断当前轮次；而应排到 turn_end 后

4) **Session = JSONL + 树结构（id/parentId），原地分叉、原地 time-travel**  
   - 文档：`ref/pi-mono/packages/coding-agent/docs/session.md`、`ref/pi-mono/packages/coding-agent/docs/tree.md`
   - 启发：PicoClaw 当前 session 更像线性日志；Pi 的树结构让“重试/回滚/走分支方案”变成一等能力，并且完整历史永远保留（LLM 只吃 compact 后的视图）

5) **/tree 导航时的“离开分支总结”（Branch summarization）**  
   - 文档：`ref/pi-mono/packages/coding-agent/docs/compaction.md`
   - 启发：这是一种非常实用的“可解释 compaction”：用户知道自己丢了什么/保留了什么；而不是后台偷偷 summarize

6) **Extensions/Skills/Prompts/Themes 的热加载与事件钩子**  
   - 文档：`ref/pi-mono/packages/coding-agent/docs/extensions.md`（`session_before_tree`/`session_tree` 等）
   - 启发：PicoClaw 的 skills 也可以有“事件点”（before_tool/after_tool/before_compact），让扩展更像 middleware，而不是只提供一堆工具

#### (3) 对 PicoClaw 的落地映射（怎么抄到 Go 项目里）

- 对齐 Phase A（可观测/可复现）：
  - 把我们现有的 tool 执行过程（tool start/result/error/duration）抽象成 `AgentEvent`，CLI 输出可折叠，Gateway 可做 /trace 页面

- 对齐 Phase E（Durable execution）：
  - Session JSONL “树”可以直接作为 checkpoint 基座：每个 turn/tool 是一条 event，`parentId` 形成可恢复执行链

- 对齐“活人感”（交互回路）：
  - 引入 steering/follow-up 两类队列：Gateway 收到新消息时，如果当前 session 正在执行 tool loop，就按策略插队/排队，而不是一刀切串行

#### (4) 建议的最小 PR 切法（先把价值跑通）

- PR-1：在 Gateway/CLI 引入“steering message”队列（最小：只支持 turn 结束后插入；进阶：中断剩余 tool calls）
- PR-2：Session 存储升级为 JSONL（先线性），再加 `parentId`（树）与 `/tree` 只读浏览
- PR-3：把 Tool Trace 事件化（start/update/end）并复用到 placeholder/typing/进度输出

---

### 3.15 ✅ clawdbot-feishu（TS）— 飞书“消息/媒体/策略/工具”全链路工程化样板（已完成：2026-03-01）

仓库：`ref/clawdbot-feishu`

#### (1) 一句话价值

clawdbot-feishu 是 OpenClaw 的飞书/Lark 插件，但它的价值远不止“能收发消息”：  
它把**真实线上飞书 Bot**会踩的坑（消息去重、@ 提取、富文本/卡片解析、流式卡片、权限/群策略）都做到了工程级可用。

对 PicoClaw 来说，这个仓库是“飞书渠道做深”的最佳参考：**不是 API 能跑通，而是行为稳定 + 可控策略 + 媒体可靠**。

#### (2) 最值得抄的 7 个点（直接对标 PicoClaw 的渠道层）

1) **消息去重（TTL + 容量上限 + scope key）**  
   - 关键位置：`ref/clawdbot-feishu/src/dedup.ts`
   - 启发：飞书事件可能重复投递，必须去重；并且 key 要带 scope（例如 account/group），避免误伤

2) **@mention 目标提取 + “转发请求”判定**  
   - 关键位置：`ref/clawdbot-feishu/src/mention.ts`
   - 启发：群聊里“只在 @bot 时响应”不够，还需要识别“@bot + @其他人 = 请你转发/代问”的场景（对工作流很常见）

3) **入站消息的“强归一化”：从 text/post/card/merge_forward 提取可喂给 LLM 的 plain text**  
   - 关键位置：`ref/clawdbot-feishu/src/send.ts`（`extractPlainTextFromMessageBody()` / placeholder 替换）
   - 启发：渠道层要保证“输入稳定”，否则上层 agent 再聪明也会被格式噪声拖垮（尤其是卡片、转发、富文本）

4) **流式卡片（streaming_mode）作为“实时输出 UI”**  
   - 关键位置：`ref/clawdbot-feishu/src/streaming-card.ts`
   - 启发：飞书原生支持 streaming card：创建 card → 按 sequence 更新 element → 结束时关 streaming_mode。  
     PicoClaw 现在有 placeholder/edit message，但要做“流式输出”时，卡片比反复 edit 普通消息更稳

5) **群策略与 allowlist：open/allowlist/disabled + requireMention + multi-bot group 兼容**  
   - 关键位置：`ref/clawdbot-feishu/src/policy.ts`
   - 启发：PicoClaw 已有 `allow_from`/`group_trigger`，但缺“多 bot 群”的现实策略（例如 allowMentionlessInMultiBotGroup）

6) **媒体管线（下载→安全检查→临时文件→上传→发送）与错误码经验**  
   - 关键位置：`ref/clawdbot-feishu/src/media.ts`、`ref/clawdbot-feishu/src/outbound.ts`、`ref/clawdbot-feishu/src/send.ts`
   - 启发：媒体是飞书最容易翻车的链路（大小限制/类型映射/鉴权过期/临时文件清理），需要“统一管线”而不是散落在 handler 里

7) **工具层的“权限现实”：不是只有 scope，还包括资源分享/成员关系**  
   - 关键位置：`ref/clawdbot-feishu/README.md`（Drive/Wiki/Bitable/Tasks 的分享与成员要求）
   - 启发：如果 PicoClaw 未来加飞书 Drive/Docs/Tasks 工具，必须把“资源共享前置条件”写进错误提示/修复建议，否则用户会被权限问题困死

#### (3) 对 PicoClaw 的落地映射（我们该补的飞书短板）

- 渠道输入侧（高优先）：
  - 引入“富文本提取器”：把 Feishu `post/interactive/merge_forward` 的 JSON content 统一抽取成 plain text（对齐 `bus.InboundMessage.Content`）
  - 统一 mention placeholder 替换（`@_user_1` 等）→ 变成可读的 `@name` 或 `@mentioned`

- 渠道输出侧（中优先）：
  - 飞书 streaming card：当 provider 支持流式输出时，把 delta 写到 card（并在结束时 finalize）
  - 媒体发送走单一管线（大小限制 + 清理 + 错误码提示）

- 策略侧（中优先）：
  - group trigger 增强：`mention_only` 之外增加“多 bot 群”规则与 bypass 策略（命令可绕过/单 bot 群可放行）

#### (4) 最小 PR 切法（先把飞书“稳”做出来）

- PR-1：Feishu 入站文本归一化（post/card/merge_forward → plain text）+ 去重 TTL（避免重复回复）
- PR-2：飞书 streaming card（可选开关）+ 失败回退为普通消息
- PR-3：飞书策略配置补齐（multi-bot group、allowlist match、命令 bypass）

---

### 3.16 ✅ UltimateSearchSkill（Shell）— “双引擎搜索 + 证据标准 + 失败降级链” 的可复用技能包（已完成：2026-03-01）

仓库：`ref/UltimateSearchSkill`

#### (1) 一句话价值

UltimateSearchSkill 的定位非常清晰：**把“可靠联网搜索”做成一套可落地的工程方案**，而不是给模型一个“搜索工具”就完事。  
它的核心贡献有两类：

1) **工程层**：Grok/Tavily 的多 Key/多 Token 聚合、负载均衡、鉴权、仅本机暴露（127.0.0.1）、Fetch/Extract 的降级链  
2) **方法论层**：`SKILL.md` 把“什么时候该搜、怎么拆查询、证据怎么写、冲突怎么处理”写成可执行的流程

对 PicoClaw 来说，它提供的是“Web 工具系统的正确打开方式”：**工具 + 策略 + 证据格式**三位一体。

#### (2) 最值得抄的 6 个点（我们可以直接落到工具策略层）

1) **双引擎交叉验证（dual-search）是默认，而不是高级选项**  
   - 关键位置：`ref/UltimateSearchSkill/SKILL.md`（工具选择与证据标准）
   - 启发：事实类结论要求 ≥2 来源；否则明确置信度。这可以变成 PicoClaw 的 web tool policy（影响模型行为非常大）

2) **搜索规划框架（意图分析→拆解→策略选择→工具映射）**  
   - 关键位置：`ref/UltimateSearchSkill/SKILL.md`（Level 2/3 的规划流程）
   - 启发：这可以直接注入到我们的 system prompt / web 工具说明里，降低“乱搜/搜不准”的失败率

3) **Fetch 的三级降级链（Extract → Scrape → error）**  
   - README 描述：`web-fetch.sh`（Tavily Extract → FireCrawl Scrape → 报错）
   - 启发：PicoClaw 的 web fetch 也应提供“可配置的降级链”和“失败原因可解释”（被 403/JS challenge/超时/内容过大）

4) **多 key 池化 + 自动负载均衡（抗额度限制）**  
   - README 描述：grok2api / TavilyProxyManager 聚合多个账号/Key
   - 启发：我们可以在 `tools.web` 配置里支持 `api_keys: []`（而不是单 key），并复用我们已有的 cooldown 机制做“失败切换”

5) **安全基线：服务只绑定 127.0.0.1 + API key + SSH tunnel 管理**  
   - README 描述：端口绑定、认证、隧道
   - 启发：如果 PicoClaw 未来提供 web proxy / scraping service，一律“本机默认关闭公网”，并给出安全模板

6) **输出规范：结论先行 + 引用格式固定 + 禁止编造引用**  
   - 关键位置：`ref/UltimateSearchSkill/SKILL.md`（引用/置信度/冲突处理）
   - 启发：这部分非常适合直接变成 PicoClaw 的“web/search 响应模板”，让用户更信任结果

#### (3) 对 PicoClaw 的落地映射（我们怎么“吸收而不变重”）

- 工具层（偏轻量）：
  - 新增一个聚合工具：`web_search_dual`（并行跑 2 个 provider，比如 DuckDuckGo + Tavily / Grok），输出结构化 hits（来源、时间、置信度）
  - web fetch 增强：支持 extractor 插件链（先结构化提取，再降级为纯 HTML 抓取）

- 策略层（强推荐）：
  - 为 web 工具引入“证据模式”：当用户请求事实/最新信息时，强制要求 ≥2 来源；否则让模型明确标注置信度并给出下一步验证建议

- 生态层（可选）：
  - 把 UltimateSearchSkill 当作“外部 skill 包”示例：PicoClaw 的 skills 可以不是纯 prompt，还可以带脚本/compose（但 ref 不进 git，仅参考其结构）

#### (4) 最小 PR 切法（不引入重服务也能提升）

- PR-1：Web 工具“证据标准”提示模板（至少 2 来源 + 冲突处理 + 置信度）✅（done: 2026-03-02；落点：`pkg/tools/web.go` + `pkg/config/config.go` + `pkg/agent/context.go`）
- PR-2：`web_search_dual`（并行双引擎 + 结果去重/聚合）
- PR-3：web fetch 降级链（可配置 extractor，失败原因分类）

---

### 3.17 ✅ nanoclaw（TS）— “容器隔离 + per-group 队列 + scheduler” 的极简安全架构（已完成：2026-03-01）

仓库：`ref/nanoclaw`

#### (1) 一句话价值

nanoclaw 的核心理念是：与其在应用层写一堆 allowlist，不如直接把 agent 跑在**隔离的容器里**。  
在“个人助手”这个场景里，它解决了两个最现实的问题：

1) **安全**：OS 级隔离（Docker / Apple Container），只挂载允许的目录  
2) **并发可控**：每个群/会话一个队列 + 全局并发上限，避免一个大循环把所有会话拖死

对 PicoClaw 的启发很直接：我们可以保持 Go 的轻量，但把“危险工具”做成可选的 sandbox 后端。

#### (2) 最值得抄的 7 个点（都是工程层“跑得住”的实现）

1) **Per-group queue（会话级队列）+ 全局并发上限**  
   - 关键位置：`ref/nanoclaw/src/group-queue.ts`
   - 机制要点：
     - `activeCount >= MAX_CONCURRENT_CONTAINERS` 时进入 waitingGroups
     - task 优先于 message（否则任务会饿死）
     - exponential backoff retry（`BASE_RETRY_MS * 2^(n-1)`）
   - 启发：PicoClaw 当前 gateway 是单 agent loop 串行消费 inbound；未来要支持多会话长任务，必须引入“会话调度层”

2) **Steering 的“硬中断”实现：写 IPC close sentinel（_close）**  
   - 关键位置：`ref/nanoclaw/src/group-queue.ts`（`closeStdin()`）
   - 启发：与 Pi 的“跳过剩余 tool calls”相似，但更硬：直接让容器端 wind down。  
     PicoClaw 可以先做软中断（跳过后续 tool），再做硬中断（取消 exec/web 请求）

3) **任务调度器：发现 due tasks → enqueueTask → 产出结果即回推用户**  
   - 关键位置：`ref/nanoclaw/src/task-scheduler.ts`
   - 启发：它把“干完活提醒你”做成默认路径（runTask 中 streamedOutput.result 直接 sendMessage）

4) **任务容器的“快速关闭”策略（避免 idle timeout 浪费资源）**  
   - 关键位置：`ref/nanoclaw/src/task-scheduler.ts`（`TASK_CLOSE_DELAY_MS` + `scheduleClose()`）
   - 启发：PicoClaw 的 cron/后台任务也应在完成后“自动收尾”：释放并发槽位、写 run log、推送通知

5) **IPC 通过文件系统（input/output 目录 + atomic rename）**  
   - 关键位置：`ref/nanoclaw/src/group-queue.ts`（写 `*.tmp` 再 rename）
   - 启发：这是一种“极简但可靠”的跨进程通信方式，适合 pico-scale（无需消息队列/redis）

6) **SQLite 作为“唯一状态源”（messages/groups/sessions/tasks）**  
   - 架构说明：README 的 Architecture + `src/db.ts`
   - 启发：PicoClaw 已经有 workspace/session/task ledger，把这些信息统一落到 SQLite（Phase B/C）会更可靠（并发、查询、迁移都更好）

7) **“不加功能，加 skills” 的治理策略**  
   - README：Contributing 部分（skills-first）
   - 启发：PicoClaw 也可以把“渠道/重能力”更多做成 skills 包/插件，而不是核心仓库不断膨胀

#### (3) 对 PicoClaw 的落地映射（我们怎么吸收它的安全与调度）

- 安全（高优先，差异化卖点）：
  - 为 `exec` 工具增加 `sandbox_backend`（`host|docker`），docker 模式只挂载 workspace（可选只读），并限制网络/CPU/内存
  - 这与 open-cowork 的 PathGuard/PathResolver 互补：一个是 OS 隔离，一个是路径策略

- 调度（中优先，体验提升）：
  - Gateway 引入“按 sessionKey/peer 分桶”的队列，支持：
    - 同一会话串行（避免上下文竞争）
    - 不同会话并发（有全局上限）
    - 任务优先级（cron/后台任务不饿死）

- 任务系统（已部分具备）：
  - 我们已经做了 cron 完成通知；下一步是补 run log 与状态（success/error/lastRun/duration），对齐 nanoclaw 的“可运营”

#### (4) 最小 PR 切法（不大改架构也能先吃到收益）

- PR-1：`exec` 增加可选 docker sandbox（只挂载 workspace）+ 配置开关（默认关闭）✅（done: 2026-03-02；落点：`pkg/tools/shell.go` + `pkg/config/config.go` + `config/config.example.json`）
- PR-2：Gateway inbound 加“会话分桶队列”（先只做串行 per-session + 并发上限）✅（done: 2026-03-02；落点：`pkg/agent/loop.go` + `pkg/config/config.go` + `config/config.example.json`）
- PR-3：cron/task run log（JSONL/SQLite）+ 失败重试策略（可配置）

---

### 3.18 ✅ zeroclaw（Rust）— “安全（estop）+ MCP 多传输 + runtime trace” 的 pico-scale 竞品工程答案（已完成：2026-03-01）

仓库：`ref/zeroclaw`

#### (1) 一句话价值

zeroclaw 是我们必须认真对标的 claw 类竞品：它把“pico-scale 的极致工程化”做到了一个很高的水位：  
**静态二进制 + 极低开销**只是表象，更关键的是它的设计非常“可运营”：

- 有明确的 **安全控制面（estop / OTP / domain/tool freeze）**
- 有 **runtime trace JSONL** 作为审计与复盘底座
- 有 **MCP**（stdio/http/sse）作为外部工具生态连接层

对 PicoClaw 来说，它提供了一个很好的参照系：我们 Roadmap 的 5 个差异化支柱在它那里都有可落地的工程形态。

#### (2) 最值得抄的 8 个点（偏“机制”，不是抄 UI）

1) **MCP Transport 抽象：stdio / HTTP / SSE 三种传输统一到一个 trait**  
   - 关键位置：`ref/zeroclaw/src/tools/mcp_transport.rs`
   - 机制要点：
     - stdio 用 line-based JSON-RPC，带超时、最大响应体限制（4MB）
     - stdio 会跳过 server notification（`id == null`），继续等真正 response（避免卡死）
   - 启发：PicoClaw 的 MCP Bridge（Phase D）如果想“真的能用”，必须把这些边角考虑进去

2) **MCP Registry + Tool Wrapper：把 MCP tool 变成本地 tool 统一调度**  
   - 关键位置：`ref/zeroclaw/src/tools/mcp_client.rs`、`ref/zeroclaw/src/tools/mcp_tool.rs`
   - 启发：这就是我们要做的 “MCP Bridge”：动态发现、命名空间隔离、统一 error 形态

3) **estop：kill switch 不是布尔值，而是可组合的安全层级**  
   - 关键位置：`ref/zeroclaw/src/security/estop.rs`
   - 支持的级别：
     - kill_all（全停）
     - network_kill（禁网）
     - blocked_domains（域名封锁）
     - frozen_tools（冻结工具）
   - 启发：这非常适合 PicoClaw 的“Policy-first Tools”支柱：把风险控制做成一等能力

4) **fail-closed：安全状态文件损坏时进入安全模式，并写回 state**  
   - 关键位置：`ref/zeroclaw/src/security/estop.rs`（parse/read 失败 → `fail_closed()`）
   - 启发：这是“可长期跑得住”的安全工程哲学：宁可停也不乱跑

5) **OTP gating：resume 操作可要求 OTP（避免误操作/越权）**  
   - 关键位置：`ref/zeroclaw/src/security/estop.rs`（`require_otp_to_resume`）
   - 启发：如果 PicoClaw 将来暴露公网 API，必须有类似“危险操作二次确认”的设计（CLI 用确认，Gateway 用 token/OTP）

6) **runtime trace JSONL：把运行时事件写成 append-only 日志**  
   - 文档线索：`ref/zeroclaw/docs/config-reference.md`（`runtime_trace_path`）+ 相关 audit logging 文档
   - 启发：这与我们 Phase A1 Tool Trace 完全对齐，而且 JSONL 是最适合 pico-scale 的格式（可 grep、可导入、可复盘）

7) **Cron/Schedule 工具更接近“可运营任务系统”：pause/resume/get/run log**  
   - 关键位置：`ref/zeroclaw/src/tools/schedule.rs`、`ref/zeroclaw/docs/cron-scheduling.md`
   - 启发：我们现在 cron 已能 add/list/enable/disable，下一步就该补“状态/暂停/历史/失败重试”

8) **项目内 RFC/Project docs：把复杂机制拆解并可审查**  
   - 例子：`ref/zeroclaw/docs/project/f1-3-agent-lifecycle-state-machine-rfi-2026-03-01.md`
   - 启发：PicoClaw 的长期演进也需要这种“可审计的设计文档”，否则功能一多就不可控

#### (3) 对 PicoClaw 的落地映射（直接对齐我们的 Roadmap 支柱）

- Replayable Runs（可回放）：
  - runtime trace JSONL（事件：tool_start/tool_end/llm_call/error/interrupt），并提供导出与重放入口

- Policy-first Tools（策略层）：
  - 引入 estop（kill/network/domains/tools）作为全局策略开关；exec/web/file 都要读这个状态

- MCP Bridge（生态）：
  - MCP 多传输（stdio/http/sse）与 registry 设计基本可以照搬到 Go 结构里（只换语言与库）

- Durable Long Tasks（长任务）：
  - 引入“生命周期状态机 + checkpoint/suspend/resume”概念（先从 cron/task 做起）

#### (4) 最小 PR 切法（从最强 ROI 的安全与可观测开始）

- PR-1（安全）：`estop` 子系统（本地 state.json + CLI 命令 + 对 exec/web 工具生效）✅（done: 2026-03-02；落点：`pkg/tools/estop.go` + `pkg/tools/toolcall_executor.go` + `pkg/httpapi/estop.go` + `cmd/picoclaw/internal/estop/command.go`）
- PR-2（可观测）：runtime trace JSONL（对齐 Phase A1，先写 tool 事件）
- PR-3（生态）：MCP stdio client（先做一种传输），再扩展 HTTP/SSE

---

### 3.19 ✅ poco-agent（Python/TS）— “可运营的 Agent 平台：回调 + TODO 进度 + 计划审批 + 定时任务” 的体系化答案（已完成：2026-03-01）

仓库：`ref/poco-agent`

#### (1) 一句话价值

poco-agent 的价值不在“又一个 agent”，而在于它把 **Agent 运行**当成“可运营系统”来设计：  
**前端可视化 + 后端状态/队列 + Executor 沙箱 + 回调/进度 + 定时任务**，并且把 Claude Code/Claude Agent SDK 的一些成熟交互范式（Plan Mode / Slash Commands / AskQuestion）产品化。

对 PicoClaw 来说，虽然我们不一定走“多服务 + UI + 容器沙箱”全家桶路线，但它提供了非常清晰的对照：  
哪些能力是 **“长期使用会刚需”**（进度、回调、审批、定时、归档），哪些可以保持 pico-scale（默认关闭、按需启用）。

#### (2) 最值得抄的 8 个点（优先可迁移的设计模式）

1) **“多进程/多服务职责分离”非常清楚：Backend / Executor-Manager / Executor / Frontend**  
   - 文档线索：`ref/poco-agent/docs/en/configuration.md`（服务划分与依赖）  
   - 关键点：内部调用使用 `INTERNAL_API_TOKEN`（内部 API token），避免“服务之间裸奔”  
   - PicoClaw 启发：即便我们仍是单二进制，也可以在内部抽象出同样的边界：  
     - `gateway` = ingress/router  
     - `agent loop` = orchestrator  
     - `tool exec` = executor（带策略）  
     - `run store` = 状态与持久化

2) **Callback 模型：每次 agent 输出/状态变化都回传，UI 只做展示，不做推理**  
   - 关键位置：`ref/poco-agent/executor/app/hooks/callback.py`（running/completed/failed + state_patch + progress）  
   - 启发：PicoClaw 的 Gateway 未来如果做轻量 UI（或 WebSocket 推送），也应该是“事件流”而不是“轮询拼 JSON”

3) **TODO 驱动的进度条：把工作流状态变成稳定结构（可计算）**  
   - 关键位置：`ref/poco-agent/executor/app/hooks/todo.py`（从 `TodoWrite` tool 抽取 todos + current_step）  
   - 启发：这与我们 Roadmap 的 **Replayable Runs / Durable Tasks** 强相关：  
     - 把“进度”定义为结构化数据（而不是模型口头说“快做完了”）

4) **Plan Mode 的“工具白名单”策略：计划阶段先禁执行工具，审批后才开放**  
   - 关键位置：`ref/poco-agent/executor/app/core/engine.py`（`permission_mode == "plan"` + tool allowlist + `ExitPlanMode` 审批）  
   - 启发：这就是 **Policy-first Tools** 最干净的一种落地：  
     - 不靠 prompt 劝模型“别乱跑”，而是硬策略限制工具可用性

5) **Slash Commands 必须“原样透传”，否则 SDK 无法识别**  
   - 关键位置：`ref/poco-agent/executor/app/core/engine.py`（`prompt.lstrip().startswith("/")`）  
   - 启发：对 PicoClaw 来说，如果我们未来加 slash 指令体系，必须把“命令解析”做成稳定协议（而不是靠 LLM 猜）

6) **定时任务：timezone + cron 校验 + next_run_at（UTC）+ coalesce missed execution**  
   - 关键位置：`ref/poco-agent/backend/app/services/scheduled_task_service.py`  
   - 启发：我们当前 cron tool 已能 add/list/enable/disable，但“可运营”还缺：  
     - timezone  
     - next_run_at  
     - run history（成功/失败/耗时/输出摘要）  
     - coalesce（当上一次还在跑时，不要无限堆队列）

7) **“reuse_session vs new session per run”是非常实用的产品化开关**  
   - 关键位置：`ref/poco-agent/backend/app/services/scheduled_task_service.py`（`reuse_session` / `session_id` pinning）  
   - 启发：PicoClaw 的 cron 也应该支持这两种模式：  
     - **固定 workspace**（做持续维护型任务，比如日报、持续研究）  
     - **每次新 workspace**（避免污染，适合一次性任务）

8) **“推荐用 Tailscale 远程访问，而不是直接暴露公网”是一种很现实的安全建议**  
   - 文档线索：`ref/poco-agent/docs/en/docker-compose.md`（Tailscale Serve 示例）  
   - 启发：这对我们的 `/api/notify` 很有参考价值：  
     - 如果用户不想做复杂反代/HTTPS，Tailscale 是更安全的“公网替代品”

#### (3) 对 PicoClaw 的落地映射（对齐我们的差异化支柱）

- Replayable Runs（可回放执行）：  
  - 参考 poco 的 callback + state_patch 思路，把我们 Phase A 的 tool trace 做成**事件流**（JSONL append-only）

- Policy-first Tools（工具策略层）：  
  - 参考 Plan Mode：引入“计划阶段 tool allowlist”，把危险工具（exec/write）默认锁住，直到用户确认

- Structured Memory（结构化记忆资产）：  
  - 参考 scheduled task 的 `config_snapshot`：把“运行配置快照”当成一等公民对象（可回滚/可审计）

- Durable Long Tasks（可恢复长任务）：  
  - 参考 scheduled_task 的 next_run_at/coalesce/reuse_session/run record：把 cron 从“定时触发”升级为“可运营任务系统”

- Channel & Notification（联动扩展）：  
  - Poco 的“回调/推送”理念提示我们：任务完成后应该能主动提醒用户  
  - 我们已经实现 `/api/notify` + cron 完工通知，下一步是把“通知”升级成可配置的工作流节点（可选开启）

#### (4) 最小 PR 切法（优先 pico-scale 可落地）

- PR-1（cron 可运营化）：为 cron job 增加 `timezone/next_run_at/run_history/coalesce`（先写 store schema + list 输出）✅（done: 2026-03-01；落点：`pkg/cron/service.go` + `pkg/tools/cron.go`）
- PR-2（Plan Mode 最小实现）：在 agent loop 中加“计划阶段工具白名单 + 用户确认开关”（先只管 `exec`/`edit`）✅（done: 2026-03-02；落点：`pkg/agent/loop.go` + `pkg/agent/plan_mode.go` + `pkg/tools/plan_mode_gate.go` + `pkg/tools/toolcall_executor.go` + `pkg/config/config.go`）
- PR-3（通知工作流）：把“任务完成提醒”做成可选 hook（配置 `notify.on_task_complete=true` + 使用 `message` tool）✅（done: 2026-03-02；落点：`pkg/agent/loop.go` + `pkg/config/config.go`）
