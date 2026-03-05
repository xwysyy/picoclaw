# ROADMAP_V4: PicoClaw 重构路线图（结构优化 + 去冗余 + Upstream 同步）

Date: 2026-03-05

本路线图基于并融合：
- [openclaw_review.md](./openclaw_review.md)：从 OpenClaw 学“闭环”与工程语义，而不是照搬全部生态
- [refactor_guide1.md](./refactor_guide1.md)：如何允许大幅重构但不继续做烂（分层、依赖方向、迁移策略）
- [ROADMAP_V3.md](./ROADMAP_V3.md)：上一版执行路线（保留为历史版本与进度记录）

本次 V4 的新增约束是：**我们已经合并了 upstream/main，并完成了冲突收敛**。因此 V4 重点从“写路线”推进到“把路线变成默认工作流”，并持续做两件事：
1. 优化结构（依赖方向正确、组合根清晰、核心逻辑可测）
2. 减少冗余（只有一个 canonical 入口、只有一套语义与类型）

---

## 0. 北极星（最终形态）

我们要的不是“功能更多”，而是一个可以长期演进的闭环系统：
- 产品闭环：`onboard -> gateway 常驻 -> status/doctor -> console -> 可预测交互`
- 运行时闭环：`session queue -> tool governance -> compaction/pruning -> trace/audit`
- 运维闭环：`config validate -> doctor -> migrate -> ready/health -> upgrade`
- 安全闭环：`sandbox/exec -> tool policy -> plan_mode/estop -> redaction -> audit log`

成功标准（必须可验证）：
- 新增一个工具/渠道，不需要触碰 AgentLoop 核心逻辑
- 出问题能定位：错误有 path、有 trace、有最小复现
- Upstream 同步不再是灾难：冲突点收敛、分支差异可解释

---

## 1. 当前状态（截至 2026-03-05）

### 1.1 已完成（与 V3 对齐，并补齐 merge 后的收敛项）

- [x] 已完成 upstream/main 合并，并清理冲突文件为一致实现（README / config.example / tests 等）
- [x] `SecretRef`（env/file/inline）作为敏感字段的 canonical 表达（避免把 token/key 固化到仓库）
- [x] Web 工具 provider 体系扩展并保持治理层一致（优先级/证据模式/抓取限制）
- [x] 工具执行上下文（channel/chat_id）采用 request-scoped context，并向后兼容 legacy key
- [x] voice transcriber 的配置检测/解析与 SecretRef 体系对齐（DetectTranscriber 逻辑可用、单测覆盖）
- [x] `config/config.example.json` 与 `pkg/config` schema 对齐且为合法 JSON（用于复制配置的单一来源）

### 1.2 已知现实约束（必须写进路线图，而不是靠“记忆”）

- 内存受限环境中 `go test ./...` 可能被 OOM kill：以 [`scripts/test.sh`](./scripts/test.sh) 作为最低门槛，并按包分拆回归测试。
- 任何涉及公网下载的命令需要代理：非交互 shell 不自动加载 `proxy_on`，必要时显式设置 `HTTP_PROXY/HTTPS_PROXY/ALL_PROXY` 或 `source ~/.zshrc && proxy_on`。

---

## 2. 反屎山原则（必须遵守的工程规则）

1. **先做 ports/adapters，不堆 helper。**
2. **Core 不能依赖 Infra。**（依赖方向错了，后面只会越补越烂）
3. **迁移必须可回滚、可验证。**
4. **去冗余是持续要求，而不是“最后再清理”。**
5. **任何 secret 不进入被 git track 的文件。**

---

## 3. Workstreams（按“收益/风险/依赖”排序）

> 规则：每个 work item 都要写清楚“验收方式”。没有验收就没有完成。

### Workstream A：Upstream 同步机制化（避免下一次合并又爆炸）

目标：把同步变成 routine，而不是一次性大手术。

行动：
- [ ] 固化同步流程文档（merge/rebase 的选择、冲突处理策略、回归测试门槛）
- [ ] 将“高冲突文件”收敛为更稳定的边界层（例如：tools registry/context、agent loop wiring）
- [ ] 每次同步必须跑：`./scripts/test.sh`

验收：
- 合并 upstream 时冲突文件数量可控
- 冲突集中在少数“适配器层文件”，而不是全仓库扩散

### Workstream B：Config/Secrets 作为系统的第一层护栏

目标：配置是系统稳定性的起点，必须做到“可校验、可诊断、可迁移、可热更新（可选）”。

行动（按收益排序）：
- [ ] 补齐 `NormalizeSecretRefs()` 覆盖面，确保所有新增 SecretRef 字段都能 normalize（相对路径、~ 展开）
- [ ] `config validate` 的错误输出保持“精确 path + 可执行建议”（避免泛化报错）
- [ ] `config/config.example.json` 保持与 schema 同步：任何新增字段必须先更新 example，再写代码

验收：
- `picoclaw config validate` 对错误字段给出精确 path
- Example JSON 可直接跑通（最小可运行配置）

### Workstream C：AgentLoop 收敛为“可测状态机”（核心循环去耦合）

目标：把 `pkg/agent` 的超大循环逐步收敛为可测状态机，并让 infra 通过端口注入。

行动：
- [ ] 继续拆分 `pkg/agent/loop.go`：按子系统拆文件但保持行为（同 package 先稳定）
- [ ] 把“渠道发送/媒体解析/工具治理/会话存取”通过 ports 注入，loop 只做调度与状态变迁
- [ ] 为关键路径补回归测试：tool loop、resume_last_task、compaction/pruning、tool error wrapping

验收：
- 使用 fake provider + fake tools 可以单测跑通完整回合（不启动 gateway、不依赖 channels/http）

### Workstream D：Tools 三段式分层（抽象 / 治理 / 实现）

目标：减少“工具实现”对核心与治理逻辑的污染，让 policy/trace/plan_mode/estop 在一个 chokepoint 生效。

行动：
- [ ] 统一 ToolContext（channel/chat_id）的读写入口，逐步弃用 legacy executionContext key（先兼容、后收敛）
- [ ] policy/trace 的默认行为“安全且可解释”：失败有结构化错误模板，必要时附 schema 概览
- [ ] 并发策略明确：默认串行，read-only 工具可并行（可配置）

验收：
- 新增工具不需要改 AgentLoop
- 任何 tool deny/timeout/redact 的行为来源可追踪（trace/audit 可定位）

### Workstream E：Channels/HTTP 适配器化（输出端统一）

目标：Core 不再“知道怎么发消息到渠道”；Core emit 事件，Infra 订阅事件发送。

行动：
- [ ] 梳理 outbound 发送路径：placeholder/edit/typing/attachments 都走同一条 adapter pipeline
- [ ] SessionKey/ThreadKey 规则统一（尤其 Telegram topic/thread、Discord guild/channel）

验收：
- 新增渠道不触碰 core loop
- 同一个会话隔离规则只存在一处实现

---

## 4. 去冗余清单（Always-On）

每次重构迭代至少干掉一种冗余：
- 规则冗余：SessionKey canonicalization 只有一个入口
- 类型冗余：同一概念只保留一套 canonical type（其余用 alias/adapter）
- 上下文冗余：ToolContext 只有一种键与一种注入方式（legacy 仅兼容期）
- 配置冗余：config.example.json 是唯一模板；defaults 只表达默认值，不重复写“第二套模板”
- provider 冗余：model_list 走统一解析与选择逻辑；legacy providers 只保兼容与迁移

---

## 5. 验收门槛（Gates）

最低门槛（必须通过）：

```bash
./scripts/test.sh
```

建议门槛（资源允许时）：

```bash
go test -p 1 ./cmd/picoclaw/... -count=1
go test -p 1 ./pkg/agent -count=1
go test -p 1 ./pkg/tools -count=1
```

运行态验收（Gateway）：

```bash
curl -sS http://127.0.0.1:18790/health
curl -sS http://127.0.0.1:18790/healthz
curl -sS http://127.0.0.1:18790/ready
curl -sS http://127.0.0.1:18790/readyz
```

