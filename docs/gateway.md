# Gateway 功能详解

X-Claw 的 Gateway 模式是推荐的主入口，Feishu / Telegram 等渠道由 Gateway 统一接入与分发。本文档详细介绍 Gateway 模式下的各项功能与配置。

启动 Gateway：

```bash
./build/x-claw gateway
```

健康检查：

```bash
curl -sS http://127.0.0.1:18790/health
```

---

## Inbound 队列（gateway.inbound_queue）

为了让 Gateway 能长期稳定跑多会话/长任务，X-Claw 支持 **按 session 分桶串行** + **全局并发上限** 的 inbound 队列：

- **同一会话串行**：避免上下文/记忆写入竞争
- **不同会话并发**：避免一个会话长任务拖死全部

### 配置示例

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

| 字段 | 说明 |
|------|------|
| `enabled` | 是否启用 inbound 队列 |
| `max_concurrency` | 全局最大并发数 |
| `per_session_buffer` | 每个 session 的缓冲区大小 |

---

## 配置热更新（gateway.reload）

为了减少"改配置就得重启/重建容器"的频率，Gateway 支持 **热更新**（仅 Gateway 模式）：

- **SIGHUP**：收到 `SIGHUP` 时尝试 reload（需要 `gateway.reload.enabled=true`）
- **watch**：轮询配置文件变更并自动 reload（适合 Docker bind-mount；需要 `gateway.reload.watch=true`）

### 配置示例

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

### 触发方式

```bash
# 方式 1：发 SIGHUP（仅当 enabled=true 时生效）
kill -HUP <x_claw_pid>
```

### Reload 覆盖范围

当前 reload 覆盖范围刻意保持小而可控：

- **会**重启 channels + 重新注册 webhook/HTTP handlers（含 `/api/notify`、`/api/console/*` 等）
- **会**把新 config 应用到 agent loop（含 notify/tool policy/MCP server 配置），并刷新 MCP tools 注册表
- **不会**重启 cron/heartbeat/provider（这些通常需要"更重"的重启语义）

---

## Gateway Console（/console/）

Gateway 内置一个**只读 Console**（Web UI，Next.js + shadcn/ui），用于自助查看：

- `last_active` / 基础状态
- cron jobs（`cron/jobs.json`）
- token usage（`state/token_usage.json`，按模型统计累计 tokens）
- sessions 列表（元数据：`sessions/*.meta.json`；事件流：`sessions/*.jsonl`）
- run trace / tool trace（`<workspace>/.x-claw/audit/**/events.jsonl`）
- 健康检查链接（`/health` / `/ready`）

### 访问方式

```bash
# 浏览器访问
http://127.0.0.1:18790/console/
```

### 鉴权规则

鉴权规则同 `/api/notify`：

- 当 `gateway.api_key` 为空时：`/console/` 与 `/api/console/*` 仅允许来自本机 loopback 的访问
- 当 `gateway.api_key` 非空时：`/console/` 可以直接在浏览器打开，但 `/api/console/*` 需要携带 `Authorization: Bearer <api_key>`
  （UI 内置了 API key 输入框，会用 Bearer header 拉取数据）

### Console JSON API

Console 对应的 JSON API（便于脚本化）：

```bash
curl -sS http://127.0.0.1:18790/api/console/status
curl -sS http://127.0.0.1:18790/api/console/cron
curl -sS http://127.0.0.1:18790/api/console/tokens
curl -sS http://127.0.0.1:18790/api/console/sessions
curl -sS http://127.0.0.1:18790/api/console/runs
curl -sS http://127.0.0.1:18790/api/console/tools
```

### 文件下载

下载 workspace 内的审计/状态文件（只允许 `.x-claw/audit/`、`cron/`、`state/` 下的 `.json/.jsonl/.md` 等白名单文件）：

```bash
curl -sS -OJ "http://127.0.0.1:18790/api/console/file?path=cron/jobs.json"
```

### Tail（末尾 N 行）

仅取末尾 N 行（适合 `events.jsonl`，避免下载整文件）：

```bash
curl -sS "http://127.0.0.1:18790/api/console/tail?path=.x-claw/audit/runs/<session>/events.jsonl&lines=200"
```

### Stream（实时跟随）

实时跟随（tail -f 风格，适合观察长任务进度；UI 里 Traces 也提供 Live 按钮）：

```bash
# -N: curl 不缓冲输出
curl -N -sS "http://127.0.0.1:18790/api/console/stream?path=.x-claw/audit/runs/<session>/events.jsonl&tail=200"
```

---

## 通知接口（/api/notify）

Gateway 会额外暴露一个通知接口，用于让外部系统（CI / 脚本 / 守护进程）通过已配置的渠道给你发提醒（例如飞书）。

### 指定渠道与收件人

```bash
curl -sS -X POST http://127.0.0.1:18790/api/notify \
  -H 'Content-Type: application/json' \
  -d '{"channel":"feishu","to":"oc_xxx","content":"任务完成了"}'
```

### 使用 last_active 发送

如果省略 `channel/to`，会默认发送到最近一次对话的 `last_active`：

```bash
curl -sS -X POST http://127.0.0.1:18790/api/notify \
  -H 'Content-Type: application/json' \
  -d '{"content":"任务完成了（last_active）"}'
```

> **注意**：如果 gateway 刚启动、还没有任何外部对话记录（`last_active` 为空），省略 `channel/to` 会返回 `channel is required`。此时请先在飞书/Telegram 等渠道给机器人发一句话建立 `last_active`，或显式指定 `channel/to`。

### 安全说明

- 当 `gateway.api_key` 为空时，仅允许来自本机 loopback 的请求（例如 `127.0.0.1`）
- 当 `gateway.api_key` 设置为非空时，请携带 `Authorization: Bearer <api_key>`（否则返回 401）

### 公网暴露建议

如需远程/跨机器通知：

- 强烈不建议在 `gateway.api_key` 为空时暴露公网
- 建议优先使用反向代理（HTTPS）或私网方案（如 Tailscale）再对外提供 `/api/notify`
- 如必须直连：将 `gateway.host` 设为 `0.0.0.0` 并配置强随机 `gateway.api_key`

外部 Agent（例如 Claude Code / Codex）对接 X-Claw 通知的扩展文档见：`extensions/x-claw-notify/SKILL.md`（通过调用 `/api/notify` 推送提醒）。

---

## 群聊触发规则（group_trigger）

### 现象

在飞书/Telegram 群里发消息，机器人不理你；但 `/health` 正常、`/api/notify` 也能给你推送消息。

### 原因

X-Claw 对群聊采用 **safe-by-default** 策略，默认只在满足触发条件时才回复群消息，否则直接忽略（避免群里过吵）。

### 触发条件

群聊触发由 `channels.<channel>.group_trigger` 控制：

| 字段 | 说明 |
|------|------|
| `mention_only=true` | 必须 `@机器人` 才回复（更安全） |
| `command_bypass=true` | 以 `/`（或自定义 `command_prefixes`）开头的命令可绕过 `@` 限制 |
| `prefixes=["/ask", "!"]` | 以指定前缀开头才触发（触发后会自动剥离前缀） |
| `mentionless=true` | 群聊不需要 `@` 也会回复（最"放开"，但也最吵） |

### allow_from（允许列表）

`allow_from` 在群聊触发之前就会生效：

- `allow_from=[]` 表示允许所有人
- 非空表示只允许名单中的 sender（支持 `"platform:id"`、纯数字 id、`"@username"`、`"id|username"` 等格式）

### 配置示例

允许飞书群聊不需要 `@` 也回复（适合"群里只有你自己"的场景）：

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

### 排查建议

如果你不确定消息是否"入站成功"，可以先用 `/api/notify` 验证 **出站** 是否正常；若出站正常但入站不触发，优先检查 `group_trigger` 和 `allow_from`。

---

## 任务完成提醒（notify.on_task_complete）

当你把 `notify.on_task_complete=true` 时，X-Claw 会在 **内部通道**（`cli/system`）的一次 run 正常结束后，自动通过 `message` tool 把"任务完成 + 结果摘要"发到 `last_active` 外部会话。

### 静默约定（少打扰）

如果内部任务最终输出为 `NO_UPDATE` 或 `HEARTBEAT_OK`（大小写不敏感），则不会触发完成提醒。

这适合 cron/后台巡检类任务：无更新不打扰，有更新才提醒。

---

## Web 证据模式（tools.web.evidence_mode）

当你把 `tools.web.evidence_mode.enabled=true` 时：

- `web_search` 会返回结构化 JSON（包含 `sources[]` 和 `evidence.satisfied/distinct_domains`）
- system prompt 会强制"事实/最新信息"回答引用 >=2 个不同域名来源；否则明确不确定性并给出验证建议

### 配置示例

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

---

## Plan Mode（计划阶段）

Plan Mode 用于把"工具执行"从默认放开，升级为"先计划、再审批"：

| 命令 | 说明 |
|------|------|
| `/plan <任务>` | 进入计划阶段（此时会拒绝 `exec/edit/write` 等危险工具） |
| `/approve` 或 `/run` | 批准并执行刚才计划阶段捕获的任务 |
| `/cancel` | 退出计划阶段并清空 pending task |
| `/mode` | 查看当前 mode 和 pending task 预览 |

---

## 上下文压缩（Compaction）

当会话历史过长（接近模型 context window）时，X-Claw 会在后台对历史进行总结/压缩，以提升长期运行稳定性。

### 默认行为

**不会**在飞书/Telegram 里额外发"正在压缩上下文"的提示（避免太吵）。

### 开启压缩提示

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
