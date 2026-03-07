# X-Claw Gateway Core Slim Design

## 背景

当前 `x-claw` 已经从单一主线产品演化成一个覆盖多渠道、多命令、多安全策略、多运行模式的通用 AI 平台。对于当前实际使用场景，这种结构已经出现明显的过度设计：

- 文件数过多，横向 package 太多，阅读和定位成本高
- 为了兼容低频场景保留了过多命令、渠道和策略分支
- Gateway 主链被多种外围治理能力包围，难以突出产品主线
- 用户的真实使用方式已经收敛：主要通过 Feishu 部署，Telegram 保留，CLI 只用于开发调试

本次重构不以兼容历史结构为目标，而以“保留主要能力、显著减少复杂度、突出项目特色”为目标。

## 目标

将项目重构为 **Gateway-first 的轻量双渠道助手内核**：

- 主渠道：`Feishu`
- 副渠道：`Telegram`
- 辅助入口：`CLI debug`
- 核心职责：`收消息 -> 进入 session 串行队列 -> 运行 agent -> 调用必要工具 -> 回消息`

重点成果：

- 显著减少文件数和 package 数
- 删除低价值、低频、平台化但当前不需要的能力
- 合并过薄抽象层和 one-file package
- 将 Feishu / Telegram 相关能力做收口整合，而不是继续分散扩张
- 保留用户真正使用到的功能主线，不追求旧命令、旧目录、旧配置的完全兼容

## 非目标

- 不保证旧 CLI 命令兼容
- 不保证旧配置字段完整兼容
- 不保证旧 package 路径兼容
- 不保留全部历史渠道
- 不继续维护“通用多渠道 AI 平台”定位

## 建议的最终产品边界

### 保留

- `gateway` 主服务
- `feishu` 渠道
- `telegram` 渠道
- 最小可用 `agent`/`debug` CLI
- 核心会话与上下文持久化
- 核心 agent loop
- 高价值通用工具：`shell`、`filesystem/edit`、`web_fetch/web_search`、`document_text`
- Feishu / Telegram 相关工具（允许整合优化，但不删除）
- 最小健康检查与必要调试接口

### 删除或下沉

- 低频 CLI 命令：`auth`、`auditlog`、`config`、`cron`、`doctor`、`estop`、`export`、`migrate`、`onboard`、`security`、`skills`、`status`
- 低频渠道：`slack`、`discord`、`line`、`onebot`、`wecom`、`qq`、`dingtalk`、`pico`、`whatsapp`、`whatsapp_native`
- 平台治理型接口：`/api/security`、`/api/estop` 等
- 复杂策略系统：`tool_policy`、`tool_confirm`、`plan_mode_gate`、复杂 break-glass 配置
- 仅为兼容多部署姿势而存在的过强抽象层

## 目标架构

重构后的项目收敛为三个主层：

### 1. `gateway`

职责：

- HTTP server 与路由
- webhook / API 鉴权
- inbound 标准化
- session 串行调度
- outbound 发送
- 最小健康检查与通知接口

### 2. `runtime`

职责：

- agent loop
- 上下文构建
- 会话恢复
- 工具注册与执行
- 运行日志和基础追踪

### 3. `channel adapters`

职责：

- Feishu / Telegram 入站解析
- Feishu / Telegram 出站发送
- 渠道专属工具
- 必要的格式转换和媒体处理

## 目录收敛方向

建议从当前“多个 `pkg/*` + 多个 `cmd/x-claw/internal/*` 命令目录”收敛为：

```text
cmd/x-claw/main.go
internal/app/app.go
internal/gateway/server.go
internal/gateway/routes.go
internal/gateway/handlers_*.go
internal/runtime/*.go
internal/channel/feishu/*.go
internal/channel/telegram/*.go
internal/tools/*.go
internal/store/*.go
internal/config/*.go
```

重构原则：

- 能并到主模块的薄层就并掉
- 能通过更少文件表达的逻辑就不要继续拆碎
- 优先按产品主链组织目录，而不是按抽象概念横向切包

## 安全与策略收缩原则

本次不追求“平台级防御体系”，只保留便宜、直观、够用的安全边界：

- 保留 `gateway.api_key` 或 loopback 限制
- 保留少量基础 HTTP headers
- 保留工具执行超时
- 保留简单工作目录边界
- 保留最小审计/日志

删除或大幅降级：

- `tool_policy` 的 allow/deny/confirm/idempotency 体系
- `tool_confirm` 两阶段确认
- `plan_mode_gate`
- `estop`
- `security` 自检面板式接口
- 复杂 break-glass 与治理提示分支

## 工具策略

工具系统不再作为“通用代理工作流平台”，而改为固定白名单产品能力：

- 保留并整合：`shell`、`filesystem/edit`、`web_fetch/web_search`、`document_text`
- 保留并整合：Feishu / Telegram 相关工具
- 删除低频、实验性、只服务复杂工作流的工具
- 将工具注册点收敛到更少的固定位置
- 统一 Feishu / Telegram 工具参数风格与结果格式，减少重复实现

## 迁移策略

采用 **先建新内核、再切主线、最后删旧实现** 的方式：

1. 建立新的 Gateway Core 主链
2. 将 Feishu / Telegram 接到新主链
3. 保留高价值工具与渠道工具
4. 用针对主链的测试验证行为
5. 删除旧多渠道框架和外围命令

## 测试策略

重点只验证主链：

- Feishu 收消息 -> session -> agent -> 回消息
- Telegram 收消息 -> session -> agent -> 回消息
- Feishu / Telegram 工具调用与结果回传
- 同 session 串行、跨 session 并发
- CLI debug 入口可用
- Gateway 构建与健康检查可用

不再为已删除的命令、渠道、治理接口维持兼容测试。

## 风险与控制

### 风险

- 一次性删除太多，可能导致 Feishu / Telegram 主链遗漏隐性依赖
- 渠道工具整合时行为细节漂移
- 配置收缩时可能误删真实部署依赖字段

### 控制方式

- 删除前先列出待删清单并确认
- 先建立新主链再切流，不直接盲删
- 对 Feishu / Telegram 主链做 characterization tests 或最小回归测试
- 每一轮删除都配套执行针对性构建/测试

## 执行原则

- 以“明显瘦身、可维护、突出 Gateway 主线”为第一优先级
- 保留 Feishu / Telegram 用户可感知功能
- 不为历史兼容背包袱
- 敢删、敢并、敢改名，但避免把正在使用的主链能力删坏
