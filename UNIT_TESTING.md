# 单元测试与 TDD 指南

本项目经历了较多定制化改动。为了避免回归、提升可维护性，我们把「高质量单元测试」当作一等公民：

- 覆盖关键路径与边界条件
- 尽量测试失败路径（错误返回、超时、空输入、非法输入、资源限制等）
- 保持测试确定性（可重复、无外部依赖、运行时间可控）

本文档是面向 PicoClaw 的测试约定与落地方法，包含推荐命令、TDD 工作流和项目内的常用测试模式。

## 推荐命令

### 1. 跑全量单测（推荐）

部分环境（小内存 VM / SBC / CI）可能会在 `go test ./...` 时被 OOM kill。
为稳定起见，本仓库提供了按包顺序逐个执行的方式：

```bash
make test
```

等价于：

```bash
./scripts/test-unit.sh
```

脚本内部会对每个包执行两步：

- `go test -c` 编译出该包的测试二进制（降低 `go test` 同时“编译+执行”时的峰值内存）
- 直接运行测试二进制（更不容易被 OOM kill）

你也可以给脚本追加常用的 `go test` 参数（会对每个包生效；脚本会把部分参数转换成测试二进制的 `-test.*` 参数）：

```bash
./scripts/test-unit.sh -race
./scripts/test-unit.sh -run TestSanitizeHistoryForProvider
./scripts/test-unit.sh -v
```

如果只想测试部分包，可以用环境变量覆盖：

```bash
PICOCLAW_TEST_PKGS='./pkg/agent ./pkg/tools' ./scripts/test-unit.sh
```

### 2. 快速并行（可能更快，但更吃内存）

```bash
make test-fast
```

### 3. 跑单个包 / 单个用例

```bash
go test ./pkg/agent -count=1
go test ./pkg/agent -run TestSanitizeHistoryForProvider -count=1
```

### 4. 覆盖率

全仓库覆盖率（推荐，内存更稳）：

```bash
make cover
```

这会生成：

- `coverage.out`（coverprofile）
- `coverage.html`（可视化报告）

等价于：

```bash
./scripts/cover-unit.sh
```

单包覆盖率：

```bash
go test ./pkg/agent -cover -count=1
```

生成可视化报告（单包示例）：

```bash
go test ./pkg/agent -coverprofile=coverage.out -count=1
go tool cover -html=coverage.out -o coverage.html
```

提示：全仓库覆盖率聚合（`go test ./... -coverprofile=...`）在某些机器上会比较重，优先按包评估并逐步补齐边界用例。

## 依赖下载与代理（本环境注意）

如果需要下载 Go 依赖或工具链，在非交互 shell 下建议：

```bash
source ~/.zshrc && proxy_on
go test ./... -count=1
```

说明：`proxy_on` 是 `~/.zshrc` 里定义的函数，非交互环境不会自动加载。

## TDD（测试驱动开发）工作流

推荐采用经典的 Red-Green-Refactor：

1. **Red**：先写一个失败的测试，用例明确描述预期行为。
2. **Green**：用最小实现让测试通过，避免一次写太多逻辑。
3. **Refactor**：在测试保护下重构代码（去重复、拆分函数、命名优化、抽象边界）。
4. **补边界**：把“现实世界会发生的坏输入”补成用例（空字符串、nil、超时、错误码、权限、重复调用等）。

对于回归问题：

- 先写一个能稳定复现的测试（锁住 bug）
- 再修复实现
- 最后加上相邻边界条件，避免换个输入又复发

## 项目内的测试模式（建议遵循）

### 1. Table-Driven Tests

优先用表驱动覆盖边界输入：

- 空值 / 缺字段
- 不同组合（参数缺失、类型错误、范围错误）
- 关键分支（成功 / 失败 / 超时）

### 2. 避免真实外部依赖

单元测试默认不应依赖：

- 真实网络（使用 `httptest.NewServer`）
- 真实 API Key
- 真实时间长等待（使用短 timeout + context）

例如 HTTP 逻辑：用 `httptest` 断言请求形状并返回伪响应。

### 3. 文件系统与临时目录

使用 `t.TempDir()`，避免污染工作区：

- 创建临时 workspace
- 写入测试文件
- 断言输出

### 4. Context 与超时

所有可能阻塞的逻辑（channel publish、HTTP、长任务）都应在测试里：

- 使用 `context.WithTimeout`
- 断言在合理时间内返回
- 超时属于失败（避免测试挂死）

### 5. Mock / Fake 的优先级

- 能 fake 就 fake（更轻、更确定）
- 只有必要时才 mock 复杂组件

项目内已有参考实现：

- `pkg/agent/mock_provider_test.go`：LLM provider mock
- `pkg/agent/loop_test.go`：bus/channel 等组合测试示例

## 质量标准（写新单测时自检）

- 用例名称清晰，失败时能一眼知道哪里错了
- 对边界条件有覆盖（尤其是错误路径）
- 不依赖随机与全局状态（必要时固定种子、用临时目录）
- 不做长 sleep（用 channel、context、可控超时替代）
- 不引入新的外部依赖（除非确实需要且能稳定下载/缓存）
