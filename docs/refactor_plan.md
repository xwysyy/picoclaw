# PicoClaw 彻底重构计划（基于 `openclaw_review.md` / `refactor_guide1.md`）

> 日期：2026-03-04（以仓库内文档为基线）  
> 目标：把当前“功能堆叠导致的耦合与屎山趋势”收敛成 **清晰边界 + 可测试核心 + 可演进基础设施** 的长期形态，并且让每一步都能 `go test` 回归。

---

## 0. 背景与痛点（来自两份文档的共同结论）

### 0.1 我们要学 OpenClaw 的“闭环能力”，不是照搬生态
来自 [openclaw_review.md](../openclaw_review.md) 的结论：OpenClaw 的核心竞争力是闭环（onboard/doctor/validate/安全默认/可观测），而不是“渠道多、插件多”。  
因此重构目标必须围绕：
- **运行时工程化**：会话模型、队列并发、事件流、工具执行、压缩/重试、可回放/可诊断
- **运维闭环**：validate/doctor/status/health、升级迁移与修复
- **默认安全**：sandbox/tool policy/elevated 的清晰边界 + SSRF/路径/权限护栏

### 0.2 当前 PicoClaw 的主要屎山成因
来自历史重构指南的诊断（并与代码现状吻合；历史版本已移除，可在 git history 中追溯）：
- `pkg/agent` 单包职责混杂，核心 loop 反向依赖 channels/media/http 等基础设施概念，测试隔离困难。
- `pkg/tools` 抽象/治理/实现混在一起，生产治理（policy/trace/timeout）不易收口。
- 组合根（wiring）分散且容易膨胀，生命周期边界不清。

---

## 1. 重构总目标（必须可验收）

### 1.1 目标架构（最终形态）
采用 **分层 + 端口(interfaces) + 适配器(adapters)**：

- **Core（核心层）**：Agent Loop / Session / Context / Tools 抽象 / Provider 抽象 / 事件模型  
  约束：不直接依赖 channels/http/media/exec 等基础设施实现；只依赖标准库和少量基础类型包。
- **Extended（扩展层）**：Memory/Skills/Heartbeat/Cron 等可选能力  
  约束：只能依赖 Core（或极少通用 util）。
- **Infra（基础设施层）**：Channels / Providers 实现 / Tools 实现 / Store / HTTP handlers / Media / Audit  
  约束：实现 Core 的端口接口，不把业务策略写死在 infra。
- **App（应用层 / 组合根）**：CLI/Gateway 的装配、生命周期、热更新、信号处理

> 目录最终建议参考 `refactor_guide1.md` 第 4 节（`internal/{core,extended,infra,app}`）。

### 1.2 核心不变式（重构中必须保持）
- Session JSONL/meta 的格式与字段兼容（或提供迁移）。
- Gateway HTTP 接口语义不破坏（至少 `/health`、`/ready`、`/api/notify`、`/api/resume_last_task` 保持）。
- Tool call 执行顺序、policy/trace 的行为不倒退。
- 渠道收发基本链路可用（至少现有 enabled channels 不因 refactor 崩）。

### 1.3 验收标准（每阶段都可判定“已完成”）
- 依赖方向满足分层矩阵（见 `refactor_guide1.md` 3.4）。
- 核心逻辑能用 fake provider + fake tools 做单测，不依赖 channels/http。
- `go test` 可在受限资源环境运行（避免单包测试被 OOM/KILL）。
- 新增/修改一个 channel/tool/provider 不需要改 AgentLoop 核心逻辑（或改动显著收敛）。

---

## 2. 分阶段实施路线（每阶段都能合并、可回滚）

> 原则：先建立护栏，再迁移核心，再拆工具/渠道，最后清理旧代码。

### Phase 0：护栏与回归基线（不改业务）
交付：
- `docs/architecture.md`：分层与依赖规则写死（“不可违反”）。
- 架构守则自动检查：新增 `internal/archcheck` 的测试，用静态扫描禁止核心层 import infra。
- 建立回归命令集：提供 `scripts/test.sh` 分组跑关键测试，避免 `go test ./...` 因资源限制被 kill。

### Phase 1：抽出 Core 的 canonical types + ports（接口层）
交付：
- `internal/core/*` 定义 Provider/Tool/SessionStore/EventStream 等端口（先够用，不超前）。
- 旧实现通过 adapter 方式实现接口（不一次性搬迁所有实现）。

### Phase 2：迁移 Agent Loop（最关键的一刀）
交付：
- 把 AgentLoop 的“外部依赖”收敛为接口：渠道目录、媒体解析、事件 sink。
- 核心 loop 以事件流输出进度（assistant/tool/lifecycle），Gateway/Channels 订阅发送。
- `pkg/agent` 逐步变成 facade（旧 API 仍可用），内部转调 core。

### Phase 3：重构 Tools（抽象/治理/实现分离）
交付：
- `core/tools`：Tool 抽象 + registry + executor + policy/guard middleware。
- `infra/tools`：shell/fs/web/cron/skills/subagent 等实现各自独立包，避免 `pkg/tools/*.go` 无限膨胀。
- 对齐 OpenClaw 的 P0 可靠性项：tool result 截断改为 head+tail（并统一 compaction/tracing 策略）。

### Phase 4：Channels/HTTP API 输出适配器化
交付：
- inbound：渠道消息 → 统一事件/输入模型
- outbound：core 事件 → 渠道发送（typing/placeholder/streaming）
- AgentLoop 不 import channels/httpapi

### Phase 5：清理收口
交付：
- 删除迁移完成的旧代码/桥接层
- 收敛 `pkg/*` public surface（不需要对外 SDK 就尽量薄）
- 目录即地图：让新人能靠目录结构理解系统

---

## 3. 本次落地范围（本轮重构会直接做什么）

> 你要求“先计划，再彻底重构”。上面是完整计划；本轮会优先落地对代码质量提升最大且风险可控的部分（Phase 0 + Phase 2/3 的关键切口）。

本轮具体交付清单：
- 工具结果截断：`utils` 增加 head+tail 截断；`pkg/tools` executor 统一使用；补单测。
- AgentLoop 解耦 channels：把 `pkg/agent` 对 `pkg/channels` 的直接依赖改为端口接口 + adapter（纠正依赖方向）。
- 拆分 `pkg/agent/loop.go` 的巨型文件：把 slash commands、reasoning 相关逻辑拆到独立文件（同 package，行为不变）。
- 增加架构守则测试：防止未来再把 channels/httpapi 反向 import 进 agent 核心。
- 修复/减压 `pkg/tools` 测试在低内存环境被 kill 的问题（定位并让默认 `go test ./pkg/tools` 可跑）。

---

## 4. 风险与控制
- 大范围移动目录/包名会引发大量 import 变更与回归风险；因此采用“接口解耦 + facade 迁移”方式，先把依赖方向纠正，再逐步迁移到 `internal/`。
- 所有改动都以单元测试与关键链路 smoke tests 作为门槛；必要时用 feature flag 保留旧路径。
