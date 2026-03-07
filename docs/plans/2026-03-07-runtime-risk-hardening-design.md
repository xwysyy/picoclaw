# Runtime Risk Hardening Design

**Date:** 2026-03-07
**Scope:** `pkg/agent`, `pkg/tools`, `pkg/session`, `pkg/providers`
**Status:** Approved approach B

## Background

当前精简后的主链已经恢复 `gateway` 入口、`/ready`、`/api/notify`、`/console/` 等关键能力，但运行时内部仍有 5 类中高优先级风险未收敛：

1. 跨 provider fallback 可能只切模型名，不切 provider 实例
2. `PlanMode / Estop / ToolPolicy` 仍主要依赖提示词语义，而不是执行层硬拦截
3. `resume_last_task` 对 `run.error` 和多 workspace 语义不够严谨
4. session 对外返回的 message 快照可能只是浅拷贝
5. tool loop 检测可能误伤合法重复操作

这些问题不阻塞 Gateway 主链，但会影响运行时正确性、可恢复性和策略约束的一致性。

## Goals

- 修复跨 provider fallback 的执行正确性
- 让 `PlanMode / Estop / ToolPolicy` 变成执行层硬约束
- 让 `resume_last_task` 只恢复“安全且合理”的未完成 run
- 保证 session 对外暴露的数据是深拷贝
- 让 tool loop 检测更保守，减少误判

## Non-Goals

- 不扩展新的 Gateway / channel / console 功能
- 不重做 provider / tool / session 整体架构
- 不在这轮引入复杂的策略 DSL 或全新审计系统

## Selected Approach

选择 **方案 B：分两批修，但在同一轮内完成**。

### Phase 1: Correctness First

优先修复“逻辑一定不该错”的问题：

1. provider fallback
2. `resume_last_task`
3. session deep copy

这三项的共同特点是：
- 对外语义比较明确
- 回归成本低于策略类修复
- 可以先把运行时 correctness 兜住

### Phase 2: Policy and Guardrails

第二批修复“名字上是强约束、实现上却偏软约束”的问题：

4. `PlanMode / Estop / ToolPolicy`
5. tool loop detection

这两项会改变运行时行为，需要在 correctness 修复稳定后再收口，以便测试与回归定位更清晰。

## Design Details

### 1. Cross-provider fallback

**Current issue**
- fallback candidate 已经能表达 `provider/model` 组合
- 但实际调用路径仍可能复用原 provider 实例，只改 `model`

**Design**
- 在 agent runtime 内，将 fallback candidate 解析为“可执行候选”而不是单纯字符串
- 执行 fallback 时，为每个候选选择对应 provider 实例
- 同 provider 候选可复用原实例；跨 provider 候选必须切到目标 provider
- `usedModel` 与 `fallbackAttempts` 继续保留，但要额外保证“实际使用的 provider 与 candidate 一致”

**Result**
- `openai/... -> anthropic/...` 这类回退会真正切 provider，而不是继续拿 OpenAI client 打 Anthropic 模型名

### 2. PlanMode / Estop / ToolPolicy hard enforcement

**Current issue**
- 这些限制现在更像 prompt-layer advisory
- 一旦模型仍然发起工具调用，执行器大概率会实际执行

**Design**
- 把限制判断前置到 `ExecuteToolCalls` 的统一 chokepoint
- 命中限制时，直接返回结构化 `ToolResult` 错误，而不是执行工具
- 保持错误输出对模型“可恢复”：明确拒绝原因、触发的策略、建议的下一步
- 执行顺序建议：`Estop` → `PlanMode` → `ToolPolicy`
  - `Estop` 是全局硬刹车，优先级最高
  - `PlanMode` 是运行模式限制
  - `ToolPolicy` 是更细粒度的策略约束

**Result**
- 这些开关从“提示词语义”收敛为“执行层硬约束”

### 3. Resume semantics

**Current issue**
- 当前只把 `run.end` 视作正常结束
- `run.error` 仍可能被当成“unfinished run”候选
- 扫描入口偏向默认 agent workspace

**Design**
- 明确 run terminal state：
  - `run.end`：正常完成，不恢复
  - `run.error`：默认视为不可自动恢复，不进入候选
  - 无 terminal event：才视为可恢复候选
- 恢复扫描范围从默认 agent workspace 扩展为“agent registry 中可见 agent 的 workspace 集合”
- 保持候选选择策略简单：在所有“可恢复候选”中选最近的一个

**Result**
- 避免把鉴权错误、配置错误、参数错误类 run 反复 resume
- 避免多 agent 部署时只能看到默认 agent 的 unfinished run

### 4. Session deep copy

**Current issue**
- 当前 message slice 本身被复制了，但嵌套字段可能仍共享底层对象

**Design**
- 在 `pkg/session` 内新增统一 clone helper：
  - clone `providers.Message`
  - clone nested `Media`
  - clone nested `ToolCalls`
  - clone `Function` / 其他嵌套引用字段
- 所有对外暴露 session/message 快照的 API 都统一走 clone helper：
  - `GetHistory`
  - `SetHistory`
  - `GetSessionSnapshot`
  - `ListSessionSnapshots`

**Result**
- 外部对返回值的修改不会污染 session manager 内存态

### 5. Tool loop detection

**Current issue**
- 当前逻辑按全历史累计重复次数判断，容易误伤轮询/重复检查类合法行为

**Design**
- 改成“最近窗口 + 连续重复 + 无明显进展”导向的保守判定
- 第一版先做最小收敛，不引入复杂进展图：
  - 只看最近 `N` 次工具签名
  - 只在连续重复达到阈值时触发
  - 不再按整个 run 累计重复次数判 loop
- 保持返回语义不变：仍向模型回灌一个 loop-detected tool error，提示改换策略

**Result**
- 降低误判率，同时保留对真实死循环的拦截

## Testing Strategy

### Phase 1
- 新增/修改 fallback 测试：验证跨 provider fallback 真切换 provider 实例
- 新增/修改 resume 测试：
  - `run.error` 不进入 resume 候选
  - 多 workspace 扫描能找到非默认 agent run
- 新增/修改 session 测试：
  - 修改返回的 `Media` / `ToolCalls` 不影响原 session

### Phase 2
- 新增/修改执行器测试：
  - `PlanMode` 命中时工具被拒绝而不是执行
  - `Estop` 命中时工具被拒绝
  - `ToolPolicy` 命中时工具被拒绝
- 新增/修改 loop 检测测试：
  - 最近连续重复会触发
  - 非连续重复/有变化的重复不触发

## Risks and Mitigations

- **Behavior change risk:** `PlanMode / Estop / ToolPolicy` 从软约束变硬约束，可能改变已有测试和某些工作流
  - **Mitigation:** 先补失败测试，再最小实现，并把错误输出做成清晰可恢复
- **Provider coupling risk:** fallback 切 provider 可能牵涉 provider factory 复用策略
  - **Mitigation:** 优先最小改造，只增加运行时 provider 选择层，不重做 factory 架构
- **Resume scope risk:** 多 workspace 扫描可能带来更多候选
  - **Mitigation:** 先保持“最近且可恢复”这一个简单选择策略

## Rollout Order

1. provider fallback
2. resume semantics
3. session deep copy
4. hard enforcement for PlanMode / Estop / ToolPolicy
5. tool loop detection
6. targeted regression tests + selected full-package validation

## Success Criteria

- 跨 provider fallback 测试能证明真实切 provider
- `run.error` 不再进入 auto resume 候选
- 多 agent workspace 的 unfinished run 可被发现
- session getter/setter 深拷贝测试覆盖嵌套字段
- `PlanMode / Estop / ToolPolicy` 命中时执行器返回拒绝结果，不执行工具
- tool loop 检测不再因为全局累计重复而误伤常见轮询行为
