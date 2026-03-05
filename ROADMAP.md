# PicoClaw Roadmap（精简版 / 单一入口）

> 更新：2026-03-05（Asia/Shanghai）
>
> 目的：把路线图保持为**可维护的单一入口**，避免多份版本文档互相重复、越写越长。

---

## 0) 当前状态

截至 2026-03-05，本仓库在 2026-03 的一轮“结构优化 + 去冗余 + Upstream 收敛”工作已完成并稳定运行：

- 可观测与可复现（tool trace / run trace / 导出 / 错误模板）
- 结构化记忆（blocks + scope + FTS/向量记忆）
- MCP Bridge（工具发现/注册 + 治理层统一）
- Durable execution（checkpoint + `resume_last_task` + 幂等保护）
- 多 Agent 协作（handoff + subagent 稳定输出 contract）
- 渠道与媒体（本地桥接 + 媒体通路与大小限制）
- Provider 层去冗余（Claude CLI provider/tool-call 解析与工具辅助函数收敛）

本文件从现在开始只维护：

- “北极星与约束”（架构方向不漂移）
- “持续演进的优先级”（少量、可验证、可回归）
- “历史资料入口”（沉淀但不污染主文档）

---

## 1) 北极星（长期目标）

PicoClaw 的差异化不靠堆功能，而是把真实长期使用的痛点做成一等公民能力：

1. Replayable Runs（可回放执行）
2. Policy-first Tools（工具策略层，安全与可解释）
3. Structured Memory（结构化记忆资产）
4. MCP Bridge（生态接入但可治理）
5. Durable Long Tasks（可恢复长任务）

---

## 2) 工程约束（反屎山规则）

这些是“默认规则”，不是建议：

- ports/adapters + 分层：Core 不能反向依赖 Infra（channels/http/media/tools 实现等）。
- 去冗余：同一概念只保留一个 canonical 入口（类型/语义/配置模板都要收敛）。
- 可回归：最低门槛固定为 `./scripts/test.sh`。
- secret 不进入被 git track 的文件（优先用 `SecretRef`：env/file）。

参考与守则：
- 架构护栏与当前落地状态：`docs/architecture.md`
- Agent 自动化执行规范（含 upstream 学习模式 + cherry-pick 搬运）：`AGENTS.md`

---

## 3) 持续演进优先级（保持短）

> 规则：只保留少量“下一步最值得做”的事；其余放到 issue/PR 里，不继续膨胀本文件。

- 继续收敛 ToolContext（channel/chat_id）注入路径：逐步弃用 legacy execution context key（先兼容、后删除）。
- 将“高冲突边界”进一步外推到 adapter 层（减少未来 upstream 同步成本）。

---

## 4) 历史资料（git 追溯）

为了减少根目录噪音，历史版本/过程文档已从工作区移除。

需要追溯历史时，直接查看 git history 即可：

- `git log --oneline -- ROADMAP.md`
- `git log --oneline -- docs/refactor_plan.md`
- `git log --oneline -- openclaw_review.md`
