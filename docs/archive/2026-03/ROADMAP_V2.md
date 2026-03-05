# PicoClaw Roadmap V2（ref 深挖结论 + 剩余 backlog）

> 初稿：2026-03-04（Asia/Shanghai）
>
> 更新：2026-03-04（与当前 `main` 分支对齐）
>
> 基线：以 `ROADMAP.md` 为 V1；本 V2 主要维护 “未完成/待推进” 与 “新发现/新增建议”（已落地项会在清单中标记 ✅）。
>
> 约束：`ref/` 不进 git（见 `ROADMAP.md` 的约束说明），因此本文件用于把“ref 深挖结论”沉淀为可追踪的 backlog。

---

## 0) V1 基线快照（用于对齐语义，不再展开细节）

- ✅ Phase A–E：可观测/结构化记忆/FTS/MCP/checkpoint-resume 已落地（详见 `ROADMAP.md`）
- ✅ Phase G：飞书本地桥接 + 媒体通路 已落地（详见 `ROADMAP.md`）
- ✅ Phase F（MVP）：多 Agent handoff（active agent 切换 + 持久化 + takeover）已落地
- ✅ Phase S/H（MVP）：默认安全 + break-glass + limits + audit log + security self-check 已落地
- 🟡 Phase J：运行时模型策略仍有待推进（J1：运行时覆盖的剩余细节待补齐）

---

## 1) 状态更新（相对初稿：已落地/仍待推进）

### Phase F — 多 Agent 协作（handoff）

#### ✅ F1：handoff 工具（active agent 切换）— 已落地

- 参数（OpenAI-compatible schema）：`agent_id`（必填）、`reason`（必填）、`takeover`（可选，默认 true）
- 语义：
  - `takeover=false`：仅持久化 active agent，本轮继续由当前 agent 收尾（下一个 turn 生效）
  - `takeover=true`：本 run 生效（同一轮 tool loop 由新 agent 继续）
- 落点：`pkg/tools/handoff.go`、`pkg/tools/agents_list.go`、`pkg/agent/loop.go`、`pkg/session/*`
- 兼容性：历史字段 `agent_name` 仍可作为别名解析，但 schema 顶层不使用 `anyOf`/`oneOf`（避免 OpenAI-compat 拒绝）

#### ✅ F2：与 `subagent`/并行任务融合（输出 contract + 接管策略）

- ✅ 已落地：`subagent` / `spawn` 输出稳定 JSON contract（`kind=subagent_result`，`summary` + `artifacts`）
- ✅ 补齐：子代理内禁止直接执行 `handoff`（通过 hooks 拦截），改为输出 `handoff_suggestions[]` 供父代理显式决策（可追踪/可审计）

---

## 2) 新发现（`ref/` 再深挖：V1 未覆盖的仓库/提案/缺口）

### 2.1 `ref/` 新增仓库（V1 未纳入清单）

1) `ref/openclaw-mini`（TS）— OpenClaw 核心架构的“可读最小复现”

- 新信息点（V1 未系统覆盖的部分）：
  - **EventStream 事件流模型**：把 run 中的一切做成类型化事件（`message_delta`/`tool_execution_start`/`retry`/`compaction`/`steering`/`subagent_summary`…）
  - **Session JSONL 追加写**：首条 assistant 后落盘，后续 O(1) 追加；坏行可跳过（更偏“日志系统”而非“快照系统”）
  - **Guard 组件化**：`tool-result-guard` / `context-window-guard` / `sandbox-paths` / `command-queue` 都是可插拔模块
- 对 PicoClaw 的新增启发：
  - 现有 `tool_trace`/`run_trace` 已具备“写盘”，但事件语义仍偏分散；可以向 EventStream 收敛，直接驱动 console/渠道“占位符/进度/回放”。

2) `ref/picoclaw-study`（MD 文档集）— PicoClaw 本仓库的结构化复盘/索引

- 新信息点：
  - 这是“给未来自己/新同学”的导航层：`architecture.md`/`dataflow.md`/`borrowable-patterns.md`/`integration-plan.md`
  - 可作为 V2 的“知识底座”，避免 `ROADMAP.md` 继续无限膨胀
- 对 PicoClaw 的新增启发：
  - 建议把 V2 的“可合并 PR 切法/定义完成度/风险护栏”沉到这类文档结构中（Roadmap 保持短、可维护）。

### 2.2 现有 ref 内的新提案文档（V1 未显式吸收）

#### A) ZeroClaw 的 OS-level hardening 提案（安全/可运营）

来源：
- `ref/zeroclaw/docs/resource-limits.md`
- `ref/zeroclaw/docs/audit-logging.md`
- `ref/zeroclaw/docs/security-roadmap.md`

新发现的缺口（对照 PicoClaw 现状；截至 2026-03-04）：

1) **资源限制（Resource Limits）**  
   - ✅ 已有 soft budgets（wall time / tool calls / tool result chars / read_file bytes）
   - 🚧 “CPU/内存/磁盘” 的 OS-level 硬上限（cgroups / systemd / disk quota 等）仍可补齐

2) **可防篡改审计日志（Tamper-evident Audit Log）**  
   - ✅ Audit log（append-only JSONL + rotation + 可选 HMAC 签名）已落地
   - 🚧 verify CLI / query（离线校验与筛选）仍可补强

3) **安全自检（`security --check`）**  
   - ✅ `picoclaw security --check` + `/api/security`（只读）已落地

4) **更强的 OS sandbox（可选）**  
   Landlock/Firejail/Bubblewrap/seccomp 等（保持默认关闭，按平台能力自动降级）。

对 V2 的落地建议（Phase H，已部分落地）：
- ✅ H1：Resource limits（预算/软限制）
- ✅ H2：OS-level enforcement（host ulimit + docker flags；cgroups/容器/沙盒后端可选增强）
- ✅ H3：Audit log JSONL + rotation（可选 HMAC 签名）
- ✅ H4：`picoclaw security --check` / `/api/security`（输出当前安全态）

#### B) Nagobot V2 提案：自愈 + 主动编排 + 可持续

来源：
- `ref/nagobot/docs/proposal-v2.md`

新发现的缺口（对照 PicoClaw 现状，建议先做“明确且可审计”的版本）：

1) **运行时模型策略（per task / per session）**  
   PicoClaw 已有多 agent + model alias + fallback，但缺少“运行时覆盖/回滚/TTL/留痕”的统一入口。

2) **Proactive Orchestrator（主动编排者）**  
   除 cron/heartbeat 的定时触发外，引入一个“扫描全局状态 → 决策唤醒/压缩/重试”的 orchestrator agent，把系统从被动变主动。

3) **更完整的 self-healing checklist**  
   `max tool rounds`、`sink delivery retry`、`panic recovery` 等（逐条评估是否已覆盖；缺的补齐）。

对 V2 的落地建议（新增 Phase J/K）：
- J1：运行时模型覆盖/切换（优先做“显式且可审计”的覆盖方式）
- K1：Orchestrator（先 cron MVP，再做规则+LLM 混合）

### 2.3 逐项目深挖补充（每个 `ref/*` 的 Delta：更像“可抄的工程细节”）

> 说明：
> - `ROADMAP.md` 的 3.x 小节已经覆盖了大部分“宏观启发”；这里补的是：**更工程化/更底层的可移植细节** 与 **新增缺口**。
> - 每条只写“Delta”，避免把 V2 写成重复的项目介绍。

- `ref/CoPaw`
  - Delta：写得非常完整的安全与信任模型（明确：个人助手≠多租户隔离；skills 属于 TCB；prompt injection 单独不算漏洞；报告必须证明 boundary bypass），并强调与 `init` 引导的安全提示一致。见 `ref/CoPaw/SECURITY.md`
  - Backlog：Phase S（new）— 把 PicoClaw 的“信任模型/边界/开箱默认安全”写成可执行规范（`SECURITY.md` + init/启动提示 + 文档对齐）

- `ref/UltimateSearchSkill`
  - Delta：把“联网搜索可靠性”当成系统工程：双引擎交叉验证、多 key 池负载均衡、`web-fetch` 多级 extractor 降级链、Cloudflare 绕过（FlareSolverr）。见 `ref/UltimateSearchSkill/README.md`
  - Backlog：Phase L（升级）— `web_search_dual` + provider key pool + `web_fetch` extractor 降级链（可选 FireCrawl/FlareSolverr，默认关闭）

- `ref/blades`
  - Delta：checkpoint 的“正确时机”与 resume 的“正确重建”：仅在 idle（无 in-flight）且有进展时落 checkpoint；resume 后重建 ready queue/remaining deps；confirm middleware 以 `ErrInterrupted` 统一表达中断。见 `ref/blades/graph/task.go`
  - Backlog：Phase H/I — checkpoint/事件流在并发场景下必须以“quiescent point”落盘；resume 必须能过滤已处理输入，避免重复副作用

- `ref/chroma`
  - Delta：更偏“独立服务/分布式向量库”的工程路线（tilt/k8s/多语言 bindings），适合作为可选外部依赖，而不是 pico-scale 默认路径。见 `ref/chroma/DEVELOP.md`
  - Backlog：保持“外部向量服务 client”只做适配层（默认关闭），避免把 PicoClaw 推向重服务

- `ref/clawdbot-feishu`
  - Delta：飞书渠道的**策略矩阵**与“踩坑清单”很系统：DM policy（pairing/open/allowlist）、group policy、mentionless 在 multi-bot group 的安全默认、命令 bypass、renderMode、以及 Drive/Wiki/Bitable 资源侧还需“共享给 bot”才能访问。见 `ref/clawdbot-feishu/README.md`
  - Backlog：Phase G+（new，可选）— 飞书策略配置补齐（dm/group/mentionless 安全默认/命令 bypass），并把“资源共享约束”写入运维文档/错误提示

- `ref/clawlet`
  - Delta：安全默认与 exec 细节很实用：`restrictToWorkspace=true`、`allowPublicBind=false` 的显式 break-glass；exec 的危险构造 guard；子进程 env allowlist；多模态媒体限制（max bytes/timeout）。见 `ref/clawlet/README.md`
  - Backlog：Phase S/H — `exec`/docker sandbox 的 env allowlist + bind/public 的 break-glass 机制 + 媒体下载/inline 上限统一化

- `ref/feishu-openclaw`
  - Delta：飞书桥接的“媒体稳 + UX”细节：2.5s 阈值触发“正在思考…”占位符并 patch 替换；群聊低打扰触发 heuristics；本地 secret 以文件 600 权限存放。见 `ref/feishu-openclaw/README.md`
  - Backlog：Phase G+/I — 把 placeholder/patch 的阈值策略抽象成通用事件（`typing/placeholder`），并在文档里规范 secret 存放位置与权限建议

- `ref/langgraph`
  - Delta：checkpoint 体系是“产品级 contract”：`thread_id`/namespace/metadata、pending sends 的归属规则、不同 backend 的一致 API（sqlite/postgres）。见 `ref/langgraph` 内 `libs/checkpoint-*`
  - Backlog：Phase H/I/N — checkpoint 需要 namespace 与 metadata（用于 resume/诊断/可视化），并定义“待发送消息/副作用”的归属与幂等策略

- `ref/letta`
  - Delta：把“任务队列 + heartbeat”写进 system prompt 的控制流（每次 run 先 `task_queue_pop`，并 request heartbeat 链式推进）；同时把“来源文件”做成可管理的 memory blocks（file_blocks，attach/detach 可测）。见 `ref/letta/examples/notebooks/data/task_queue_system_prompt.txt`
  - Backlog：Phase K/I — Orchestrator 可以用“task queue + heartbeat”作为最小形态；并把“上下文来源（文件/网页）”事件化（attach/detach），避免隐式塞进 prompt

- `ref/mem0`
  - Delta：把 scope 变成 API contract（`user_id/agent_id/run_id` + metadata filters 一等公民），并强调“写入/查询时 ID 一致性”是稳定性的核心。见 `ref/mem0/docs/integrations/*`
  - Backlog：Phase J/S — PicoClaw 的 identity/session scope 继续往“显式可审计的 ID contract”靠拢（尤其是跨渠道 unified identity）

- `ref/nagobot`
  - Delta：V2 proposal 强调“自愈 checklist”：panic recovery、WAL user message、retry 分类、max tool rounds、sink delivery retry、proactive orchestrator。见 `ref/nagobot/docs/proposal-v2.md`
  - Backlog：Phase H/K — 把“自愈 checklist”做成可勾选的 hardening 任务清单（并写入回归测试/可观测）

- `ref/nanobot`
  - Delta：工具参数校验不是“靠类型断言”，而是把 JSON schema 当成 runtime validator；Skill 里建议把大段领域资料放到 `references/`，SKILL.md 保持可执行步骤短小。见 `ref/nanobot/nanobot/agent/tools/base.py` 与 `ref/nanobot/nanobot/skills/skill-creator/SKILL.md`
  - Backlog：Phase I/S — 对高风险工具（exec/web/write）补 schema validator；Skills 规范升级：支持 `references/*` 并约束“SKILL.md 只放流程”

- `ref/nanoclaw`
  - Delta：安全边界以“容器隔离”为主（非应用层 allowlist）；mount allowlist 放在工作区外且永不挂载；主群/非主群不同信任等级；project root 只读挂载；env var allowlist；IPC 授权矩阵。见 `ref/nanoclaw/docs/SECURITY.md`
  - Backlog：Phase S/H — channel trust level（主 chat vs 群聊）一等公民；docker sandbox 的 mount allowlist 外置 + 默认 read-only root；env allowlist；副作用工具按信任等级收紧

- `ref/open-cowork`
  - Delta：把“exec sandbox”做成多 backend：WSL2/Lima VM 级隔离 + fallback；Trace Panel UI；内置 office skills。见 `ref/open-cowork/README_zh.md`
  - Backlog：Phase I/H（optional）— Trace Panel 的交互形态可直接借鉴；sandbox backend 可以扩展为 VM（默认关闭）

- `ref/openai-swarm`
  - Delta：handoff 的最小原语：工具返回 `Agent` → `active_agent` 切换；并把 runtime context（`context_variables`）从 tools schema 隐藏，但执行时注入。见 `ref/openai-swarm/swarm/core.py`
  - Backlog：Phase F — PicoClaw 优先做显式 `handoff` 工具（可审计）；可选兼容“工具返回 handoff 建议”但仍需显式确认/留痕

- `ref/openclaw-mini`
  - Delta：EventStream taxonomy（tool_start/tool_end/retry/steering/compaction 等）非常适合作为“trace/UI/渠道占位符”的统一数据模型。见 `ref/openclaw-mini/src/agent-events.ts`
  - Backlog：Phase I — 事件流优先统一语义（start/update/end），再把 tool_trace/run_trace 收敛成事件视图

- `ref/pi-mono`
  - Delta：session JSONL **树结构**（`id/parentId/leaf`），`/tree` 原地切分支 + 可选 branch summary；extension/hook 系统可拦截 tool_call/tool_result、提供 UI 交互、持久化自定义 entry。见：
    - `ref/pi-mono/packages/coding-agent/docs/session.md`
    - `ref/pi-mono/packages/coding-agent/docs/tree.md`
    - `ref/pi-mono/packages/coding-agent/docs/extensions.md`
  - Backlog：Phase N（new）— PicoClaw 的 session 存储升级为“可分支的 JSONL 树”+ branch summary；并补 hook/extension 机制（至少支持 tool_call gate）

- `ref/picoclaw-study`
  - Delta：把“自己仓库”写成可检索的学习索引（architecture/dataflow/borrowable-patterns/integration-plan），非常适合作为 Roadmap 的外延知识库。见 `ref/picoclaw-study/README.md`
  - Backlog：文档工程：Roadmap 保持短，细节沉到 `docs/` 或 `picoclaw-study` 风格的索引文档

- `ref/poco-agent`
  - Delta：Plan Mode 的 UX 闭环：两阶段（plan→approve→execute），并通过 callback/state_patch 把运行态推到前端；能力页对 skills/plugins/MCP 做 catalog 管理。见 `ref/poco-agent/executor/app/core/engine.py`
  - Backlog：Phase I/K — plan/confirm 的 UI（console）与 callback 事件流（state_patch）可以直接借鉴，实现“可运营的 agent”

- `ref/trpc-agent-go`
  - Delta：把 tool calls/responses/reasoning 组织成稳定 `AgentTrace`，用于 evaluation 与可视化；并注意 JSON 输出细节（空数组用 `[]`，避免 `null` 破坏下游）。见 `ref/trpc-agent-go/examples/knowledge/evaluation/knowledge_system/trpc_agent_go/trpc_knowledge/knowledge.go`
  - Backlog：Phase I — trace schema 要能被外部消费（evaluation/console）；所有 JSON 输出尽量避免 `null` 语义坑

- `ref/zeroclaw`
  - Delta：把“资源限制 + 防篡改审计 + OS sandbox”作为 roadmap 主线（不是附加项），并提供 `security --check`/audit query 的运维入口。见 `ref/zeroclaw/docs/security-roadmap.md`
  - Backlog：Phase H — Resource limits + audit signing + security self-check（默认关闭，按配置启用）

---

## 3) V2 Roadmap（按可合并 PR 粒度；标注实现状态）

> 原则延续：默认关闭重功能；每个 PR 都能独立上线；pico-scale 优先；先软约束再硬隔离。
>
> 标记：✅ 已落地｜🟡 部分完成｜🚧 待推进（截至 2026-03-04）

### Phase F（carry-over）— 多 Agent handoff（见上文）

- ✅ F1：`handoff(agent_id, reason, takeover?)` 工具（active agent 切换）
  - 兼容：历史字段 `agent_name` 仍可作为别名解析，但参数 schema 顶层不使用 `anyOf`/`oneOf`（避免 OpenAI-compat 拒绝）
- ✅ F2：handoff 与 subagent/并行任务融合（接管/汇总）
  - ✅ 输出 contract：`subagent_result`（`summary` + `artifacts`）
  - ✅ 保护：subagent 内禁止 `handoff`，通过 `handoff_suggestions[]` 输出建议（显式 handoff + trace/audit 留痕）

### Phase S（new）— Security Model：信任边界、默认安全与 break-glass

> 目标：把“个人助手”运行时的安全前提说清楚，并把默认值做成“默认安全、显式放开”的 break-glass。

- ✅ S1：`SECURITY.md` — 明确 trust model（个人/单操作者优先，不承诺多租户隔离）、skills/插件的 TCB 语义、Out-of-scope（prompt injection-only 等）
- ✅ S2：对外暴露/危险配置的显式 break-glass 开关（public bind / unsafe workspace / unsafe exec / docker network / exec inherit env）
- ✅ S3：`exec` env allowlist + deny patterns + secret hygiene（避免把宿主环境 secrets 透传给子进程/容器）
- ✅ S4：按 channel 信任等级收紧（主 chat vs 群聊），把策略写进 config（默认更保守）
  - `plan_mode.default_mode_group=plan`（群聊默认进入 plan；限制 side-effect tools，需显式 `/switch plan to run`）

### Phase H（new）— Hardening：资源限制 + 可证明审计

- ✅ H1：资源预算模型（soft limit）
  - per-run：最大 tool calls、最大累计 wall time、最大输出字节、最大 trace 体积
  - per-tool：`exec`/`web_fetch`/`write_file` 的超时/输出/文件大小 guard 统一化
- ✅ H2：资源限制 enforcement（hard limit，可选）
  - ✅ host exec backend：`ulimit` wrapper（memory/cpu/file/nproc，best-effort）
  - ✅ Exec docker sandbox：docker run flags（`--memory/--cpus/--pids-limit/--read-only` 等）
  - 🚧 Linux：cgroups v2 / systemd 限制（可选增强，不强绑定代码）
- ✅ H3：Audit log（append-only JSONL）
  - 事件覆盖：exec/web/file write/mcp call/estop change/config reload/handoff
  - 支持 rotation（按大小/按日）
  - 可选 HMAC 签名 + `picoclaw auditlog verify`
- ✅ H4：`security --check` / `/api/security`（只读）
  - 输出 sandbox backend、limits、audit、estop 状态（便于运维与排障）

### Phase N（new）— Session Tree + Hooks：把“分支/回滚/拦截”做成一等能力

- ✅ N1：Session JSONL 树（`id/parent_id/leaf`）与 `/tree`（原地切换分支）
  - 可选：离开分支时生成 branch summary entry（用于保留探索路径的可检索摘要）
- ✅ N2：Hook/Extension 机制（最小版）
  - ✅ `tool_call` 拦截：支持 short-circuit（deny/rewrite）；已用于禁止 subagent 内 `handoff`
  - ✅ `tool_result` 拦截：内置 redaction hook（regex + JSON field）
  - ✅ tool trace 记录 `hook_actions`（便于 UI/运维）
  - ✅ 自定义 entry：hook actions 会写入 tool trace（不进 LLM context，可用于 UI/运维）

### Phase I（new）— EventStream：把“可回放”推到 UI/渠道

- ✅ I1：统一事件模型（最小可用）
  - run trace：`run.start/run.end/run.error`、`llm.request/llm.response`、`tool.batch`
  - tool trace：`tool.start/tool.end`（含 args/result preview + per-call snapshot）
- ✅ I2：事件落盘（JSONL）+ 视图层消费（console/gateway）
  - run trace：`.picoclaw/audit/runs/<session>/events.jsonl`
  - tool trace：`.picoclaw/audit/tools/<session>/events.jsonl`
  - console：`/console/` + `/api/console/runs` + `/api/console/tools`（只读）
- ✅ I3：Trace Panel（只读）
  - console 内置 trace viewer：runs/tools 列表点击 View → tail events.jsonl（带过滤/raw 查看/下载）

### Phase J（new）— Provider/Model 运行时策略（可审计）

- 🟡 J1：运行时模型覆盖（per session / per task）
  - ✅ `session_model`（TTL + 持久化）已落地（CLI：`/switch session_model to <name> [ttl_minutes]`）
  - ✅ Gateway API：`/api/session_model`（GET/POST）+ audit log 留痕
- ✅ J2：自动降级策略（与 fallback 链协同）
  - 当连续触发 fallback / 上下文溢出时：自动把 session_model 切到首个可用 fallback（TTL + audit + trace 留痕）

### Phase K（new）— Proactive Orchestrator（主动编排）

- ✅ K1：Cron-based orchestrator（MVP）
  - supervisor audit loop：定时扫描 task ledger（running 超时 / failed 重试预算 / completed 质量与一致性）
  - auto remediation：按配置 spawn subagent 重试 + 发布 audit report（notify 到 last_active/指定 channel）
- ✅ K2：Rule-based + LLM escalation（进阶）
  - audit supervisor 支持 mode=escalate：仅对规则已命中的 tasks 调用 LLM；并支持 max_tasks 限流

### Phase G+（optional）— Feishu 策略与 UX 补齐（偏“生产可运营”）

- ✅ G+1：策略矩阵补齐：dmPolicy/groupPolicy/requireMention/mentionless 安全默认/命令 bypass
- ✅ G+2：统一 placeholder 策略：阈值触发 → patch/update → 失败回退（事件化落盘）
  - delay 阈值（避免快回复闪烁）+ cancel + placeholder edit（失败自动回退 normal send）
  - audit log：`channel.placeholder.*` 事件落盘（console 可直接 View/Download）
- ✅ G+3：把“资源共享给 bot”这种平台约束写入错误提示与运维文档（减少踩坑）
  - 飞书媒体下载失败时追加 `media: unavailable` 提示；并补齐 `docs/channels/feishu/README.zh.md`

### Phase L（optional）— Web 工具可靠性栈（搜索 + fetch + 证据）

- ✅ L0：provider key pool（多 key 轮转；配额可观测作为后续增强）
- ✅ L1：`web_search_dual`（并行双引擎 + 结果去重/聚合 + evidence summary）
- ✅ L2：`web_fetch` extractor 降级链
  - HTML：readability-like → strip tags → raw
  - JSON：pretty print → raw
  - 失败原因分类（DNS/timeout/403/oversize/parse）
- ✅ L3：缓存/去重（同 URL 在短窗口内不重复拉取）
- ✅ L4：证据引用规范（与 `web_search` 的 evidence_mode 结合）：`web_fetch` 输出 `sources[]`/`quotes[]`

---

## 4) V2 追踪清单（建议用它替代“脑内 TODO”）

### Must（P0）

- [x] F1：handoff 工具（最小闭环）
- [x] F2：subagent 融合（contract + handoff_suggestions + 禁止子代理隐式 handoff）
- [x] S1：安全/信任模型文档 + break-glass 默认值（先把边界说清楚）
- [x] H1：资源预算模型（soft limit）
- [x] H3：append-only audit log（先可查询 + rotation）

### Should（P1）

- [x] N1：Session JSONL 树 + `/tree`（先只读/切 leaf，再做 branch summary）
- [x] I1/I2：EventStream 抽象 + JSONL 落盘
- [x] H4：`security --check`（运维可见性）
- [x] K1：cron orchestrator MVP（先规则）
- [x] L1：`web_search_dual`（双引擎 + 去重 + evidence summary）

### Could（P2）

- [x] H2：OS-level resource enforcement（host ulimit + docker flags）
- [x] H3：HMAC 签名 + verify CLI
- [x] J1：运行时模型覆盖（session/task）
- [x] L0：provider key pool（多 key/轮转）
- [x] L2：`web_fetch` extractor 降级链（可选 FireCrawl/FlareSolverr）
- [x] G+1：飞书策略矩阵补齐（dm/group/mentionless/命令 bypass）
- [x] N2：Hook/Extension 最小版（tool_call gate + tool_result scrub）

---

## 5) 附录：本次“新发现”来源索引（便于复盘）

- `ref/CoPaw/SECURITY.md`
- `ref/UltimateSearchSkill/README.md`
- `ref/blades/graph/task.go`
- `ref/chroma/DEVELOP.md`
- `ref/clawdbot-feishu/README.md`
- `ref/clawlet/README.md`
- `ref/feishu-openclaw/README.md`
- `ref/nanoclaw/docs/SECURITY.md`
- `ref/open-cowork/README_zh.md`
- `ref/openai-swarm/swarm/core.py`
- `ref/openclaw-mini/README.md`
- `ref/openclaw-mini/src/agent-events.ts`
- `ref/pi-mono/packages/coding-agent/docs/session.md`
- `ref/pi-mono/packages/coding-agent/docs/tree.md`
- `ref/pi-mono/packages/coding-agent/docs/extensions.md`
- `ref/picoclaw-study/README.md`
- `ref/poco-agent/executor/app/core/engine.py`
- `ref/trpc-agent-go/examples/knowledge/evaluation/knowledge_system/trpc_agent_go/trpc_knowledge/knowledge.go`
- `ref/letta/examples/notebooks/data/task_queue_system_prompt.txt`
- `ref/zeroclaw/docs/resource-limits.md`
- `ref/zeroclaw/docs/audit-logging.md`
- `ref/zeroclaw/docs/security-roadmap.md`
- `ref/nagobot/docs/proposal-v2.md`
