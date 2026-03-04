# OpenClaw 功能复盘 & PicoClaw 改进指导（截至 2026-03-04）

> 目的：基于 OpenClaw 的**最新功能与近期变更**，给 PicoClaw 的产品/运行时/渠道/运维/安全提供一份“可落地”的改进参考。
>
> 参考基线（本次阅读）：
> - OpenClaw：`openclaw/openclaw`，Changelog 版本 `2026.3.1 ~ 2026.3.3`（含 Breaking），本地阅读时的 commit `61f7cea`。
> - PicoClaw：本仓库 `main`，对齐 `ROADMAP_V2.md`（初稿日期 2026-03-04）。

---

## 1. 我们要从 OpenClaw 学什么（而不是照搬什么）

OpenClaw 的价值不在“某一个工具/某一个渠道”，而在它把一个个人助手做成了**完整闭环**：

- **入口与产品化**：CLI Wizard（onboard）+ 常驻 Gateway + Web 控制台/WebChat +（可选）桌面/移动节点。
- **运行时工程化**：会话模型、队列并发、事件流、工具执行、压缩/重试、可观测与可回放。
- **运维可控**：`doctor`、`status/health`、更新渠道（stable/beta/dev）、配置校验、迁移与修复。
- **默认安全**：sandbox、工具策略、提升权限（elevated）、SSRF/路径/权限边界、HTTP 安全头。
- **可扩展但可验证**：插件的 manifest + JSON Schema（先验校验、无需执行代码）。

对 PicoClaw 来说，“吸收”的关键是：**把现有能力串成闭环**（诊断/迁移/安全/渠道 UX），而不是把 OpenClaw 的全部生态（移动节点、Canvas、超多插件）一次性搬过来。

---

## 2. OpenClaw 能力地图（按系统层次拆解）

> 这一节用于建立“共同语言”，后面差距分析和改进清单会直接引用这些术语。

### 2.1 产品/入口层（用户能感知到的）

- **onboard wizard**：把 Gateway/模型/渠道/技能/权限引导成“一次走通”的路径；同时写入 metadata，便于后续 doctor/upgrade。
- **update channels**：stable/beta/dev；升级后建议运行 doctor（迁移 + 修复 + 诊断）。
- **status/health**：本地（CLI）与 Gateway 探针结合；支持“深度诊断（deep）”输出，适合直接粘贴给排障同学。

### 2.2 运行时/Agent 层（工程化的“真实 Agent Loop”）

- **会话模型（Session Model）**：
  - DMs 折叠进 `main`（减少碎片化），群聊/线程/话题是隔离 session（避免污染）。
  - 每个 session 串行化执行（lane/queue），避免工具/写盘竞态。
- **事件流（Streams）**：至少拆成 `assistant/tool/lifecycle` 三路；能驱动 UI、渠道占位符、回放、调试。
- **Compaction/Retry**：压缩触发、重试路径、保留尾部诊断、避免重复输出。
- **Hooks（内置 + 插件）**：在 model resolve / prompt build / tool call / session lifecycle 等关键点插入可审计扩展点。

### 2.3 工具/扩展层（“能做什么”）

- **工具分组/档位（tool profiles + allow/deny）**：新安装默认只给“消息类/低风险”工具，编码/系统类工具需要显式开启。
- **Sandbox / Tool Policy / Elevated 三层控制**：
  - sandbox：决定“在哪里跑”（容器 vs host）。
  - tool policy：决定“能不能调用”（allow/deny/分组）。
  - elevated：仅对 exec 类提供“从 sandbox 逃逸到 host”的受控开关。
- **近期新增/强化工具点**（见 Changelog）：
  - `pdf` 工具：原生/降级提取、默认模型与大小页数限制。
  - `diffs` 工具：用于“只读差异渲染”，并支持 PDF 输出来绕过渠道压图。
  - web search provider：Perplexity Search API + 语言/地区/时间过滤（结构化结果）。

### 2.4 运维/安全层（系统要能长期跑）

- **config validate**：启动前校验配置（含 `--json`）；错误包含“精确的 invalid-key path”。
- **doctor**：
  - 迁移 legacy key（并能解释迁移做了什么）。
  - 修复常见运行时问题（权限、目录布局、服务安装、多重安装冲突）。
  - 与 Gateway 启动/升级流程绑定，减少“升级后不可用”的摩擦。
- **Secrets/SecretRef**：
  - 可把敏感值从 config 中抽离（env/file/exec provider）。
  - 运行时是“内存快照”，reload 走原子 swap；active-surface filtering（只对生效面 fail-fast）。
- **安全加固清单化**：SSRF guard、路径规范化、HTTP 安全头、workspace mount 只读等。

---

## 3. 近期更新（2026.3.1 ~ 2026.3.3）对 PicoClaw 最值得吸收的点

> 这里不是逐条抄 Changelog，而是筛选“对我们架构/体验影响最大、可迁移”的点。

### 3.1 可靠性/一致性：工具结果截断要保尾部诊断（head+tail）

OpenClaw 在 `tool-result truncation` 上从“只保头部”改成“head+tail”，核心价值：

- 许多真实故障信息在尾部（stack trace、error summary、最后几行日志）。
- 只截断头部会让排障信息消失，反而增加重试次数和人类介入成本。

对 PicoClaw 的直接建议：

- 改造 `pkg/tools/toolcall_executor.go` 的 `truncateToolResult`：由单纯 `utils.Truncate` → 支持 head+tail（可配置比例/最小尾部长度）。
- 与现有 compaction 内的“tool result condensed head+tail”策略统一（避免两套规则）。

### 3.2 “启动前发现问题”：`config validate` + invalid-key path

OpenClaw 把“配置错误”从运行时错误提前到启动前，且能输出精确 path，效果是：

- 用户/运维能快速定位 `channels.telegram.accounts.default.botToken` 这类错误字段，而不是笼统的 JSON parse error。
- 能对 CI/部署流程做“config gate”，降低升级风险。

对 PicoClaw 的建议：

- 新增 `picoclaw config validate`（支持 `--json`），并把校验逻辑复用到 gateway 启动前。
- 输出“错误路径 + 建议修复”，并和 `onboard` 生成的 config 绑定（减少手写 JSON）。

### 3.3 “升级后不崩”：doctor 迁移/修复成为默认流程

OpenClaw 的高发布频率决定了：没有 doctor，用户就会被 breaking change 反复击穿。

对 PicoClaw 的建议：

- 以 `doctor` 思路补齐我们缺的“升级闭环”：迁移（schema/key）+ 权限/目录检查 + 服务/容器状态检查 + 可选 repair。
- PicoClaw 已有 `migrate --from openclaw`，但 doctor 面向的是**本项目自身的长期演进**（把 breaking change 变成可控迁移）。

### 3.4 “渠道 UX”从功能到细节：typing / reaction / 占位符

近期 OpenClaw 在 Slack/Telegram/Discord 上补了许多“让用户感觉可靠”的细节（例如 Slack DM 里用 reaction 表示处理中、Telegram streaming 默认开启等）。

对 PicoClaw 的建议：

- 我们的 `pkg/channels` 已有 Typing/Reaction/Placeholder 能力接口与 Manager 编排（见 `pkg/channels/README.zh.md`），应继续把“统一体验”做实：
  - 每个渠道至少实现一种“正在处理”的反馈（typing 或 reaction 或 placeholder）。
  - 对“长回答/慢工具”提供一致的流式预览策略（编辑消息/分段/卡片等）。

### 3.5 “更细粒度的会话隔离”：Telegram 话题（topic）路由到专用 agent

OpenClaw 增加了 Telegram forum group / DM topics 的 per-topic `agentId` 覆盖，价值是：

- 同一个群的不同 topic 可以是不同“工作流/人格/工具权限”的 agent。
- 同一渠道内也能保持 session 隔离，避免上下文污染。

对 PicoClaw 的建议：

- 在 Telegram/Discord/Slack 等支持 thread/topic 的渠道上，完善 `sessionKey` 的分层结构（例如 `...:thread:<id>` / `...:topic:<id>`）。
- 为“topic → agentId”提供配置映射（并有默认值）。

---

## 4. OpenClaw vs PicoClaw：差距分析（截至 2026-03-04）

> 这一节把“我们已经有的能力”也写清楚，避免重复造轮子。

| 能力域 | OpenClaw（概念） | PicoClaw 现状 | 差距/机会（建议方向） |
|---|---|---|---|
| 安装/引导 | onboard wizard 一条龙 | `picoclaw onboard` 已有 | 强化：把“选择工具档位/安全默认/渠道授权/健康检查”也纳入引导，并沉淀 metadata 供 doctor 使用 |
| 升级闭环 | update + doctor 强绑定 | 目前更偏“手动升级 + 自查” | 增加 doctor：迁移 + 修复 + 检查；并把升级风险收敛为“可执行步骤” |
| 配置校验 | `config validate` + path | 以运行时报错为主 | 增加 config validate；错误包含 json path；作为 gateway 启动前置 gate |
| Secrets | SecretRef（env/file/exec）+ 原子快照 | 主要是明文 key（或 env 注入） | 设计一个轻量 SecretRef（先 env/file），逐步扩展 exec；引入 active-surface filtering |
| 会话模型 | main 折叠 + 群/线程/topic 隔离 | 已有 sessionKey，但 thread/topic 覆盖不足 | 强化 thread/topic；统一 sessionKey 规范化；避免跨面 duplicate 回复 |
| 事件流 | assistant/tool/lifecycle streams | trace 有，但“事件语义”偏分散 | 向统一 EventStream taxonomy 收敛，驱动 UI/渠道占位符/回放 |
| 工具结果处理 | head+tail truncation 等 guard | `truncateToolResult` 目前偏“截头” | 改为 head+tail；对 error 结果优先保留尾部；统一 compaction 与 tool executor 策略 |
| 渠道覆盖 | 超多渠道 + 插件扩展 | 已覆盖 Telegram/Discord/Slack/LINE/QQ/钉钉/企业微信/WhatsApp 等 | 继续扩展不是第一优先；优先补齐“每个渠道的可靠 UX + thread/topic + media 端到端” |
| 插件系统 | manifest + JSON schema + hooks | skills/MCP 已有，但插件形态不同 | 先把“技能/扩展”做成可验证（schema/contract）与可诊断（doctor）；再评估是否需要 OpenClaw 式插件系统 |
| 安全默认 | sandbox + policy + elevated + SSRF/路径/HTTP 头 | 我们已有 policy/plan-mode/审计/部分安全头 | 补齐：HTTP 安全头（Permissions-Policy 等）、更清晰的“沙盒/策略/提权”说明与解释命令（explain） |

---

## 5. PicoClaw 改进清单（优先级建议）

> 这里按“收益/风险/工作量”排序，便于拆分成小 PR 并持续交付。

### P0（1~2 周）低风险高收益：先把“稳定 + 可诊断”补齐

1) **工具结果截断 head+tail（优先做）**  
   - 目标：保留 tail diagnostics；避免误判/重复重试。  
   - 落点建议：`pkg/tools/toolcall_executor.go`（并补单测）；同时复核 `pkg/agent/compaction.go` 的一致性策略。

2) **SessionKey 规范化（canonicalization）**  
   - 目标：避免 CLI/Gateway/UI 对 sessionKey 大小写/空白处理不一致导致“丢流/丢事件/找不到历史”。  
   - 落点建议：集中在 `pkg/session` 或 `pkg/agent` 提供 `CanonicalSessionKey()`，全链路只使用 canonical key。

3) **Gateway readiness + 基础安全头**  
   - 目标：更适合容器编排与安全基线；减少误报与攻击面。  
   - 建议：在现有 `/health` 基础上补 `/readyz`；补齐 `Permissions-Policy`、`X-Frame-Options`、`Referrer-Policy` 等。

4) **`picoclaw config validate`（含 `--json`）**  
   - 目标：让配置问题在启动前暴露，并输出“精确字段路径”。  
   - 建议：作为 gateway 启动前置检查；CI/部署时也可直接调用。

### P1（1~2 月）中等工程量：做“升级闭环 + 安全闭环”

1) **doctor：迁移 + 修复 + 深度诊断**  
   - 做法：先实现“只读扫描 + 建议”，再加 `--repair/--yes`。  
   - 覆盖范围建议：目录结构、权限、服务/容器状态、legacy config key、版本兼容提示。

2) **SecretRef（轻量版）**  
   - 先 env/file 两种 provider；把“运行时解析”做成原子快照 + active-surface filtering。  
   - 后续再加 exec provider（以及安全边界：绝对路径、无 shell、env allowlist）。

3) **事件模型收敛：EventStream taxonomy**  
   - 把 run 中的关键信号统一成类型化事件：message_delta/tool_start/tool_end/retry/compaction/steering/...  
   - 直接收益：Web console、渠道占位符、审计/回放都可以吃同一份事件流。

4) **渠道 thread/topic 细化（以 Telegram 为先）**  
   - 支持 topic/thread 级 sessionKey；支持 topic → agent 映射；对群聊触发策略做统一抽象。

### P2（3~6 月）大工程：可扩展与产品化深化（按需）

1) **插件化（manifest + JSON schema + hook）**  
   - 如果要做：优先做“可验证（schema）+ 可诊断（doctor）”，再做“可执行（runtime）”。  
   - 避免直接引入“运行时动态 import”导致启动性能与安全边界复杂化。

2) **更完整的 Control UI/WebChat 闭环**  
   - 让 UI 真正成为“诊断 + 操作 + 回放”的控制面，而不是仅展示。

---

## 6. 建议的 PR 切分（把大目标拆成可合并的小步）

> 经验：每个 PR 只解决一个“闭环点”，并自带最小回归测试/验收方式。

- PR-01：tool result 截断改为 head+tail（含单测）
- PR-02：SessionKey canonicalization（含 CLI/gateway/UI 覆盖点梳理）
- PR-03：Gateway `/readyz` + 安全头（含 curl 验收）
- PR-04：`picoclaw config validate`（含 `--json` + 错误路径）
- PR-05：Telegram topic/thread sessionKey +（可选）topic→agent 映射（先 config + sessionKey，后路由）
- PR-06：doctor（只读扫描版）+ 输出建议（后续再加 `--repair`）
- PR-07：SecretRef（env/file）+ 原子快照 + active-surface filtering

---

## 7. 持续跟踪 OpenClaw 更新的机制（建议流程）

OpenClaw 更新频率很高（接近日更），建议我们把“吸收更新”变成流程：

1) **每周固定扫一次 changelog（近 7 天）**  
   - 分类：`security` / `ops` / `channels` / `runtime` / `tools` / `breaking`  
   - 每类只挑 1~3 条“对我们有直接收益”的进入 backlog。

2) **为 PicoClaw 建一个“对齐标签”**  
   - 例如：`openclaw-parity`, `openclaw-security`, `openclaw-ops`, `openclaw-channels`。

3) **迁移工具也要跟着动**  
   - 我们已有 `pkg/migrate/sources/openclaw`；OpenClaw schema 变更时，至少保证“读取不崩 + 忽略未知字段 + 发出 warnings”。

---

## 8. 参考阅读入口（给深入同学）

建议按“先闭环、后细节”的顺序读：

- OpenClaw `CHANGELOG.md`：优先关注 Breaking 与 Fixes（很多是生产事故后的 hardening）
- `docs/gateway/doctor.md`：doctor 的迁移/修复思路很适合我们借鉴
- `docs/gateway/secrets.md`：SecretRef + active-surface filtering 的落地细节
- `docs/gateway/sandbox-vs-tool-policy-vs-elevated.md`：三层控制如何向用户解释
- `docs/channels/group-messages.md`：mention gating、group session 隔离、pending-only 注入等渠道 UX 细节
- `docs/concepts/agent-loop.md`：事件流/队列/运行时边界的“工程化说明”

---

## 9. 飞书（Feishu/Lark）专项：值得对齐的能力与改进路线

> 说明：本节聚焦“飞书作为企业协作主战场”时，OpenClaw 做对了哪些细节，以及 PicoClaw 应该优先补哪些闭环。

### 9.1 OpenClaw 的飞书能力（我们应重点吸收的点）

**A) 接入形态：WebSocket 长连接（不依赖公网 Webhook）**

- Feishu/Lark bot 通过事件订阅的 **WebSocket 长连接**接入，能在本地/内网运行，不需要暴露公网回调。
- 对于企业内网环境，这比“Webhook + 公网服务”更贴近现实部署形态。

**A2) 多账号 / 多租户与国际版（Lark）**

- 多账号（`channels.feishu.accounts` + `defaultAccount`）是企业落地的常见诉求：同一套 Gateway 连接多个飞书应用/租户。
- 支持 Lark（国际版）域名切换：`domain: "lark"`（以及自定义 `https://...` 域名形态），并允许 per-account 覆盖。
- 支持连接模式切换：`connectionMode: "websocket" | "webhook"`，webhook 模式要求 `verificationToken`（且 SecretRef 也可用）。

**B) 会话与路由：DM 主会话 + 群聊隔离 + thread/topic**

- DM 通常折叠到 `main`，避免单聊碎片化；群聊 session 隔离，避免污染。
- 支持 thread/topic 的 session 细分与回复锚定（root/thread），保证“在话题里说话”和“上下文隔离”同时成立。

补充细节（很值得抄“工程语义”）：

- **群聊 session scope** 有多档可选：`group` / `group_sender` / `group_topic` / `group_topic_sender`（对应不同隔离粒度与成本）。
- **topic session key 稳定性**：优先用 `root_id`，缺失时再用 `thread_id`；为“首轮创建 thread，下一轮才有 root/thread”的情况，能保持同一话题落在同一个 session。
- **Lark 私聊**会出现 `chat_type: "private"`（不是 `p2p`）；OpenClaw 直接按“非 group 都当 DM”处理，避免把 Lark 私聊误判成群聊。

**C) 体验与抗打扰：pairing、@mention、typing/streaming**

- DM 默认 `pairing`：陌生人先拿配对码，通过后再聊天（兼顾可用与安全）。
- 群聊默认 `requireMention`：只在 @ 提及后响应（低打扰）。
- 交互卡片 streaming（可选）+ block-level streaming：慢模型/慢工具时，用户能看到进度，不会以为“卡死”。
- typing 指示与 sender name 解析都有 quota/开关控制（默认开启，但可禁用以降低 API 开销）。

补充细节：

- **多机器人群聊的 @mention 误触发**：OpenClaw 会用 *bot open_id + botName 双重校验*，避免飞书 WS remapping 导致“别人 @ 另一个机器人，我也响应”。
- **typing indicator 的 backoff/circuit breaker**：对 429/配额等错误做显式识别并停止 keepalive，避免在限流时疯狂重试造成更差体验。

**D) “飞书=协作 OS”：文档/Drive/Wiki/Bitable/Task 工具化**

- 飞书真正的高价值不止聊天：更在**文档、表格、任务、知识库**。
- OpenClaw 的 Feishu 生态（插件/工具）覆盖 `feishu_doc` / `feishu_chat` 等，能把“对话 → 落地到文档/任务”变成闭环。

**E) 媒体与安全：收图/收视频/回传 + SSRF/本地路径防护**

- 入站 media：`messageResource.get` 下载（覆盖 image/file/audio/video；video/media 同时有 `file_key` 与缩略图 `image_key` 时优先用 `file_key`）。
- 出站 media：上传时对**非 ASCII 文件名**做 percent-encode（避免 SDK multipart silently fail）。
- URL/本地路径媒体加载：走 SSRF guard + 本地根目录 allowlist（防止任意文件读取与内网探测）。

### 9.2 PicoClaw 当前飞书能力（截至 2026-03-04）

**飞书 Channel（已实现）**：`pkg/channels/feishu/*`

- ✅ WebSocket 模式收消息（基于 `larksuite/oapi-sdk-go`）
- ✅ 出站默认使用 Interactive Card（markdown）发送（`buildMarkdownCard`）
- ✅ Placeholder（Thinking…）+ EditMessage（结果回填）闭环（`SendPlaceholder` + `EditMessage` + Manager `preSend`）
- ✅ 入站媒体下载（image/file/audio/video）→ MediaStore；工具输出 media refs 可回传飞书（`SendMedia`）
- ✅ 群聊触发：通过 `@` 检测 + `BaseChannel.ShouldRespondInGroup`（默认“被 @ 才响应”）

**飞书工具（已实现）**：`pkg/tools/feishu_calendar.go`

- ✅ `feishu_calendar`：创建日历事件（支持时区/提醒/参会人等）

**当前主要缺口（影响体验/可运营/可扩展）**

- ❗缺少 DM `pairing`（只能靠 `allow_from` 静态名单；对企业内“先用起来再授权”不友好）
- ❗缺少 thread/topic 级 sessionKey（群聊默认一个 session，话题上下文容易串）
- ❗没有 typing keepalive（目前只有 reaction + placeholder；在超慢请求时“存在感”不足）
- ❗没有“飞书协作工具”闭环（doc/drive/wiki/bitable/task/chat 成员查询等）
- ⚠️ outbound 文本长度/格式的工程细节还可加强（长消息、链接、文件名编码）
- ⚠️ Lark（国际版）域名/多账号等配置能力不足（目前只有单 AppID/AppSecret；且 `chat_type: "private"` 目前会被我们误判为群聊）

### 9.3 PicoClaw 在飞书上“最该先补”的工程细节（优先排雷）

0) **先修一个确定性 bug：Lark 私聊 `chat_type: "private"` 要按 DM 处理**  
   - 现状：我们仅把 `chat_type == "p2p"` 当 DM，`private` 会落到 group 分支，触发“需要 @ 才响应”的逻辑，导致 Lark 私聊体验直接不可用。  
   - 参考方向：对齐 OpenClaw：`chat_type !== "group"` 都视作 direct message（至少覆盖 `p2p` + `private`）。

1) **@mention 语义不要简单丢弃（上下文保真）**  
   - 现状：入站处理中会 strip 掉 mention placeholder，导致“谁被提及/提及了谁”信息丢失。  
   - 参考方向：把 mention placeholder 规范化为 `@Name` 或带 id 的显式标签（OpenClaw 甚至会保留 `<at user_id=...>name</at>` 语义），既能用于触发判断，也能保留给模型做上下文理解。  
   - 相关实现点：`pkg/channels/feishu/feishu_64.go`（入站）+ `pkg/channels/feishu/mentions.go`（已有可复用逻辑，但当前未接入）。

2) **出站 markdown 的“飞书兼容性”要收敛成一处**  
   - 我们已有 `normalizeFeishuMarkdownLinks`（用于裸链/特殊字符处理），但目前未在发送路径里使用。  
   - 建议：在 `Send`/`EditMessage` 的入参统一做一次规范化，避免不同路径格式漂移。  
   - 相关实现点：`pkg/channels/feishu/markdown.go`（已有）+ `pkg/channels/feishu/feishu_64.go`（Send/EditMessage）。

3) **长回复的长度上限与切分策略**  
   - 现状：Feishu 未实现 `MaxMessageLength()`，Manager 不会自动切分；长内容可能直接被 API 拒绝。  
   - 建议：实现 `channels.MessageLengthProvider`，并配合 `SplitMessage`（保代码块完整）做切分；必要时对“卡片 markdown”做更保守的阈值。

4) **中文/特殊字符文件名的上传兼容性**  
   - OpenClaw 最近专门修过“非 ASCII 文件名 multipart 上传”的兼容问题。  
   - 我们应补单测覆盖“中文文件名/括号/空格”等场景，必要时做 percent-encode 或安全替换，避免飞书把附件降级成纯文本链接。

5) **Lark（国际版）域名与代理网络**  
   - OpenClaw 明确支持 `domain: lark` 等配置，并在代理环境下给 WS 客户端注入 proxy agent。  
   - 我们目前没有 domain/proxy 配置项；如果目标用户包含海外/跨境团队，这是实打实的可用性缺口。

6) **音频/视频的 msg_type 选择（媒体 UX）**  
   - 现状：我们出站媒体统一按 `msg_type="file"` 发送，音频会显示为“文件”而不是“语音/音频”。  
   - 参考方向：对齐 OpenClaw：opus/ogg 走 `msg_type="audio"`，视频仍可用 `file`（飞书侧更稳定）。

7) **多机器人群聊的误触发（@mention 更可靠）**  
   - OpenClaw 的经验是：仅靠 open_id 可能在某些群触发误判，需要引入“botName 二次校验”或更强的 mention 语义解析。  
   - PicoClaw 可以先做轻量版：在拿到 bot info 时一并缓存 bot 显示名；当 mention.name 与 botName 不一致时不视为 @ 到自己。

### 9.4 飞书改进路线（按 PR 可落地切分）

> 以下是“对齐 OpenClaw 的关键体验”但仍符合 PicoClaw 轻量定位的版本：先补闭环，再做大而全的协作工具。

**P0（1~2 周）：把飞书聊天体验做成“稳 + 可用”**

- PR-FEISHU-01：入站 mention 规范化（不丢语义）+ 群聊触发更可靠（复用 `mentions.go`）  
  - 目标文件：`pkg/channels/feishu/feishu_64.go`、`pkg/channels/feishu/mentions.go`
- PR-FEISHU-02：出站 markdown 统一规范化（链接/特殊字符）  
  - 目标文件：`pkg/channels/feishu/feishu_64.go`、`pkg/channels/feishu/markdown.go`
- PR-FEISHU-03：实现 Feishu `MaxMessageLength()` + 长回复切分（避免 API 拒绝）  
  - 目标文件：`pkg/channels/feishu/feishu_64.go`（实现接口即可），必要时加测试
- PR-FEISHU-04：文件名上传兼容性（中文/特殊字符）+ 单测  
  - 目标文件：`pkg/channels/feishu/feishu_64.go`（sendFile）+ `pkg/channels/feishu/*_test.go`

**P1（1~2 月）：企业场景必备的“可运营 + 可扩展”**

- PR-FEISHU-05：可选 sender display name 解析（带开关，默认可关）  
  - 价值：群聊里把“谁说的”暴露给模型，提高协作可用性；同时控制 quota。
- PR-FEISHU-06：thread/topic 级 sessionKey（root/thread id）  
  - 价值：一个群多个话题不串；同时为后续“按话题路由到不同 agent”打基础。
- PR-FEISHU-07：WS 代理/域名支持（Lark + proxy 环境可用）  
  - 价值：跨境网络与企业代理环境兼容。

**P2（3~6 月）：把飞书变成“协作工作台”**

- PR-FEISHU-08：最小 `feishu_doc` 工具（先做到“创建/追加 markdown/上传图片”）  
  - 目标：把对话结果直接落到飞书文档里，形成可交付产物。
- PR-FEISHU-09：`feishu_chat` / 成员信息工具（只读优先）  
  - 目标：更好的群聊上下文（成员、群信息）与可审计的权限边界。
- PR-FEISHU-10：DM pairing（可选，默认 off）  
  - 目标：提升企业落地的开箱体验与安全默认。

> 辅助参考（工程细节很实用）：
> - `ref/feishu-openclaw`：媒体通路与本地桥接的“稳定性细节”（收图/收视频/回传、白名单等）
> - `ref/clawdbot-feishu`：飞书插件的权限清单与 doc/drive/wiki/bitable/task 工具覆盖面
