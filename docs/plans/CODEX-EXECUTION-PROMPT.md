# Codex 执行提示词

> 将此文件内容作为 Codex 的输入提示词。

---

你是一个高级 Go 工程师，现在需要对 X-Claw 项目执行一轮全面的重构优化。所有设计和需求文档已经准备好，你的任务是按计划逐步执行实现。

## 必读文档（按优先级排序）

1. **需求文档**（最重要，定义了"做什么"和验收标准）:
   `docs/plans/2026-03-07-refactor-requirements.md`

2. **技术实现文档**（定义了"怎么做"，含具体代码修改方案）:
   `docs/plans/2026-03-07-refactor-implementation.md`

3. **运行时风险加固实现计划**（Phase 1a 的详细 TDD 步骤）:
   `docs/plans/2026-03-07-runtime-risk-hardening.md`

4. **运行时风险加固设计**（Phase 1a 的设计背景和方案选型）:
   `docs/plans/2026-03-07-runtime-risk-hardening-design.md`

5. **全面审查报告**（背景参考，问题发现的原始证据）:
   `docs/plans/2026-03-07-comprehensive-refactor-design.md`

## 执行规则

### 1. 任务分解与进度追踪

在开始实施前，先在 `docs/plans/PROGRESS.md` 中创建任务清单，格式如下：

```markdown
# 重构执行进度

## Phase 1a: 运行时正确性修复
- [ ] Task 1a.1: 跨 provider fallback 切换实例 (REQ-RT-001)
- [ ] Task 1a.2: resume_last_task 语义收紧 (REQ-RT-002 + REQ-PERF-002)
- [ ] Task 1a.3: Session 快照深拷贝 (REQ-RT-003)
- [ ] Task 1a.4: PlanMode/Estop/ToolPolicy 硬拦截 (REQ-RT-004)
- [ ] Task 1a.5: Tool loop 检测保守策略 (REQ-RT-005)

## Phase 1b: Bug 修复与安全加固
- [ ] Task 1b.1: Goroutine 泄漏修复 (REQ-BUG-001 + REQ-BUG-006 + REQ-BUG-011)
- [ ] Task 1b.2: HTTP Body/JSON 错误处理 (REQ-BUG-002 + REQ-BUG-003 + REQ-BUG-010)
- [ ] Task 1b.3: 会话 GC 机制 (REQ-BUG-005)
- [ ] Task 1b.4: DB 连接泄漏 + parseFlexibleInt (REQ-BUG-007 + REQ-BUG-004)
- [ ] Task 1b.5: Model Downgrade 竞态 + copyFile 语义 (REQ-BUG-008 + REQ-BUG-009)
- [ ] Task 1b.6: 消除 stdout 输出 (REQ-SEC-003)

## Phase 2: 文件拆分
- [ ] Task 2.1: 拆分 pkg/config/config.go (REQ-ARCH-001)
- [ ] Task 2.2: 拆分 pkg/agent/memory.go (REQ-ARCH-002)
- [ ] Task 2.3: 拆分 pkg/agent/loop.go (REQ-ARCH-003)
- [ ] Task 2.4: 拆分 pkg/channels/manager.go (REQ-ARCH-004)
- [ ] Task 2.5: 拆分 console.go + toolcall_executor.go (REQ-ARCH-005)

## Phase 3: 代码质量提升
- [ ] Task 3.1: 提取魔法数字为命名常量 (REQ-QUAL-001)
- [ ] Task 3.2: 消除 Channel 间重复代码 (REQ-QUAL-002)
- [ ] Task 3.3: 统一错误处理规范 (REQ-QUAL-003)
- [ ] Task 3.4: 降低 handleCommand 圈复杂度 (REQ-QUAL-004)

## Phase 4: 性能优化
- [ ] Task 4.1: 字符串拼接优化 (REQ-PERF-001)
- [ ] Task 4.2: JSON 序列化优化 (REQ-PERF-003)
- [ ] Task 4.3: HTTP 客户端复用 (REQ-PERF-004)

## Phase 5: 测试补充
- [ ] Task 5.1: memory 模块测试 (REQ-TEST-001)
- [ ] Task 5.2: session GC 测试 (REQ-TEST-002)
```

每完成一个 Task，立刻更新 `PROGRESS.md`：将 `[ ]` 改为 `[x]`，并追加完成时间和简要说明。

### 2. 每个 Task 的执行流程

对每个 Task，严格按以下步骤执行：

1. **读取对应需求**: 从需求文档中找到该 Task 对应的 REQ 编号，确认验收标准
2. **读取实现方案**: 从技术实现文档中找到具体的代码修改方案
3. **对于 Phase 1a 的 Task**: 严格按照 `runtime-risk-hardening.md` 中的 TDD 步骤执行（写失败测试 → 验证失败 → 最小实现 → 验证通过）
4. **对于其他 Task**: 先实现修改，再运行测试验证
5. **验证**: 每个 Task 完成后运行 `go build ./...` 和相关包的 `go test`
6. **更新进度**: 在 `PROGRESS.md` 中标记完成

### 3. 并行执行策略

以下 Task 组之间相互独立，可以使用 Agent 并行处理：

**Phase 1a 内部可并行组**:
- 组 A: Task 1a.1 (fallback) — 改 `pkg/agent/loop.go`, `pkg/providers/fallback.go`
- 组 B: Task 1a.3 (deep copy) — 改 `pkg/session/`
- 组 C: Task 1a.4 (policy) — 改 `pkg/tools/toolcall_executor.go`
- （1a.2 和 1a.5 都改 `pkg/agent/loop.go`，与组 A 串行）

**Phase 1b 内部可并行组**:
- 组 D: Task 1b.1 (goroutine) — 改 `pkg/channels/manager.go`
- 组 E: Task 1b.2 (JSON/HTTP) — 改 `pkg/auth/oauth.go`, `pkg/httpapi/console.go`
- 组 F: Task 1b.3 (session GC) — 改 `pkg/session/manager.go`
- 组 G: Task 1b.4 + 1b.5 + 1b.6 — 改 `pkg/agent/memory.go`, `pkg/agent/loop.go`, `pkg/agent/run_pipeline_impl.go`, `pkg/tools/shell.go`

**Phase 2 内部可并行组**（每个 Task 改不同的包，全部可并行）:
- Task 2.1 (config) / Task 2.2 (memory) / Task 2.3 (loop) / Task 2.4 (manager) / Task 2.5 (console+executor)

**Phase 3 + 4 可并行组**:
- Task 3.1 + 4.1 + 4.2（独立文件修改）
- Task 3.3 + 3.4（可能有文件重叠，串行）

建议的并行执行顺序:
```
Round 1: [1a.1] [1a.3] [1a.4] 并行
Round 2: [1a.2] [1a.5] 串行（都改 loop.go）
Round 3: [1b.1] [1b.2] [1b.3] [1b.4+1b.5+1b.6] 并行
Round 4: [2.1] [2.2] [2.3] [2.4] [2.5] 全部并行
Round 5: [3.1+4.1+4.2] 并行 | [3.2] [3.3] [3.4] 串行
Round 6: [5.1] [5.2] 并行
```

### 4. 每个 Phase 之间的门禁验证

每个 Phase 的所有 Task 完成后，必须执行门禁验证才能进入下一个 Phase：

```bash
go build ./...
go vet ./...
go test ./... -count=1
```

Phase 1a 额外需要:
```bash
go test ./pkg/agent -run 'TestAgentLoop_.*Fallback.*Provider|TestFindLastUnfinishedRun|TestResumeLastTask|TestDetectToolCallLoop' -count=1
go test ./pkg/tools -run 'TestExecuteToolCalls_(PlanMode|Estop|ToolPolicy)' -count=1
go test ./pkg/session -run 'Test.*DeepCopy' -count=1
```

Phase 1b 额外需要:
```bash
go test -race ./pkg/channels/ ./pkg/session/ ./pkg/agent/ ./pkg/httpapi/ -count=1
```

如果门禁失败，修复后再次验证，直到全部通过。

### 5. 关键约束

- **不要改变任何现有的公开 API**（函数签名、struct 字段、JSON tag）
- **不要改变任何现有行为**，除非是明确标记为 Bug 的修复
- **不要在同一次修改中混合不同 Phase 的变更**
- **文件拆分（Phase 2）只做代码移动**，不改变逻辑
- **如果测试不通过，先分析原因再修复**，不要跳过测试
- **如果需要代理下载依赖**: `source ~/.zshrc && proxy_on && <command>`
- **不要自行创建 git commit**，除非我明确要求

### 6. 完成标志

当 `PROGRESS.md` 中所有 Task 都标记为 `[x]` 且最终门禁验证通过时，任务完成。在 `PROGRESS.md` 末尾追加最终验证结果。

现在开始执行。先阅读上述 5 份文档，然后创建 `PROGRESS.md`，然后从 Phase 1a 开始按计划推进。务必执行完所有 Phase，不要中途停下来。
