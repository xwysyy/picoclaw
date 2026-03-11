# 运维操作

本文档介绍 X-Claw 的日常运维操作接口与命令。

---

## Resume last task（/api/resume_last_task）

Gateway 提供"断点续跑"接口：定位最近一次 **未正常结束** 的 run，并在同一 `run_id` 上继续执行（append-only 续写 run trace）。

### 调用示例

```bash
curl -sS -X POST http://127.0.0.1:18790/api/resume_last_task
```

### 鉴权规则

鉴权规则同 `/api/notify`：

| 场景 | 行为 |
|------|------|
| `gateway.api_key` 为空 | 仅允许 loopback（127.0.0.1）访问 |
| `gateway.api_key` 非空 | 携带 `Authorization: Bearer <api_key>` 或 `X-API-Key: <api_key>` |

### 安全建议

强烈建议同时开启 `tools.policy.confirm` + `tools.policy.idempotency`，避免 resume 时重复执行 `exec` / 写文件 / MCP 写操作类工具。

---

## Run/Session 导出（x-claw export）

当你需要提交 bug / 复盘某次对话 / 把"可回放执行"资料打包给别人时，可以导出一个 zip bundle（默认会包含：session 快照 + tool traces + run traces + cron/state/config 脱敏快照 + manifest）。

### 常用用法

```bash
# 直接导出当前 workspace 的 last_active 会话（推荐）
./build/x-claw export --last-active

# 或导出指定 sessionKey
./build/x-claw export --session 'agent:main:feishu:group:oc_xxx'
```

### 默认输出位置

```
<workspace>/exports/*.zip
```

---

## Console API 速查

Gateway Console 提供以下 JSON API，便于脚本化操作：

| 端点 | 说明 |
|------|------|
| `GET /api/console/status` | 查看 `last_active` / 基础状态 |
| `GET /api/console/cron` | 查看 cron jobs 状态 |
| `GET /api/console/tokens` | 查看 token usage（按模型统计累计 tokens） |
| `GET /api/console/sessions` | 查看 sessions 列表（元数据） |
| `GET /api/console/runs` | 查看 run traces |
| `GET /api/console/tools` | 查看已注册工具列表 |
| `GET /api/console/file?path=<path>` | 下载 workspace 内的审计/状态文件 |
| `GET /api/console/tail?path=<path>&lines=N` | 获取文件末尾 N 行 |
| `GET /api/console/stream?path=<path>&tail=N` | 实时跟随文件（tail -f 风格） |

### 使用示例

```bash
# 查看状态
curl -sS http://127.0.0.1:18790/api/console/status

# 查看 cron 任务
curl -sS http://127.0.0.1:18790/api/console/cron

# 查看 token 用量
curl -sS http://127.0.0.1:18790/api/console/tokens

# 查看 sessions
curl -sS http://127.0.0.1:18790/api/console/sessions

# 查看 run traces
curl -sS http://127.0.0.1:18790/api/console/runs

# 查看已注册工具
curl -sS http://127.0.0.1:18790/api/console/tools

# 下载文件
curl -sS -OJ "http://127.0.0.1:18790/api/console/file?path=cron/jobs.json"

# 获取 events.jsonl 末尾 200 行
curl -sS "http://127.0.0.1:18790/api/console/tail?path=.x-claw/audit/runs/<session>/events.jsonl&lines=200"

# 实时跟随
curl -N -sS "http://127.0.0.1:18790/api/console/stream?path=.x-claw/audit/runs/<session>/events.jsonl&tail=200"
```

### 鉴权

- `gateway.api_key` 为空：仅允许 loopback 访问
- `gateway.api_key` 非空：需携带 `Authorization: Bearer <api_key>`

### 文件下载安全

`/api/console/file` 只允许下载以下目录中的白名单后缀文件：

- `.x-claw/audit/` 目录
- `cron/` 目录
- `state/` 目录
- 允许后缀：`.json`、`.jsonl`、`.md` 等

---

## 健康检查与就绪探针

Gateway 提供以下端点用于健康检查和就绪探针，适合 Kubernetes / Docker 健康检查配置：

| 端点 | 说明 | 用途 |
|------|------|------|
| `/health` | 存活探针 | 确认进程存活 |
| `/healthz` | 同 `/health` | Kubernetes 风格别名 |
| `/ready` | 就绪探针 | 确认服务已准备好接收请求 |
| `/readyz` | 同 `/ready` | Kubernetes 风格别名 |

### 使用示例

```bash
# 存活检查
curl -sS http://127.0.0.1:18790/health

# 就绪检查
curl -sS http://127.0.0.1:18790/ready
```

### Docker Compose 健康检查配置

```yaml
healthcheck:
  test: ["CMD", "curl", "-f", "http://127.0.0.1:18790/health"]
  interval: 30s
  timeout: 5s
  retries: 3
```
