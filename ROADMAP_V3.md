# ROADMAP_V3: PicoClaw 重构路线图（结构优化 + 去冗余 + 工程闭环）

Date: 2026-03-05

本路线图把以下两份文档合并为一套“可执行”的工程路线：
- [openclaw_review.md](./openclaw_review.md)：从 OpenClaw 学习什么（闭环/运维/安全/运行时工程化），以及优先级建议
- [refactor_guide1.md](./refactor_guide1.md)：如何允许大幅重构但不把代码继续做烂（分层、端口、迁移策略）

重构目标非常明确：**优化结构，减少冗余**。不是“堆 helper”，也不是“把所有东西搬进新目录”。我们要把系统拉回可维护、可测试、可持续迭代的轨道，同时保持可交付（`go test` 和运行验收作为硬门槛）。

---

## 0. 北极星（最终形态）

### 0.1 我们从 OpenClaw 学的不是功能，是闭环

OpenClaw 的可取之处不是“某个工具/某个渠道”，而是它把个人助手做成了完整闭环：

- 产品闭环：`onboard -> gateway 常驻 -> console -> 可预测的交互体验`
- 运行时闭环：`session model -> 队列并发 -> event stream -> tool execute -> compaction/retry`
- 运维闭环：`config validate -> doctor -> status/health/ready -> upgrade/migrate`
- 安全闭环：`sandbox vs tool policy vs elevated` + SSRF/路径/权限边界 + 安全默认

我们要吸收这些闭环的“工程语义”，而不是一次性照搬：
- 所有渠道
- 一套庞大的插件生态
- 大而全的 UI 面板

### 0.2 反屎山原则（必须遵守）

1. 先做 **ports/adapters**，不要用更多 helper 来缝合耦合。
2. **Core 不能依赖 Infra**（channels/http/media/db 等具体实现）。
3. 避免一次性大挪包。推荐迁移策略：
   - `internal/core` 定义 canonical types + ports（接口）
   - 旧包保留为薄门面（facade），保持 import 稳定
   - 逐步迁移调用点，最后删旧代码
4. 任何一步都必须“可测试、可回归”，以 `./scripts/test.sh` 为最低门槛。
5. 去冗余是持续要求：
   - 只有一套 session key 规范化规则
   - 只有一套 event type taxonomy
   - CLI 输出 JSON 的逻辑集中到一个 helper

---

## 1. 分层架构目标（Layered + Ports/Adapters）

### 1.1 目标分层

- **Core**：`internal/core/*`
  - 持久概念和接口（ports），少量稳定类型，事件 taxonomy
  - 只做纯逻辑，尽量只依赖标准库
  - 禁止 import 仓库内的 infra/app 包（强制依赖方向正确）
- **Extended**：`internal/extended/*`
  - 可选能力（memory/skills/heartbeat/cron），应仅依赖 core
- **Infra**：现阶段主要是 `pkg/*`（迁移期），后续可逐步引入 `internal/infra/*`
  - channels/providers/tools I/O/persistence/http/media/audit 等实现
  - 通过 adapter 实现 core ports
- **App**：现阶段主要是 `cmd/picoclaw/internal/*`（迁移期），后续可引入 `internal/app/*`
  - 组合根（composition root）：装配依赖、生命周期、进程管理

### 1.2 依赖规则（必须被强制）

允许方向：
- core -> core
- extended -> core
- infra -> core
- app -> everything

禁止方向：
- core -> infra/app/pkg
- extended -> infra/app/pkg（除非明确批准并说明原因）

强制方式：
- `go test -p 1 ./internal/archcheck -count=1` 必须失败并指出违规 import

---

## 2. 不变式（Refactor 不能破的东西）

1. **持久化格式兼容**
   - session JSONL / meta
   - run/tool trace JSONL
   - policy ledger JSONL
2. **Gateway HTTP 面稳定**
   - `/health`, `/healthz`
   - `/ready`, `/readyz`
   - `/api/notify`, `/api/resume_last_task`
3. **工具执行语义不回退**
   - policy / plan-mode 的 deny 行为一致
   - timeout/kill 行为一致
   - trace/audit 的输出与脱敏不回退

---

## 3. 现状进度（已经落地的“闭环点”）

这一节的意义：把已完成的点写清楚，避免未来反复重复劳动。

### 3.1 Phase 0：架构护栏 + 稳定测试门槛

- [x] `internal/archcheck/archcheck_test.go`：
  - 禁止 `pkg/agent` import `pkg/channels/pkg/httpapi/pkg/media`
  - 禁止 `internal/core` import 任何非 `internal/core` 的 repo 包
- [x] `./scripts/test.sh`：在内存受限环境更稳定（`GOMEMLIMIT/GOGC/GOMAXPROCS`），并只跑关键回归测试集

### 3.2 P0 工程硬化（OpenClaw “闭环”里最划算的部分）

- [x] 工具结果截断改为 **head + tail**，保留尾部诊断（stack trace/error summary）
  - 实现：`pkg/tools/toolcall_executor.go` + `pkg/utils/string.go`
  - 回归：`TestExecuteToolCalls_MaxResultChars_UsesHeadTailTruncation`
- [x] SessionKey 规范化入口：`pkg/utils/session_key.go` (`CanonicalSessionKey`)
- [x] `picoclaw config validate`（含 `--json` + 精确字段 path）
  - CLI：`cmd/picoclaw/internal/config/validate.go`
  - 校验：`pkg/config/validate.go`
- [x] `/healthz` + `/readyz` 以及安全头默认值（`X-Frame-Options/Referrer-Policy/Permissions-Policy`）
  - 实现：`pkg/health/server.go`、`pkg/channels/http_security.go`
- [x] `picoclaw doctor`（只读扫描 + 建议，含 /healthz /readyz probe）
  - 实现：`cmd/picoclaw/internal/doctor/*`

### 3.3 Phase 1：Core 端口与算法的第一刀（去耦合 + 去冗余）

- [x] ports（端口）落地：`internal/core/ports/*`
  - `ChannelDirectory`：由 `pkg/channels.Manager` 作为 adapter 实现
  - `MediaResolver`：由 `pkg/media.AsMediaResolver(store)` 适配
  - `SessionStore`：由 `pkg/session.SessionManager` 实现（history/summary/active_agent/model_override/tree/snapshots）
  - `EventSink`：核心事件消费端口（为 JSONL trace / ws/sse / 渠道占位符 等订阅者留出结构位）
- [x] canonical event taxonomy：`internal/core/events/types.go`
- [x] routing 算法迁入 core：`internal/core/routing/*`
  - agent/account id 规范化、session key 构造/解析
  - `pkg/routing` 保留为薄门面（facade），保持现有 import 不破
- [x] provider 协议与接口迁入 core：
  - `internal/core/provider/protocoltypes`：canonical `Message/ToolCall/ToolDefinition/LLMResponse/...`
  - `pkg/providers/protocoltypes`：薄门面（type alias）
  - `internal/core/provider`：`LLMProvider` / `StatefulProvider` 端口
  - `pkg/providers`：接口薄门面（type alias）
- [x] session domain types 迁入 core：
  - `internal/core/session`：`Session/SessionEvent/SessionMeta/SessionTree/...`
  - `pkg/session`：薄门面（type alias）

---

## 4. 继续推进的工作流（按“闭环点”拆分）

> 下面按 workstream 拆分。原则：每个条目都要有“验收方式”和“回归测试/脚本门槛”。

### Workstream A：Core Canonical Types + Ports（持续推进）

目标：core 只表达系统“需要什么”，infra 提供“怎么实现”。

后续 ports 候选（按收益排序）：
- EventStream（core 发事件，infra 订阅落盘/推送渠道/推 UI）
- Clock/IDGen（便于测试与可回放）

验收：
- core 的单测可以用 fake adapters 跑通（不启动 gateway、不依赖 channels/http）

### Workstream B：Agent Loop（核心循环）继续收敛为可测状态机

目标：把 AgentLoop 从“杂糅一切的超大包”逐步变成可测的状态机。

建议步骤：
- 继续把 `pkg/agent/loop.go` 拆出更聚焦的文件（同 package 保持行为）
- 引入 `internal/core/loop` 后，`pkg/agent` 变成 facade（对外 API 稳定，对内调用 core loop）

验收：
- “fake provider + fake tools” 单测覆盖：
  - tool call loop
  - steering
  - compaction/guard
  - resume_last_task

### Workstream C：Tools（抽象/治理/实现分离）

目标：把工具体系拆成三块，减少耦合和重复逻辑：
- 抽象：Tool interface + schema（core）
- 治理：policy/trace/timeout/parallel（core executor）
- 实现：shell/fs/web/...（infra）

验收：
- 新增一个工具不需要改 AgentLoop
- policy/trace 行为集中在一处，不再散落

### Workstream D：Channels + HTTP 逐步适配器化

目标：core 不直接“发消息到渠道”，而是 emit 事件；infra 订阅事件做 outbound。

验收：
- AgentLoop core 不含 channel-specific code path
- 新增渠道不需要触碰 core 逻辑

### Workstream E：清理与收口（删冗余、收敛 public surface）

目标：迁移完成后删除临时桥接层，缩小 `pkg/*` 面积（除非我们明确要对外作为 SDK）。

验收：
- “目录即地图”：新人能靠目录结构找到概念归属

---

## 5. 去冗余清单（Always-On）

每个重构迭代至少要干掉一种冗余：

- 规则冗余：同一规则（session key 规范化、agent id 规范化、event type）只允许一个 canonical 入口
- 类型冗余：同一概念不要出现多套 struct（先在 core 定 canonical，再用 adapter 转换）
- 字符串冗余：event type/tool type 等不允许散落 magic string（用常量 taxonomy）
- 输出冗余：CLI JSON 输出逻辑集中在一个 helper（避免每个命令自己 MarshalIndent）
- 错误冗余：错误分类（network/auth/rate_limit/context_overflow）集中在 provider 层

---

## 6. 测试与验收门槛（Gates）

最低门槛（必须通过）：

```bash
./scripts/test.sh
```

当改动面更大时建议额外跑：

```bash
go test -p 1 ./cmd/picoclaw/... -count=1
go test -p 1 ./pkg/routing -count=1
```

运行态验收（Gateway）：

```bash
curl -sS http://127.0.0.1:18790/health
curl -sS http://127.0.0.1:18790/healthz
curl -sS http://127.0.0.1:18790/ready
curl -sS http://127.0.0.1:18790/readyz
```

---

## 7. 建议的 PR 切分（来自 openclaw_review，可作为 backlog）

> 经验：每个 PR 只解决一个“闭环点”，并自带最小回归测试/验收方式。

- [x] PR-01：tool result 截断 head+tail（含单测）
- [x] PR-02：SessionKey canonicalization（含 CLI/gateway/UI 覆盖点梳理）
- [x] PR-03：Gateway `/readyz` + 安全头（含 curl 验收）
- [x] PR-04：`picoclaw config validate`（含 `--json` + 错误路径）
- [x] PR-05：Telegram topic/thread sessionKey + topic→agent 映射（通过 `bindings.match.thread_id`）
- [x] PR-06：doctor（只读扫描版）+ 输出建议（后续再加 `--repair`）
- [x] PR-07：SecretRef（env/file）+ 原子快照 + active-surface filtering（gateway reload fail-fast + enabled channel secret preflight）

---

## 8. 飞书/ Lark 专项（产品可靠性，不放进 core）

重构目标以结构为主，但飞书是最重要落地渠道之一，建议把“工程细节”当作独立 workstream（不污染 core）。

P0（优先排雷）：
- [x] 修复 `chat_type: "private"` 识别为 DM（否则 Lark 私聊不可用）
- [x] mention 语义规范化（触发判断 + 上下文保真）
- [x] outbound markdown 兼容性统一入口
- [x] 长消息切分与长度上限
- [x] 中文/特殊字符文件名上传兼容性 + 单测

P1/P2 详见 [openclaw_review.md](./openclaw_review.md) 的 “飞书专项”章节。

---

## 9. 跟进 OpenClaw 更新的机制（建议流程）

OpenClaw 接近日更，把吸收变成流程：

1. 每周扫一次 changelog（近 7 天）
2. 分类：security / ops / channels / runtime / tools / breaking
3. 每类只挑 1-3 条有直接收益的进入 backlog，并打标签：
   - `openclaw-parity`, `openclaw-security`, `openclaw-ops`, `openclaw-channels`
4. OpenClaw schema 变更时，迁移/解析工具至少保证：
   - 读取不崩、忽略未知字段、发出 warnings
