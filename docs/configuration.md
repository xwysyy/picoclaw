# 配置参考

X-Claw 的运行时配置通过一个 JSON 文件统一管理，不支持环境变量覆盖配置字段，以避免"同一份代码在不同环境变量下行为漂移"。

## 配置文件路径与加载机制

- **默认路径**：`~/.x-claw/config.json`
- **Docker 部署**：通常将 `config/config.json` 挂载到容器内的 `~/.x-claw/config.json`
- **环境变量 `X_CLAW_HOME`**：如设置，则从 `$X_CLAW_HOME/config.json` 读取
- **配置模板**：仓库中的 `config/config.example.json` 提供了所有配置块的完整示例

加载流程：
1. 读取 `DefaultConfig()`（代码内置的默认值）
2. 从 JSON 文件反序列化覆盖（用户只需写自己需要的字段）
3. 校验（`ValidateAll()`）
4. 如仅有旧版 `providers` 配置而无 `model_list`，自动迁移

如果配置文件不存在，X-Claw 会使用内置默认值运行。

---

## 配置块说明

### agents

Agent 运行时核心配置，位于 `agents.defaults` 下。

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `workspace` | string | `~/.x-claw/workspace` | 工作区路径 |
| `restrict_to_workspace` | bool | true | 限制文件操作在工作区内 |
| `model_name` | string | - | 使用的模型名（对应 `model_list` 中的 `model_name`） |
| `max_tokens` | int | 32768 | LLM 最大输出 tokens |
| `temperature` | float | nil(provider default) | 采样温度 |
| `max_tool_iterations` | int | 50 | 单次 run 最大工具迭代次数 |
| `summarize_message_threshold` | int | 20 | 触发摘要的消息数阈值 |
| `summarize_token_percent` | int | 75 | 触发摘要的 token 占比阈值 |

#### compaction（上下文压缩）

当会话历史过长时，X-Claw 会在后台对历史进行总结/压缩。

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `mode` | string | `safeguard` | 压缩模式 |
| `reserve_tokens` | int | 2048 | 保留给输出的 tokens |
| `keep_recent_tokens` | int | 2048 | 保留最近消息的 tokens |
| `max_history_share` | float | 0.5 | 历史占 context window 的最大比例 |
| `notify_user` | bool | false | 压缩时是否通知用户 |
| `memory_flush.enabled` | bool | true | 压缩时将重要内容刷入记忆 |
| `memory_flush.soft_threshold_tokens` | int | 1500 | 触发记忆刷新的 token 阈值 |

#### context_pruning（上下文裁剪）

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `mode` | string | `tools_only` | 裁剪模式（仅工具结果） |
| `soft_tool_result_chars` | int | 2000 | 工具结果软截断字符数 |
| `hard_tool_result_chars` | int | 350 | 工具结果硬截断字符数 |
| `trigger_ratio` | float | 0.8 | 触发裁剪的 context 占比 |

#### memory_vector（语义记忆）

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `enabled` | bool | true | 启用向量记忆 |
| `dimensions` | int | 256 | 向量维度 |
| `top_k` | int | 6 | 检索返回的最大条目数 |
| `min_score` | float | 0.15 | 最小相似度阈值 |
| `max_context_chars` | int | 1800 | 注入上下文的最大字符数 |
| `recent_daily_days` | int | 14 | 近期日记忆天数 |
| `embedding.kind` | string | `hashed` | 嵌入方式：`hashed`（本地）或 `openai_compat`（远程） |
| `embedding.api_base` | string | - | 远程嵌入端点（`openai_compat` 时必填） |
| `embedding.model` | string | - | 远程嵌入模型（`openai_compat` 时必填） |
| `embedding.api_key` | string | - | 远程嵌入 API key |

#### bootstrap_snapshot

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `enabled` | bool | true | 启用启动快照（加速冷启动） |

---

### model_list

模型配置列表。每个条目代表一个可用的 LLM 端点。

| 字段 | 类型 | 是否必填 | 说明 |
|------|------|----------|------|
| `model_name` | string | 是 | 模型显示名（在 `agents.defaults.model_name` 中引用） |
| `model` | string | 是 | 模型标识（`provider/model`，如 `openai/gpt-5.2-medium`） |
| `api_key` | string | 通常是 | API 密钥 |
| `api_base` | string | 否 | 自定义 API 端点 |
| `thinking_level` | string | 否 | 思考级别（如 `high`，仅部分 provider 支持） |
| `auth_method` | string | 否 | 认证方式（如 `oauth`，用于 Antigravity/GitHub Copilot） |

**负载均衡**：使用相同的 `model_name` 创建多个条目，X-Claw 会在它们之间进行轮询负载均衡。

示例：

```json
{
  "model_list": [
    {
      "model_name": "loadbalanced-gpt",
      "model": "openai/gpt-5.2-medium",
      "api_key": "sk-key1",
      "api_base": "https://api1.example.com/v1"
    },
    {
      "model_name": "loadbalanced-gpt",
      "model": "openai/gpt-5.2-medium",
      "api_key": "sk-key2",
      "api_base": "https://api2.example.com/v1"
    }
  ]
}
```

---

### channels

渠道配置。每个渠道有各自的专属字段（参见 `config/config.example.json`），但以下字段是通用的：

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `enabled` | bool | false | 启用该渠道 |
| `allow_from` | []string | [] | 允许的发送者列表（空表示允许所有人） |
| `group_trigger` | object | - | 群聊触发规则 |
| `group_trigger.mention_only` | bool | - | 需要 @机器人 才回复 |
| `group_trigger.mentionless` | bool | false | 群聊不需要 @ 也回复 |
| `group_trigger.command_bypass` | bool | - | 命令前缀可绕过 @ 限制 |
| `group_trigger.command_prefixes` | []string | ["/"] | 命令前缀列表 |
| `group_trigger.prefixes` | []string | [] | 消息触发前缀 |
| `placeholder` | object | - | 占位消息配置 |
| `placeholder.enabled` | bool | - | 启用"Thinking..."占位消息 |
| `placeholder.text` | string | - | 占位消息文本 |
| `placeholder.delay_ms` | int | 2500 | 延迟多少毫秒后显示 |
| `typing` | object | - | 打字状态指示器 |
| `typing.enabled` | bool | - | 启用打字状态 |
| `reasoning_channel_id` | string | "" | 推理过程输出到指定频道 |

当前支持的渠道包括：Feishu、Telegram、Discord、QQ、WhatsApp、DingTalk、Slack、LINE、OneBot、WeCom、WeCom App、WeCom AI Bot、Pico。

具体的渠道配置字段请参考 `config/config.example.json` 和 `docs/channels/` 目录。

---

### gateway

Gateway 服务配置。

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `host` | string | `127.0.0.1` | 监听地址 |
| `port` | int | 18790 | 监听端口 |
| `api_key` | string | "" | API 鉴权密钥（空则仅允许 loopback） |

#### inbound_queue（入站队列）

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `enabled` | bool | true | 启用入站队列 |
| `max_concurrency` | int | 4 | 全局最大并发 |
| `per_session_buffer` | int | 32 | 每 session 缓冲区大小 |

#### reload（热更新）

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `enabled` | bool | true | 启用 SIGHUP 热更新 |
| `watch` | bool | false | 启用轮询检测配置文件变更 |
| `interval_seconds` | int | 2 | 轮询间隔（秒） |

热更新详细说明见 [docs/gateway.md](gateway.md)（如已创建）或 README 中的"Gateway 配置热更新"章节。

热更新覆盖范围：
- 会重启 channels + 重新注册 webhook/HTTP handlers
- 会把新 config 应用到 agent loop（含 notify/tool policy/MCP server 配置），并刷新 MCP tools 注册表
- **不会**重启 cron/heartbeat/provider

---

### tools

工具系统配置。包含 `web`、`exec`、`mcp`、`cron`、`skills`、`trace`、`policy`、`error_template`、`plan_mode`、`estop` 等子配置块。

详细文档请参阅：[docs/tools.md](tools.md)

---

### notify

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `on_task_complete` | bool | false | 内部通道任务完成后自动推送摘要到 last_active |

静默约定：如果任务最终输出为 `NO_UPDATE` 或 `HEARTBEAT_OK`（大小写不敏感），则不触发完成提醒。

---

### heartbeat

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `enabled` | bool | true | 启用心跳 |
| `interval` | int | 5 | 心跳间隔（分钟） |

---

### orchestration

多 Agent 编排配置（实验性功能）。

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `enabled` | bool | false | 启用编排 |
| `max_parallel_workers` | int | 8 | 最大并行 worker 数 |
| `max_tasks_per_agent` | int | 20 | 每个 agent 最大任务数 |
| `default_task_timeout_seconds` | int | 180 | 默认任务超时（秒） |
| `tool_calls_parallel_enabled` | bool | true | 启用工具并行调用 |
| `max_tool_call_concurrency` | int | 8 | 工具并行最大并发 |
| `parallel_tools_mode` | string | `read_only_only` | 并行工具模式 |

---

## 安全提醒

以下字段包含敏感信息，**绝对不要**提交到 Git 仓库或公开分享：

- `model_list[].api_key`
- `channels.*.token` / `app_secret` / `encrypt_key` / `verification_token`
- `tools.git.pat`
- `tools.web.brave.api_key` / `perplexity.api_key` / `tavily.api_key`
- `tools.mcp.servers.*.env.*TOKEN`
- `gateway.api_key`
- `agents.defaults.memory_vector.embedding.api_key`

建议做法：
- 在 `.gitignore` 中添加 `config/config.json`
- 使用 `config/config.example.json` 作为模板，复制后填入真实密钥
- Docker 部署时通过 bind-mount 挂载配置文件，不将密钥写入镜像
