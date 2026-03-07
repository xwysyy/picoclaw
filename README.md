# X-Claw

<p align="center">
  <img src="assets/logo.svg" alt="X-Claw logo" width="120" />
</p>

[English](README.en.md) | [Roadmap](ROADMAP.md)

X-Claw 是一个使用 Go 编写的轻量级个人 AI 助手，目前主线产品形态是 **Gateway-first 的双渠道助手**。

本仓库当前重点聚焦：
- `Feishu` 主部署
- `Telegram` 保留支持
- `gateway` 常驻服务
- `agent` 本地调试入口

## 项目范围

当前主线支持：
- Gateway 服务（`gateway` 模式）
- 本地调试 CLI（`agent` 模式）
- Feishu / Telegram 渠道接入
- 基础会话与工作区持久化
- 工具调用（文件、命令、Web、文档解析）

当前命令面已收敛为：
- `x-claw gateway`
- `x-claw agent`
- `x-claw version`

## 项目状态

项目正在进行 **Gateway-first 瘦身重构**：
- 不再以“多命令、多渠道、全平台兼容”作为优先目标
- 重点是减少文件数、降低结构复杂度、突出主链能力
- 未加防护前，不建议直接暴露到公网

## 环境要求

- 推荐 Linux 主机（x86_64 / ARM64 / RISC-V）
- 源码构建需要 Go 工具链
- 至少一个可用模型 API Key（或兼容的本地/代理端点）

## 快速开始（本地构建）

```bash
git clone https://github.com/xwysyy/X-Claw.git x-claw
cd x-claw
make deps
make build
```

编辑配置：

```bash
vim ~/.x-claw/config.json
```

说明：X-Claw 的运行时配置**只读取** `config.json`（默认路径 `~/.x-claw/config.json`；Docker 部署通常将 `config/config.json` 挂载到该位置），不支持通过环境变量覆盖配置字段，以避免“同一份代码在不同环境变量下行为漂移”。

最小配置示例：

```json
{
  "agents": {
    "defaults": {
      "workspace": "~/.x-claw/workspace",
      "model_name": "gpt-5.2-medium",
      "max_tokens": 8192,
      "max_tool_iterations": 20
    }
  },
  "model_list": [
    {
      "model_name": "gpt-5.2-medium",
      "model": "openai/gpt-5.2-medium",
      "api_key": "YOUR_API_KEY",
      "api_base": "https://api.openai.com/v1"
    }
  ]
}
```

本地调试：

```bash
./build/x-claw agent -m "hello"
./build/x-claw agent
```

## Gateway 模式

启动 gateway：

```bash
./build/x-claw gateway
```

健康检查：

```bash
curl -sS http://127.0.0.1:18790/health
```

当前推荐把 Gateway 作为主入口，Feishu / Telegram 由 Gateway 统一接入与分发。

### Gateway inbound 队列（gateway.inbound_queue）

为了让 Gateway 能长期稳定跑多会话/长任务，X-Claw 支持 **按 session 分桶串行** + **全局并发上限** 的 inbound 队列：
- 同一会话串行：避免上下文/记忆写入竞争
- 不同会话并发：避免一个会话长任务拖死全部

配置示例：

```json
{
  "gateway": {
    "inbound_queue": {
      "enabled": true,
      "max_concurrency": 4,
      "per_session_buffer": 32
    }
  }
}
```

### Gateway 配置热更新（gateway.reload）

为了减少“改配置就得重启/重建容器”的频率，Gateway 支持 **热更新**（仅 Gateway 模式）：
- **SIGHUP**：收到 `SIGHUP` 时尝试 reload（需要 `gateway.reload.enabled=true`）
- **watch**：轮询配置文件变更并自动 reload（适合 Docker bind-mount；需要 `gateway.reload.watch=true`）

配置示例：

```json
{
  "gateway": {
    "reload": {
      "enabled": true,
      "watch": true,
      "interval_seconds": 2
    }
  }
}
```

触发方式：

```bash
# 方式 1：发 SIGHUP（仅当 enabled=true 时生效）
kill -HUP <x_claw_pid>
```

当前 reload 覆盖范围（刻意保持小而可控）：
- 会重启 channels + 重新注册 webhook/HTTP handlers（含 `/api/notify`、`/api/console/*` 等）
- 会把新 config 应用到 agent loop（含 notify/tool policy/MCP server 配置），并刷新 MCP tools 注册表
- **不会**重启 cron/heartbeat/provider（这些通常需要“更重”的重启语义）

### Gateway Console（/console/）

Gateway 内置一个**只读 Console**（Web UI，Next.js + shadcn/ui），用于自助查看：
- `last_active` / 基础状态
- cron jobs（`cron/jobs.json`）
- token usage（`state/token_usage.json`，按模型统计累计 tokens）
- sessions 列表（元数据：`sessions/*.meta.json`；事件流：`sessions/*.jsonl`）
- run trace / tool trace（`<workspace>/.x-claw/audit/**/events.jsonl`）
- 健康检查链接（`/health` / `/ready`）

打开：

```bash
# 浏览器访问
http://127.0.0.1:18790/console/
```

鉴权规则同 `/api/notify`：
- 当 `gateway.api_key` 为空时：`/console/` 与 `/api/console/*` 仅允许来自本机 loopback 的访问
- 当 `gateway.api_key` 非空时：`/console/` 可以直接在浏览器打开，但 `/api/console/*` 需要携带 `Authorization: Bearer <api_key>`  
  （UI 内置了 API key 输入框，会用 Bearer header 拉取数据）

Console 对应的 JSON API（便于脚本化）：

```bash
curl -sS http://127.0.0.1:18790/api/console/status
curl -sS http://127.0.0.1:18790/api/console/cron
curl -sS http://127.0.0.1:18790/api/console/tokens
curl -sS http://127.0.0.1:18790/api/console/sessions
curl -sS http://127.0.0.1:18790/api/console/runs
curl -sS http://127.0.0.1:18790/api/console/tools
```

下载 workspace 内的审计/状态文件（只允许 `.x-claw/audit/`、`cron/`、`state/` 下的 `.json/.jsonl/.md` 等白名单文件）：

```bash
curl -sS -OJ "http://127.0.0.1:18790/api/console/file?path=cron/jobs.json"
```

仅取末尾 N 行（适合 `events.jsonl`，避免下载整文件）：

```bash
curl -sS "http://127.0.0.1:18790/api/console/tail?path=.x-claw/audit/runs/<session>/events.jsonl&lines=200"
```

实时跟随（tail -f 风格，适合观察长任务进度；UI 里 Traces 也提供 Live 按钮）：

```bash
# -N: curl 不缓冲输出
curl -N -sS "http://127.0.0.1:18790/api/console/stream?path=.x-claw/audit/runs/<session>/events.jsonl&tail=200"
```

### 通知接口（/api/notify）

Gateway 会额外暴露一个通知接口，用于让外部系统（CI / 脚本 / 守护进程）通过已配置的渠道给你发提醒（例如飞书）。

指定渠道与收件人：

```bash
curl -sS -X POST http://127.0.0.1:18790/api/notify \
  -H 'Content-Type: application/json' \
  -d '{"channel":"feishu","to":"oc_xxx","content":"任务完成了"}'
```

如果省略 `channel/to`，会默认发送到最近一次对话的 `last_active`：

```bash
curl -sS -X POST http://127.0.0.1:18790/api/notify \
  -H 'Content-Type: application/json' \
  -d '{"content":"任务完成了（last_active）"}'
```

注意：如果 gateway 刚启动、还没有任何外部对话记录（`last_active` 为空），省略 `channel/to` 会返回 `channel is required`。此时请先在飞书/Telegram 等渠道给机器人发一句话建立 `last_active`，或显式指定 `channel/to`。

安全说明：
- 当 `gateway.api_key` 为空时，仅允许来自本机 loopback 的请求（例如 `127.0.0.1`）
- 当 `gateway.api_key` 设置为非空时，请携带 `Authorization: Bearer <api_key>`（否则返回 401）

公网暴露建议（如需远程/跨机器通知）：
- 强烈不建议在 `gateway.api_key` 为空时暴露公网
- 建议优先使用反向代理（HTTPS）或私网方案（如 Tailscale）再对外提供 `/api/notify`
- 如必须直连：将 `gateway.host` 设为 `0.0.0.0` 并配置强随机 `gateway.api_key`

外部 Agent（例如 Claude Code / Codex）对接 X-Claw 通知的扩展文档见：`extensions/x-claw-notify/SKILL.md`（通过调用 `/api/notify` 推送提醒）。

### 常见问题：群聊发消息不回复（channels.*.group_trigger）

**现象**：你在飞书/Telegram 群里发消息，机器人不理你；但 `/health` 正常、`/api/notify` 也能给你推送消息。

**原因（最常见）**：X-Claw 对群聊采用 **safe-by-default** 策略，默认只在满足触发条件时才回复群消息，否则直接忽略（避免群里过吵）。

群聊触发由 `channels.<channel>.group_trigger` 控制：
- `mention_only=true`：必须 `@机器人` 才回复（更安全）
- `command_bypass=true`：以 `/`（或自定义 `command_prefixes`）开头的命令可绕过 `@` 限制
- `prefixes=["/ask", "!"]`：以指定前缀开头才触发（触发后会自动剥离前缀）
- `mentionless=true`：群聊不需要 `@` 也会回复（最“放开”，但也最吵）

同时注意 `allow_from`（允许列表）在群聊触发之前就会生效：
- `allow_from=[]` 表示允许所有人
- 非空表示只允许名单中的 sender（支持 `"platform:id"`、纯数字 id、`"@username"`、`"id|username"` 等格式）

示例：允许飞书群聊不需要 `@` 也回复（适合“群里只有你自己”的场景）：

```json
{
  "channels": {
    "feishu": {
      "group_trigger": {
        "mention_only": false,
        "mentionless": true,
        "command_bypass": true,
        "command_prefixes": ["/"]
      }
    }
  }
}
```

如果你不确定消息是否“入站成功”，可以先用 `/api/notify` 验证 **出站** 是否正常；若出站正常但入站不触发，优先检查 `group_trigger` 和 `allow_from`。

### 任务完成提醒（notify.on_task_complete）

当你把 `notify.on_task_complete=true` 时，X-Claw 会在 **内部通道**（`cli/system`）的一次 run 正常结束后，自动通过 `message` tool 把“任务完成 + 结果摘要”发到 `last_active` 外部会话。

静默约定（少打扰）：
- 如果内部任务最终输出为 `NO_UPDATE` 或 `HEARTBEAT_OK`（大小写不敏感），则不会触发完成提醒  
  这适合 cron/后台巡检类任务：无更新不打扰，有更新才提醒。


### Web 证据模式（tools.web.evidence_mode）

当你把 `tools.web.evidence_mode.enabled=true` 时：
- `web_search` 会返回结构化 JSON（包含 `sources[]` 和 `evidence.satisfied/distinct_domains`）
- system prompt 会强制“事实/最新信息”回答引用 ≥2 个不同域名来源；否则明确不确定性并给出验证建议

配置示例：

```json
{
  "tools": {
    "web": {
      "evidence_mode": {
        "enabled": true,
        "min_domains": 2
      }
    }
  }
}
```

### Plan Mode（计划阶段：先出方案，再允许执行）

Plan Mode 用于把“工具执行”从默认放开，升级为“先计划、再审批”：
- `/plan <任务>`：进入计划阶段（此时会拒绝 `exec/edit/write` 等危险工具）
- `/approve` 或 `/run`：批准并执行刚才计划阶段捕获的任务
- `/cancel`：退出计划阶段并清空 pending task
- `/mode`：查看当前 mode 和 pending task 预览

### 上下文压缩（Compaction）

当会话历史过长（接近模型 context window）时，X-Claw 会在后台对历史进行总结/压缩，以提升长期运行稳定性。

默认行为：
- **不会**在飞书/Telegram 里额外发“正在压缩上下文”的提示（避免太吵）

如需开启提示：

```json
{
  "agents": {
    "defaults": {
      "compaction": {
        "notify_user": true
      }
    }
  }
}
```

### Exec Docker Sandbox（tools.exec.backend=docker）

X-Claw 的 `exec` 工具支持两种后端：
- `host`（默认）：直接在宿主机执行
- `docker`：在一次性容器里执行（只挂载 workspace），更适合长期运行时的安全边界

配置示例：

```json
{
  "tools": {
    "exec": {
      "backend": "docker",
      "docker": {
        "image": "alpine:3.20",
        "network": "none",
        "read_only_rootfs": true
      }
    }
  }
}
```

注意：
- `docker` 后端要求 `restrict_to_workspace=true`
- 当前 `docker` 后端不支持 `background=true` / `yield_ms`（建议用 `host` 后端配合 process tool 做长任务）

### Tool Trace（工具调用可追溯 / 可复盘）

当你把配置里的 `tools.trace.enabled` 设为 `true` 时，每一次 tool call 都会追加写入一个 JSONL 事件流，并可选写 per-call 文件，便于排查 “模型为什么调用了某个工具 / 工具到底返回了什么 / 耗时多少”。

默认落盘位置（当 `tools.trace.dir` 为空时）：

- ` <workspace>/.x-claw/audit/tools/<session>/events.jsonl `
- ` <workspace>/.x-claw/audit/tools/<session>/calls/*.json|*.md `（当 `tools.trace.write_per_call_files=true`）

配置示例：

```json
{
  "tools": {
    "trace": {
      "enabled": true,
      "write_per_call_files": true
    }
  }
}
```

### Run Trace（run 级 checkpoint 事件流）

Tool Trace 解决的是“每次工具调用发生了什么”；Run Trace 解决的是“这一次用户请求（run）整体发生了什么”（LLM 回合、工具批次、最终输出）。

当 `tools.trace.enabled=true` 时，X-Claw 会额外为每个 session 维护一个 run 级 JSONL 事件流（append-only），用于：
- 故障定位（某一轮为什么走偏 / 哪个 batch 出错）
- 长任务可恢复（Phase E2 的地基）
- 与 `x-claw export` 一起形成“可回放执行”的最小证据链

默认落盘位置：

- ` <workspace>/.x-claw/audit/runs/<session>/events.jsonl `

### Tool Policy（tools.policy：统一策略层 / 审计与安全护栏）

从 Phase D2 开始，X-Claw 在 **tool executor 的统一入口**（所有内置 tools + MCP tools 一视同仁）增加了策略层，用于收口：
- allow/deny（名单与前缀）
- 统一 timeout（每个 tool call 的最大墙钟时间）
- 脱敏（避免 secret 落盘到 trace/ledger；可选对 LLM/User 输出也脱敏）
- “副作用确认”（两段提交：先要求确认，再执行）
- 幂等/可恢复（resume 时避免重复执行副作用工具）

配置示例（建议先从“只做审计脱敏 + timeout”开始）：

```json
{
  "tools": {
    "policy": {
      "enabled": true,
      "timeout_ms": 60000,
      "redact": {
        "enabled": true,
        "apply_to_llm": false,
        "apply_to_user": false
      }
    }
  }
}
```

启用“副作用确认 + 幂等重放”（推荐仅在 resume 流程开启确认门）：

```json
{
  "tools": {
    "policy": {
      "enabled": true,
      "confirm": {
        "enabled": true,
        "mode": "resume_only",
        "tools": ["exec", "write_file", "edit_file", "append_file"]
      },
      "idempotency": {
        "enabled": true,
        "cache_result": true,
        "tools": ["exec", "write_file", "edit_file", "append_file"]
      }
    }
  }
}
```

说明：
- 当某个 tool call 被策略层拦下时，tool output 会返回结构化 JSON（`kind=tool_policy_*`），模型应先向用户请求确认，再调用 `tool_confirm(confirm_key)`。
- 幂等 ledger 会落在：` <workspace>/.x-claw/audit/runs/<session>/runs/<run_id>/policy.jsonl `
- Tool Trace 的 JSONL 事件会携带 `run_id`、`policy_decision`、`idempotency_key` 等字段，便于线上排障与复盘。

### MCP Bridge（tools.mcp：挂载外部工具生态）

X-Claw 支持把 MCP（Model Context Protocol）服务器上的工具动态注册为原生工具（Phase D1）。

核心特性：
- 启动时（best-effort）发现 MCP server 工具并注册到 `ToolRegistry`
- MCP tool call 统一走现有 tool executor：自动纳入 Tool Trace / Run Trace / 超时与错误模板
- 工具命名隔离：默认前缀 `mcp_<server>_`，也可自定义 `tool_prefix`

配置示例（stdio）：

```json
{
  "tools": {
    "mcp": {
      "enabled": true,
      "servers": [
        {
          "name": "local",
          "transport": "stdio",
          "command": "python",
          "args": ["-m", "your_mcp_server"],
          "include_tools": ["echo", "add"],
          "tool_prefix": "mcp_local_",
          "timeout_seconds": 30
        }
      ]
    }
  }
}
```

命名说明：
- 注册到 LLM 的工具名会做安全清洗（仅允许字母/数字/`_`/`-`），避免 provider 拒绝调用
- 默认命名形如：`mcp_<server>_<tool>`（例如 `mcp_local_echo`）

### Resume last task（/api/resume_last_task）

从 Phase E2 开始，Gateway 会额外暴露一个“断点续跑”接口：定位最近一次 **未正常结束** 的 run，并在同一 `run_id` 上继续执行（append-only 续写 run trace）。

调用示例：

```bash
curl -sS -X POST http://127.0.0.1:18790/api/resume_last_task
```

鉴权规则同 `/api/notify`：
- `gateway.api_key` 为空：仅允许 loopback（127.0.0.1）访问
- `gateway.api_key` 非空：携带 `Authorization: Bearer <api_key>` 或 `X-API-Key: <api_key>`

强烈建议同时开启 `tools.policy.confirm` + `tools.policy.idempotency`，避免 resume 时重复执行 `exec` / 写文件 / MCP 写操作类工具。

### Run/Session 导出（x-claw export）

当你需要提交 bug / 复盘某次对话 / 把 “可回放执行” 资料打包给别人时，可以导出一个 zip bundle（默认会包含：session 快照 + tool traces + run traces + cron/state/config 脱敏快照 + manifest）。

常用用法：

```bash
# 直接导出当前 workspace 的 last_active 会话（推荐）
./build/x-claw export --last-active

# 或导出指定 sessionKey
./build/x-claw export --session 'agent:main:feishu:group:oc_xxx'
```

默认输出位置：

- ` <workspace>/exports/*.zip `

### 统一工具错误模板（tools.error_template）

当工具执行失败时（`is_error=true`），X-Claw 会把错误包装成结构化 JSON（`kind=tool_error`），并附带最小的自愈 hints（required 参数、可用工具列表/相似工具名等），让模型更容易自救（换参数 / 换工具 / 先读后写）。

说明：
- 这是 executor 层的统一能力，不需要每个 tool 单独实现
- 只影响 LLM 侧的 `ForLLM`；不会强制把 JSON 错误刷给用户（若 tool 提供了 `ForUser`，会优先保留人类友好输出）

配置示例（默认已启用）：

```json
{
  "tools": {
    "error_template": {
      "enabled": true,
      "include_schema": true
    }
  }
}
```

### 结构化记忆输出（Memory JSON hits）

`memory_search` / `memory_get` 的工具输出对 LLM 侧返回结构化 JSON（`kind` 字段可用于回归测试与稳定引用），同时对人类侧保留简要摘要：

- `memory_search` → `{"kind":"memory_search_result","hits":[...]}`
- `memory_get` → `{"kind":"memory_get_result","found":...,"hit":...}`

这能显著降低 “模型看不懂纯文本结果 / 引用不稳定” 的概率。

另外，`memory_search_result.hits[]` 会提供 `match_kind` 与 `signals` 字段（例如 `fts_score` / `vector_score`），便于你在排查召回漂移时快速判断“这条命中是靠关键词还是靠向量”。

### 语义记忆 Embeddings（可选远程）

X-Claw 的语义记忆（`agents.defaults.memory_vector`）默认使用本地 `hashed` embedder：快、确定性强、无需额外 API / 网络。

如果你希望更高质量的语义检索，可以让 X-Claw 调用一个 OpenAI-compatible 的 embeddings 端点（`POST <api_base>/embeddings`），例如 SiliconFlow / OpenAI / 其他兼容服务。

在本项目中，embeddings 配置 **只从 `config.json` 读取**。示例（放到 `agents.defaults.memory_vector.embedding`）：

```json
{
  "agents": {
    "defaults": {
      "memory_vector": {
        "dimensions": 4096,
        "hybrid": {
          "fts_weight": 0.6,
          "vector_weight": 0.4
        },
        "embedding": {
          "kind": "openai_compat",
          "api_base": "https://api.siliconflow.cn/v1",
          "api_key": "sk-...",
          "model": "Qwen/Qwen3-Embedding-8B",
          "proxy": "",
          "batch_size": 64,
          "request_timeout_seconds": 30
        }
      }
    }
  }
}
```

说明：
- 如果 `embedding.kind` 为空或为 `hashed`，则使用本地 deterministic 的 `hashed` embedder（无网络）
- 首次触发语义检索/索引重建时会产生网络请求；索引会落盘缓存，并在源文件或 `api_base/model` 变化时自动重建
- 如果你显式把 `embedding.kind` 设为 `openai_compat`，则 `api_base` 与 `model` 为必填（否则会报错）

### Cron 可运营任务状态（runHistory / lastStatus）

Cron 的任务状态会持久化到工作区：

- ` <workspace>/cron/jobs.json `

其中 `state` 会记录：

- `lastStatus` / `lastRunAtMs` / `lastDurationMs`
- `lastOutputPreview`（截断预览）
- `runHistory`（最近 N 次运行记录）

CLI 侧常用命令：

```bash
```

#### Cron 无更新不提醒（NO_UPDATE）

对于“抓取/巡检/日报”类定时任务，经常会出现“本次没有新内容”的情况。为了减少打扰，你可以在任务提示词里约定：

- **如果没有新发现，最终回复必须是 `NO_UPDATE`**（大小写不敏感）

当 cron 的 `deliver=false`（让 agent 处理）时，X-Claw 会把 `NO_UPDATE` 视为“无更新”，从而：
- 不向聊天渠道推送 `Cron job '...' completed` 消息（安静）
- 但 `jobs.json` 的 `runHistory/lastOutputPreview` 仍会记录 `NO_UPDATE`（便于运维/回放）

## Docker Compose

本仓库的 `docker/docker-compose.yml` 提供两个 profile：
- `gateway`：常驻服务
- `agent`：单次/手动执行

容器会挂载本地 `config/config.json`（只读）作为运行配置。
如果容器日志提示 `permission denied` 无法读取 `/home/xclaw/.x-claw/config.json`，通常是因为宿主机上的 `config/config.json` 权限过严（例如 `600`），请确保容器用户可读（例如 `chmod 644 config/config.json`）。

构建并启动 gateway：

```bash
docker compose -p x-claw -f docker/docker-compose.yml --profile gateway up -d --build
docker compose -p x-claw -f docker/docker-compose.yml ps
curl -sS http://127.0.0.1:18790/health
```

执行单次 agent：

```bash
docker compose -p x-claw -f docker/docker-compose.yml run --rm x-claw-agent -m "hello"
```

容器镜像内已包含 `git`，可用于 agent 在工作区内提交/推送代码。请在 `config/config.json` 里配置 `tools.git`（PAT + 身份）：

```json
{
  "tools": {
    "git": {
      "enabled": true,
      "username": "你的 GitHub 用户名",
      "pat": "github_pat_xxx",
      "user_name": "你的名字",
      "user_email": "you@example.com",
      "host": "github.com",
      "protocol": "https"
    }
  }
}
```

容器启动时会自动写入 `~/.git-credentials` 和 `~/.gitconfig`。

停止服务：

```bash
docker compose -p x-claw -f docker/docker-compose.yml down
```

## 常用命令

- `x-claw agent` 交互式对话
- `x-claw agent -m "..."` 单轮对话
- `x-claw gateway` 启动网关服务

## 配置说明

- 主配置文件：`~/.x-claw/config.json`
- 默认工作区：`~/.x-claw/workspace`
- 配置模板：`config/config.example.json`

进阶配置可直接查看代码中的配置结构（`pkg/config`）。

## 单元测试

单元测试与 TDD 工作流说明见：[UNIT_TESTING.md](UNIT_TESTING.md)。

仓库还提供了一个适合当前受限环境的分批测试脚本：

```bash
./scripts/test-batches.sh
```

特点：
- 先执行 `go build ./...`、`go vet ./...` 与 compile-only 全量检查
- 再按包单独执行 `go test`，降低单次进程峰值内存
- 对 `pkg/agent` 按顶层测试逐批执行，减少 `SIGKILL(137)` 概率
- 可选 `--race-safe` 追加当前环境里相对稳定的 race 批次（`pkg/session`、`pkg/httpapi`）

## 排错

参考：
- `docker compose ... logs -f x-claw-gateway`

## 许可证

MIT
