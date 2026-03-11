# 工具系统

X-Claw 的工具系统为 Agent 提供与外部世界交互的能力，包括 Web 搜索、命令执行、文件操作、定时任务、外部 MCP 工具等。所有工具配置位于 `config.json` 的 `tools` 字段下。

## 目录

1. [工具概述](#工具概述)
2. [Web 搜索工具](#web-搜索工具)
3. [命令执行工具](#命令执行工具)
4. [MCP Bridge](#mcp-bridge挂载外部工具生态)
5. [Cron 工具](#cron-工具)
6. [Skills 工具](#skills-工具)
7. [Tool Trace / Run Trace](#tool-trace--run-trace)
8. [Tool Policy](#tool-policy统一策略层)
9. [错误模板](#统一工具错误模板)
10. [配置来源说明](#配置来源说明)

---

## 工具概述

工具配置的顶层结构如下：

```json
{
  "tools": {
    "web": { ... },
    "exec": { ... },
    "mcp": { ... },
    "cron": { ... },
    "skills": { ... },
    "trace": { ... },
    "policy": { ... },
    "error_template": { ... },
    "plan_mode": { ... },
    "estop": { ... }
  }
}
```

每个工具子系统都有独立的 `enabled` 开关。除了上述子系统外，还有一组原子工具（`read_file`、`write_file`、`edit_file`、`append_file`、`list_dir`、`message`、`web_fetch`、`send_file` 等），它们各自有 `enabled` 开关，默认大多为 `true`。

---

## Web 搜索工具

Web 搜索工具用于 `web_search` 和 `web_fetch` 能力。X-Claw 支持多种搜索后端，可同时启用多个。

### Brave

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `enabled` | bool | false | 启用 Brave 搜索 |
| `api_key` | string | - | Brave Search API key |
| `max_results` | int | 5 | 最大返回结果数 |

### DuckDuckGo

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `enabled` | bool | true | 启用 DuckDuckGo 搜索 |
| `max_results` | int | 5 | 最大返回结果数 |

### Perplexity

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `enabled` | bool | false | 启用 Perplexity 搜索 |
| `api_key` | string | - | Perplexity API key |
| `max_results` | int | 5 | 最大返回结果数 |

### Tavily

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `enabled` | bool | false | 启用 Tavily 搜索 |
| `api_key` | string | - | Tavily API key |
| `base_url` | string | - | 自定义端点 |
| `max_results` | int | 5 | 最大返回结果数 |

### Grok

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `enabled` | bool | false | 启用 Grok 搜索 |
| `api_key` | string | - | Grok API key |
| `endpoint` | string | `https://api.x.ai/v1/chat/completions` | 端点地址 |
| `default_model` | string | `grok-4` | 默认模型 |
| `max_results` | int | 5 | 最大返回结果数 |

### GLM Search

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `enabled` | bool | false | 启用智谱 GLM 搜索 |
| `api_key` | string | - | GLM API key |
| `base_url` | string | `https://open.bigmodel.cn/api/paas/v4/web_search` | 端点地址 |
| `search_engine` | string | `search_std` | 搜索引擎类型 |
| `max_results` | int | 5 | 最大返回结果数 |

### Web 证据模式（evidence_mode）

当启用 `tools.web.evidence_mode.enabled=true` 时：
- `web_search` 会返回结构化 JSON（包含 `sources[]` 和 `evidence.satisfied/distinct_domains`）
- system prompt 会强制"事实/最新信息"回答引用不少于指定数量的不同域名来源；否则明确不确定性并给出验证建议

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

### 其他 Web 配置

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `proxy` | string | "" | HTTP 代理（用于搜索请求） |
| `fetch_limit_bytes` | int | 10485760 (10MB) | `web_fetch` 单次最大抓取字节数 |

---

## 命令执行工具

### 危险命令拦截（deny patterns）

exec 工具内置了一套危险命令正则拦截机制。

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `enable_deny_patterns` | bool | true | 启用默认危险命令拦截 |
| `custom_deny_patterns` | array | [] | 自定义拦截正则 |
| `custom_allow_patterns` | array | [] | 自定义放行正则 |

默认拦截的命令模式包括：

- 删除命令：`rm -rf`、`del /f/q`、`rmdir /s`
- 磁盘操作：`format`、`mkfs`、`diskpart`、`dd if=`、写入 `/dev/sd*`
- 系统操作：`shutdown`、`reboot`、`poweroff`
- 命令替换：`$()`、`${}`、反引号
- 管道到 shell：`| sh`、`| bash`
- 权限提升：`sudo`、`chmod`、`chown`
- 进程控制：`pkill`、`killall`、`kill -9`
- 远程操作：`curl | sh`、`wget | sh`、`ssh`
- 包管理：`apt`、`yum`、`dnf`、`npm install -g`、`pip install --user`
- 容器：`docker run`、`docker exec`
- Git：`git push`、`git force`
- 其他：`eval`、`source *.sh`

配置示例：

```json
{
  "tools": {
    "exec": {
      "enable_deny_patterns": true,
      "custom_deny_patterns": ["\\brm\\s+-r\\b", "\\bkillall\\s+python"]
    }
  }
}
```

### Docker Sandbox（backend=docker）

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

注意事项：
- `docker` 后端要求 `restrict_to_workspace=true`
- 当前 `docker` 后端不支持 `background=true` / `yield_ms`（建议用 `host` 后端配合 process tool 做长任务）

### 环境变量控制（exec.env）

exec 工具支持通过 `env` 配置项控制命令执行时的环境变量传递。默认模式为 `allowlist`，仅传递白名单中的环境变量（`PATH`、`HOME`、`LANG`、代理变量等），防止 API key 等敏感信息泄露到子进程。

---

## MCP Bridge（挂载外部工具生态）

X-Claw 支持把 MCP（Model Context Protocol）服务器上的工具动态注册为原生工具。

核心特性：
- 启动时（best-effort）发现 MCP server 工具并注册到 `ToolRegistry`
- MCP tool call 统一走现有 tool executor：自动纳入 Tool Trace / Run Trace / 超时与错误模板
- 工具命名隔离：默认前缀 `mcp_<server>_`，也可自定义 `tool_prefix`

### 全局配置

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `enabled` | bool | false | 全局启用 MCP 集成 |
| `servers` | object | `{}` | server 名称到配置的映射 |

### 单 Server 配置

| 配置项 | 类型 | 是否必填 | 说明 |
|--------|------|----------|------|
| `enabled` | bool | 是 | 启用此 MCP server |
| `type` | string | 否 | 传输类型：`stdio`、`sse`、`http` |
| `command` | string | stdio 必填 | stdio 模式的可执行命令 |
| `args` | array | 否 | 命令参数 |
| `env` | object | 否 | stdio 进程的环境变量 |
| `env_file` | string | 否 | 环境变量文件路径 |
| `url` | string | sse/http 必填 | `sse`/`http` 模式的端点 URL |
| `headers` | object | 否 | `sse`/`http` 模式的 HTTP 头 |

### 传输行为

- 如果省略 `type`，会自动检测：
  - 设置了 `url` 则使用 `sse`
  - 设置了 `command` 则使用 `stdio`
- `http` 和 `sse` 都使用 `url` + 可选 `headers`
- `env` 和 `env_file` 仅应用于 `stdio` 模式

### 配置示例

stdio 模式：

```json
{
  "tools": {
    "mcp": {
      "enabled": true,
      "servers": {
        "filesystem": {
          "enabled": true,
          "command": "npx",
          "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]
        }
      }
    }
  }
}
```

远程 SSE/HTTP 模式：

```json
{
  "tools": {
    "mcp": {
      "enabled": true,
      "servers": {
        "remote-mcp": {
          "enabled": true,
          "type": "sse",
          "url": "https://example.com/mcp",
          "headers": {
            "Authorization": "Bearer YOUR_TOKEN"
          }
        }
      }
    }
  }
}
```

README 中的 stdio 示例（带 `include_tools` 和 `tool_prefix`）：

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

---

## Cron 工具

Cron 工具用于调度周期性任务。

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `exec_timeout_minutes` | int | 5 | 执行超时（分钟），0 表示不限 |

Cron 的任务状态会持久化到工作区：`<workspace>/cron/jobs.json`

其中 `state` 会记录：
- `lastStatus` / `lastRunAtMs` / `lastDurationMs`
- `lastOutputPreview`（截断预览）
- `runHistory`（最近 N 次运行记录）

### 无更新不提醒（NO_UPDATE）

对于"抓取/巡检/日报"类定时任务，经常会出现"本次没有新内容"的情况。为了减少打扰，可以在任务提示词里约定：

- **如果没有新发现，最终回复必须是 `NO_UPDATE`**（大小写不敏感）

当 cron 的 `deliver=false`（让 agent 处理）时，X-Claw 会把 `NO_UPDATE` 视为"无更新"，从而：
- 不向聊天渠道推送 `Cron job '...' completed` 消息（安静）
- 但 `jobs.json` 的 `runHistory/lastOutputPreview` 仍会记录 `NO_UPDATE`（便于运维/回放）

---

## Skills 工具

Skills 工具配置技能发现和安装，通过 ClawHub 等注册表获取社区技能包。

### 注册表配置

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `registries.clawhub.enabled` | bool | true | 启用 ClawHub 注册表 |
| `registries.clawhub.base_url` | string | `https://clawhub.ai` | ClawHub 基础 URL |
| `registries.clawhub.search_path` | string | `/api/v1/search` | 搜索 API 路径 |
| `registries.clawhub.skills_path` | string | `/api/v1/skills` | 技能 API 路径 |
| `registries.clawhub.download_path` | string | `/api/v1/download` | 下载 API 路径 |

### 配置示例

```json
{
  "tools": {
    "skills": {
      "registries": {
        "clawhub": {
          "enabled": true,
          "base_url": "https://clawhub.ai",
          "search_path": "/api/v1/search",
          "skills_path": "/api/v1/skills",
          "download_path": "/api/v1/download"
        }
      },
      "max_concurrent_searches": 2,
      "search_cache": {
        "max_size": 50,
        "ttl_seconds": 300
      }
    }
  }
}
```

---

## Tool Trace / Run Trace

### Tool Trace（工具调用可追溯 / 可复盘）

当 `tools.trace.enabled=true` 时，每一次 tool call 都会追加写入一个 JSONL 事件流，并可选写 per-call 文件，便于排查"模型为什么调用了某个工具 / 工具到底返回了什么 / 耗时多少"。

默认落盘位置（当 `tools.trace.dir` 为空时）：

- `<workspace>/.x-claw/audit/tools/<session>/events.jsonl`
- `<workspace>/.x-claw/audit/tools/<session>/calls/*.json|*.md`（当 `write_per_call_files=true`）

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `enabled` | bool | false | 启用 Tool Trace |
| `dir` | string | "" | 自定义落盘目录（空则使用默认位置） |
| `write_per_call_files` | bool | true | 为每次调用写独立文件 |
| `max_arg_preview_chars` | int | 200 | JSONL 中参数预览截断长度 |
| `max_result_preview_chars` | int | 400 | JSONL 中结果预览截断长度 |

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

Tool Trace 解决的是"每次工具调用发生了什么"；Run Trace 解决的是"这一次用户请求（run）整体发生了什么"（LLM 回合、工具批次、最终输出）。

当 `tools.trace.enabled=true` 时，X-Claw 会额外为每个 session 维护一个 run 级 JSONL 事件流（append-only），用于：
- 故障定位（某一轮为什么走偏 / 哪个 batch 出错）
- 长任务可恢复（断点续跑的基础）
- 与 `x-claw export` 一起形成"可回放执行"的最小证据链

默认落盘位置：

- `<workspace>/.x-claw/audit/runs/<session>/events.jsonl`

---

## Tool Policy（统一策略层）

X-Claw 在 tool executor 的统一入口增加了策略层，所有内置 tools + MCP tools 一视同仁，用于收口安全与审计需求。

### 核心能力

- **allow/deny**：名单与前缀过滤
- **统一 timeout**：每个 tool call 的最大墙钟时间
- **脱敏**：避免 secret 落盘到 trace/ledger；可选对 LLM/User 输出也脱敏
- **副作用确认**：两段提交——先要求确认，再执行
- **幂等/可恢复**：resume 时避免重复执行副作用工具

### 基础配置（审计脱敏 + timeout）

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

### 副作用确认 + 幂等重放

推荐仅在 resume 流程开启确认门：

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

### allow / deny 配置

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `allow` | []string | [] | 允许的工具名列表（空表示不限） |
| `allow_prefixes` | []string | [] | 允许的工具名前缀 |
| `deny` | []string | [] | 拒绝的工具名列表 |
| `deny_prefixes` | []string | [] | 拒绝的工具名前缀 |

### 脱敏配置（redact）

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `enabled` | bool | true | 启用脱敏 |
| `apply_to_llm` | bool | false | 对 LLM 输出也脱敏 |
| `apply_to_user` | bool | false | 对用户可见输出也脱敏 |
| `json_fields` | []string | (见下方) | 需脱敏的 JSON 字段名 |
| `patterns` | []string | (见下方) | 需脱敏的正则模式 |

默认脱敏字段：`api_key`、`apikey`、`token`、`access_token`、`refresh_token`、`secret`、`password`、`authorization`、`cookie`

### 行为说明

- 当某个 tool call 被策略层拦下时，tool output 会返回结构化 JSON（`kind=tool_policy_*`），模型应先向用户请求确认，再调用 `tool_confirm(confirm_key)`
- 幂等 ledger 会落在：`<workspace>/.x-claw/audit/runs/<session>/runs/<run_id>/policy.jsonl`
- Tool Trace 的 JSONL 事件会携带 `run_id`、`policy_decision`、`idempotency_key` 等字段，便于线上排障与复盘

---

## 统一工具错误模板

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

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `enabled` | bool | true | 启用错误模板 |
| `include_schema` | bool | true | 在错误输出中包含工具参数 schema |

---

## 配置来源说明

X-Claw 的工具配置**仅从 `config.json` 读取**。环境变量覆盖工具配置的方式被刻意不支持，以保持部署环境的可复现性。

所有工具配置的完整参考请查看 `config/config.example.json` 中的 `tools` 部分。
