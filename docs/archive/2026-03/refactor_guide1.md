# PicoClaw 重构指南（参考 `ref/openclaw-mini`）

> 本文目标：**允许大幅度重构**，以换取长期更清晰、更可维护、更可测试的架构。
>
> 适用范围：Go 后端（CLI/Gateway/Agent/Tools/Providers/Channels）+ Web Console（Next.js）+ 工作区与技能系统（SKILL.md）。

---

## 0. TL;DR（建议的最终形态）

- 用 **“分层 + 端口(interfaces) + 适配器(adapters)”** 解决耦合与复杂度，而不是靠更多 helper。
- 把 Agent 相关代码按 `openclaw-mini` 的思路拆成 3 层：
  - **Core（核心层）**：Agent Loop / Session / Context / Tools 抽象 / Provider 抽象 / 事件流
  - **Extended（扩展层）**：Memory / Skills / Heartbeat / Cron 等可选能力
  - **Production（工程层）**：Tool policy / 并发队列 / 运行护栏 / audit/trace
- 用 Go 的 `internal/` 做“隔离墙”：核心不再 import channels/media/http 等基础设施；基础设施实现核心定义的接口。
- 迁移策略：**先建新架构，再把旧代码“抽丝剥茧”搬过去**；保留旧包做薄门面（facade），直到迁移完成再删除。

---

## 1. 为什么需要重构（结合当前仓库的典型症状）

### 1.1 单包过大、职责混杂（可读性与演进成本爆炸）

以 `pkg/agent` 为例：同时包含 loop/context/compaction/memory/audit/token_usage/steering/registry/trace 等多个子系统（见 `pkg/agent/*`，尤其 `pkg/agent/loop.go`）。

结果是：
- “找代码”成本高：一个需求可能跨十几个文件，且没有明确边界。
- “改代码”风险高：容易误触并发/持久化/工具权限/渠道发送等链路。
- “测代码”困难：核心逻辑依赖外部系统（渠道、媒体、HTTP、工具实现），单测无法隔离。

### 1.2 核心逻辑反向依赖基础设施（架构方向错）

`pkg/agent/loop.go` 直接依赖 `pkg/channels`、`pkg/media`、`pkg/tools`、`pkg/httpapi` 的概念/实现。

这会导致：
- Agent Loop 无法脱离 Gateway/Channels 单独运行或做纯逻辑测试。
- “换渠道/换存储/换 Provider”会波及核心循环，改动面变大。
- 依赖图容易出现环（或靠 “抽象泄漏” 规避环）。

### 1.3 工具系统“抽象/治理/实现”混在一起（权限、审计、可扩展性受损）

当前 `pkg/tools` 既包含：
- Tool 抽象与 registry/executor/trace/policy
- 也包含大量有副作用的实现（shell/filesystem/web/skills_install/subagent 等）

当这些混在同一层：
- 很难做到“核心只依赖抽象，基础设施提供实现”
- 很难统一做 tool policy、审计、超时、沙箱、配额等生产治理

### 1.4 组合根（wiring）分散，生命周期边界不清

以 Gateway 为例，当前 wiring 在 `cmd/picoclaw/internal/gateway/helpers.go` 中完成，并且通过 `_` import 拉起所有 channel 实现。

这本身不是错，但如果核心层也知道太多基础设施细节，wiring 很快会“长到无法维护”。

### 1.5 仓库噪音源（对新人/维护者的认知负担）

- `ref/` 下面是大量参考仓库（当前被 `.gitignore` 忽略），对理解主工程是噪音；但你又确实需要 `openclaw-mini` 这种“清晰参考”。
- `web/picoclaw-console` 是另一套技术栈（Next.js），如果缺少清晰的“前后端边界”，会进一步拉低可维护性。

---

## 2. 参考架构：`openclaw-mini` 的“清晰点”应该学什么

`ref/openclaw-mini` 之所以清晰，不是因为代码少，而是因为：

### 2.1 把复杂系统压成少数“不可替代的核心概念”

它将系统拆为 5～7 个核心子系统，并且做到**文件名/目录名即地图**：
- Agent / Agent Loop（双层循环）
- EventStream（类型化事件流）
- Session（持久化）
- Context（加载 + 裁剪 + 摘要压缩）
- Tools（抽象 + 内置工具）
- Provider（模型适配）
再在上面叠加可选扩展（Memory/Skills/Heartbeat）与工程护栏（policy/guard/queue）。

### 2.2 分层不是“讲道理”，而是“用依赖方向落地”

核心层不依赖扩展层/工程层；工程护栏是可插拔的。

### 2.3 “事件流”是系统的骨架，而不是 debug 附属品

核心循环通过事件流向外输出进度、delta、tool 调用、错误、终止等。好处：
- CLI/Gateway/Console 都可以复用一套事件模型
- audit/trace 变成订阅者，而不是散落在逻辑里

---

## 3. PicoClaw 目标架构（建议）

### 3.1 分层模型（建议最终形态）

推荐采用 4 层（与 `openclaw-mini` 的 3 层兼容，只是把“App”显式拎出来）：

```
┌──────────────────────────────────────────────────────────────┐
│ App（应用层）                                                  │
│ - CLI 命令（onboard/agent/gateway/status/...）                 │
│ - Gateway 进程：生命周期、依赖装配、热更新、信号处理            │
├──────────────────────────────────────────────────────────────┤
│ Infra（适配器层 / 基础设施）                                   │
│ - Channels（飞书/企微/Telegram/...）                            │
│ - Providers（OpenAI/Anthropic/Copilot/...）                     │
│ - Tools 实现（shell/fs/web/cron/skills_install/...）            │
│ - 存储（JSONL/SQLite/文件系统）、HTTP API、媒体存储             │
├──────────────────────────────────────────────────────────────┤
│ Extended（扩展层）                                              │
│ - Memory / Skills / Heartbeat / Cron                            │
│ - 这些能力可开关、可替换，不能“污染”核心循环                     │
├──────────────────────────────────────────────────────────────┤
│ Core（核心层）                                                  │
│ - Agent Loop、事件模型、Session、Context、Tool 抽象、Provider 抽象 │
│ - 只依赖标准库 + 极少量基础类型包；不直接做网络/磁盘/HTTP/渠道     │
└──────────────────────────────────────────────────────────────┘
```

核心收益：**当你读 Core 时，你不会看到“飞书、HTTP、shell、SQLite”**；你只会看到“会话、上下文、工具、事件、模型调用”。

### 3.2 核心层应该定义的“端口（interfaces）”

核心层需要的是能力，而不是实现。建议至少抽出这些端口：

- `Provider`：给定 messages/tools/systemPrompt，返回流式输出 + tool calls
- `Tool`：声明式 schema + `Execute(ctx)`，由核心调度/治理
- `SessionStore`：append/load/snapshot（不关心 JSONL 还是 SQLite）
- `EventSink` / `EventStream`：核心把事件写出去（CLI/Gateway/Console 订阅）
- `Clock` / `IDGen`：便于测试与可重放（replay）
- `FileReader` / `ExecRunner`：用于“工具实现层”时可注入 mock（扩展层也会用到）

### 3.3 依赖规则（必须写进架构守则）

建议把下面的规则写成“不可违反”的守则（也可用 lint/CI 强制）：

- `core/*`：禁止 import `infra/*`、`app/*`、`channels/*`、`httpapi/*` 等任何外部实现
- `extended/*`：只允许 import `core/*`（可 import 少量通用 util，但最好下沉到 core）
- `infra/*`：允许 import `core/*`，实现 core 定义的接口
- `app/*`：负责装配（wiring），允许 import core/extended/infra

### 3.4 依赖矩阵（快速判断“是否越界”）

> 经验规则：**出现“为了方便直接 import 一下”时，架构已经开始腐烂**。先问清楚：这是端口接口该存在的位置，还是装配问题该留在 app？

| from \\ to | core | extended | infra | app |
|---|---:|---:|---:|---:|
| core | Y | N | N | N |
| extended | Y | Y | N | N |
| infra | Y | N（建议） | Y | N |
| app | Y | Y | Y | Y |

图例：`Y=允许`，`N=禁止`。

说明：
- 让 `infra -> extended` 保持 **禁止/极少**，否则你会得到“基础设施里混业务”，后续很难替换实现。
- 如果某个 infra 实现真的需要 extended 的策略，通常意味着：
  - 这个“策略”其实应下沉到 core（变成端口/接口），或
  - 应由 app 层注入（装配时把策略传给 infra）

### 3.5 端口接口草案（建议先写出来再迁移代码）

下面给一个“够用但不超前”的 Go 草案，目的是统一语言，避免每个子系统自己发明一套类型。

```go
// internal/core/provider/provider.go
package provider

import "context"

type ToolSchema struct {
	Name        string
	Description string
	JSONSchema  any
}

type Message struct {
	Role    string // system/user/assistant/tool
	Content any    // string or structured blocks
}

type ToolCall struct {
	ID   string
	Name string
	Args any
}

type StreamEvent struct {
	Type     string   // delta/tool_call/end/error/...
	Delta    string
	ToolCall *ToolCall
	Err      error
}

type Stream interface {
	Recv(ctx context.Context) (StreamEvent, error)
	Close() error
}

type Request struct {
	SystemPrompt string
	Messages     []Message
	Tools        []ToolSchema
}

type Provider interface {
	Stream(ctx context.Context, req Request) (Stream, error)
}
```

```go
// internal/core/tools/tool.go
package tools

import "context"

type Call struct {
	ID   string
	Name string
	Args any
}

type Result struct {
	CallID  string
	Name    string
	Content any
	IsError bool
}

type Tool interface {
	Name() string
	Schema() any
	Execute(ctx context.Context, call Call) (Result, error)
}
```

你不需要一次把现有类型完全替换；但至少要保证：
- 新 core 里只出现这一套 canonical types
- 旧包通过 adapter 把现有数据结构转换到 canonical types

---

## 4. 推荐目录结构（可一次性迁移，也可渐进）

> 说明：当前仓库已有 `cmd/picoclaw/internal/*`（CLI 命令）。这里给出“最终形态”建议；渐进迁移时可以先只引入 `internal/core` 等新目录，不必一次挪动 CLI。

```
cmd/
  picoclaw/                 # main.go（保持不动）

internal/
  app/                      # 组合根（composition root）
    cli/                    # cobra 命令实现（可从 cmd/picoclaw/internal 迁来）
    gateway/                # gateway 进程装配、热更新、生命周期
  core/                     # 核心层（无外部依赖）
    agent/                  # Agent facade（组合 loop/session/context）
    loop/                   # agent loop（双层循环、steering/follow-up）
    events/                 # 事件类型（统一对外模型）
    session/                # 会话抽象（store 接口 + domain types）
    context/                # 上下文裁剪/压缩/加载策略
    tools/                  # Tool 抽象 + registry + executor（不含具体实现）
    provider/               # Provider 抽象 + 通用错误分类/重试策略
    routing/                # session_key/agent_id/route（纯算法）
  extended/
    memory/
    skills/
    heartbeat/
    cron/
  infra/
    channels/               # 各渠道实现（适配 inbound/outbound）
    providers/              # OpenAI/Anthropic/Copilot/... 适配 core/provider
    tools/                  # shell/fs/web/edit/... 具体工具实现
    store/                  # JSONL/SQLite/文件存储
    httpapi/                # 网关 HTTP handlers（依赖 core 事件/状态）
    media/                  # 媒体存储实现
    audit/                  # audit/trace 落盘实现（事件订阅者）

pkg/                        # （可选）对外稳定 API；不稳定就保持极小
  picoclaw/                 # re-export 或 SDK（尽量薄）

web/
  picoclaw-console/         # Next.js Console（作为独立 app 看待）

docs/
  refactor_guide.md         # 本文
```

如果你希望更贴近 `openclaw-mini` 的“地图感”，可以把 `internal/core` 再“横向合并”为：
- `internal/core/agent` 下含 `context/ provider/ session/ tools/` 子包（每个子包一个概念）

---

## 5. 从哪里下刀：以 `pkg/agent` 为拆分中心的建议

> 目标不是“拆成更多包”，而是把“依赖方向”纠正，并形成可读的模块边界。

### 5.1 第一步：把 Agent Loop 从渠道/媒体/HTTP 中解耦

现状：`AgentLoop` 持有 `*channels.Manager`、`media.MediaStore` 等（见 `pkg/agent/loop.go`）。

目标：核心 loop 只做三件事：
- 读取 session/context（通过接口）
- 调用 provider（通过接口）
- 调度 tool 执行（通过接口 + policy/guard）

建议改造点：
- 把“发送消息到渠道”的动作从 core 中移除，改为：
  - core 仅产生事件（delta、final、tool_start、tool_result...）
  - infra 的 “channel outbound adapter” 订阅事件并发送
- 把 “media store” 也变成 tool 实现层的细节（例如 image/audio 的读取/上传是工具的一部分）

### 5.2 第二步：把 Tools 分成三块（抽象 / 治理 / 实现）

建议拆分：

1) `core/tools`
- `Tool` 接口与 schema
- `Registry`
- `Executor`（含：并发/超时/重试/审计 hook/trace hook）
- `ToolPolicy`（allow/deny/none），以及 policy 编译/匹配算法

2) `infra/tools`
- `shell`、`filesystem`、`web`、`edit`、`mcp_tool` 等具体实现
- 每个工具尽量在自己的包里，避免 `tools/*.go` 无限膨胀

3) `extended/*`（如果工具本质是“扩展能力”）
- 例如 memory-save、skills-install 这类“改变 agent 能力边界”的工具

### 5.3 第三步：Session / Context 变成“可替换策略”

`openclaw-mini` 的启发是：Context 不是“消息数组”，而是一套策略：
- loader（bootstrap 文件）
- pruning（裁剪）
- compaction（摘要压缩）

建议在 PicoClaw 中把它们明确为：
- `ContextLoader`（读取 workspace 的 AGENTS.md/TOOLS.md 等）
- `Pruner`（多策略：tool_result 优先裁剪、保留最近 N 轮、关键消息 pin）
- `Compactor`（摘要压缩：按 token 预算触发）

然后让 `AgentLoop` 只依赖接口：`ContextManager.Prepare(messages) -> messagesForModel`。

### 5.4 第四步：Provider 层“只做模型适配”，不做业务

建议把 provider 层明确为两部分：
- `core/provider`：统一协议类型（messages/toolcalls）、错误分类、重试/退避策略
- `infra/providers/*`：OpenAI/Anthropic/Copilot 等实现

然后让上层（AgentLoop）只关心：
- `Stream()` 的事件（delta/tool_call/stop_reason）
- 错误类别（rate_limit/context_overflow/auth/network）

### 5.5 第五步：Channels / HTTP API 全部视为“输入输出适配器”

统一思路：
- inbound：渠道消息 -> 统一的 `core/events.InboundMessage`（或 `core/bus`）
- outbound：core 事件 -> 渠道发送（文本/媒体/卡片）

这样 Gateway 只做：
- 装配 adapters
- 管理生命周期
- 提供只读 console API（订阅/读取事件与状态）

---

## 6. 分阶段路线图（可落地、每阶段可合并）

> 原则：**每个阶段都能跑 `go test ./...`，并且行为可回归。**

### Phase 0：建立“架构护栏”（不改业务）

- 写清楚分层与依赖规则（本文就是第一份）
- 列出系统不变式（invariants），例如：
  - session 文件格式（JSONL/metadata）兼容
  - tool trace/audit 的路径与字段
  - gateway 的 `/health`、`/ready`、`/api/notify` 行为不变
- 给关键链路补最小回归测试：
  - `agent loop` 的工具执行/steering/follow-up 行为
  - `context pruning/compaction` 的触发条件
  - `provider error classifier` 的分类稳定

交付物：
- 目录：`docs/architecture.md`（可后续补充）+ 本文
- 若引入 lint：只加“依赖守则”相关的最小规则（避免一上来就打爆 CI）

### Phase 1：抽出 Core 的 domain types + ports（接口）

- 建 `internal/core/*` 的类型定义与接口（Provider/Tool/SessionStore/EventStream）
- 不搬代码，先让现有实现去实现这些接口（adapter 方式）

交付物：
- Core 层不依赖任何 infra
- 新增 core 的单测可以不启动 gateway、不配置渠道

### Phase 2：迁移 Agent Loop（最关键的一刀）

- 把 `pkg/agent/loop.go` 拆解为：
  - `core/loop`：纯循环与状态机
  - `core/context`：消息准备策略
  - `core/session`：会话读写接口 + domain types
  - `extended/memory` 等扩展从 loop 中剥离
- `pkg/agent` 暂时保留为 facade，内部调用新 core

验收标准：
- core/loop 单测可用 fake provider + fake tools 跑通主要路径
- Gateway 仍可运行，CLI 行为一致

### Phase 3：重构 Tools（抽象/治理/实现分离）

- 把 `pkg/tools` 拆成 core/tools 与 infra/tools
- tool policy / guard 变成 executor 的 middleware/hook

验收标准：
- 工具新增/修改不需要改 AgentLoop
- tool trace/audit 是 executor 的统一能力

### Phase 4：重构 Channels + HTTP API（输出适配器化）

- 把 “发送渠道消息” 从 AgentLoop 移除：改为订阅事件
- Gateway 负责把事件写入 state/audit，并推送给 channels

验收标准：
- AgentLoop 不 import channels
- 新增一个 channel 只影响 infra/channels，不影响 core

### Phase 5：清理与收口（删除旧包、收敛 API）

- 删除迁移完成后的旧实现与临时桥接层
- 收敛 `pkg/*` 的 public surface（如果不需要对外 SDK，`pkg` 可以几乎为空）
- 重命名与目录整理：让“目录即地图”

---

## 7. PR/代码评审守则（建议写进 CONTRIBUTING）

- 任何新代码必须明确属于哪一层（core/extended/infra/app）
- core 包内禁止出现：
  - `net/http`、任何渠道 SDK、数据库驱动、os/exec（除非明确作为端口接口）
- 每个子系统至少有 1 个 “fake 驱动” 的单测（避免只能集成测试）
- 任何跨层依赖变更必须更新本文件或 `docs/architecture.md`

---

## 8. 附录：现有目录到目标分层的建议映射（起步用）

> 这是“迁移路线的起点”，不是最终答案；最终以依赖方向为准。

- `pkg/agent/*` → `internal/core/{loop,context,agent}` + `internal/extended/{memory,...}` + `internal/infra/audit`
- `pkg/tools/*` → `internal/core/tools` + `internal/infra/tools`
- `pkg/providers/*` → `internal/core/provider` + `internal/infra/providers`
- `pkg/channels/*` → `internal/infra/channels`
- `pkg/httpapi/*` → `internal/infra/httpapi`（或 `internal/app/gateway/httpapi`）
- `pkg/session/*` → `internal/core/session`（store 接口）+ `internal/infra/store/sessionjsonl`
- `pkg/bus/*` → `internal/core/events`（统一事件模型）或 `internal/core/bus`
- `cmd/picoclaw/internal/*` → `internal/app/cli/*`（可选，后置）
- `web/picoclaw-console/*` → 保持独立 app；只通过 HTTP API 与 gateway 通信
