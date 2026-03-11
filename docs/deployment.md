# 部署指南

本文档介绍 X-Claw 的 Docker Compose 部署方式、容器配置以及安全建议。

---

## Docker Compose

本仓库的 `docker/docker-compose.yml` 提供两个 profile：

| Profile | 用途 |
|---------|------|
| `gateway` | 常驻服务，推荐的主入口 |
| `agent` | 单次/手动执行 |

容器会挂载本地 `config/config.json`（只读）作为运行配置。

---

## 构建并启动

### 启动 Gateway（常驻服务）

```bash
docker compose -p x-claw -f docker/docker-compose.yml --profile gateway up -d --build
```

验证：

```bash
docker compose -p x-claw -f docker/docker-compose.yml ps
curl -sS http://127.0.0.1:18790/health
```

### 执行单次 Agent

```bash
docker compose -p x-claw -f docker/docker-compose.yml run --rm x-claw-agent -m "hello"
```

### 停止服务

```bash
docker compose -p x-claw -f docker/docker-compose.yml down
```

### 查看日志

```bash
docker compose -p x-claw -f docker/docker-compose.yml logs -f x-claw-gateway
```

---

## 容器内 Git 配置

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

| 字段 | 说明 |
|------|------|
| `username` | GitHub 用户名（用于 credentials） |
| `pat` | Personal Access Token |
| `user_name` | Git commit 作者名 |
| `user_email` | Git commit 作者邮箱 |
| `host` | Git 服务域名（默认 `github.com`） |
| `protocol` | 协议（`https`） |

容器启动时会自动写入 `~/.git-credentials` 和 `~/.gitconfig`。

---

## config.json 权限问题

如果容器日志提示 `permission denied` 无法读取 `/home/xclaw/.x-claw/config.json`，通常是因为宿主机上的 `config/config.json` 权限过严（例如 `600`），请确保容器用户可读：

```bash
chmod 644 config/config.json
```

---

## 配置文件说明

X-Claw 的运行时配置**只读取** `config.json`（默认路径 `~/.x-claw/config.json`；Docker 部署通常将 `config/config.json` 挂载到该位置），不支持通过环境变量覆盖配置字段，以避免"同一份代码在不同环境变量下行为漂移"。

配置模板：`config/config.example.json`

---

## 安全部署建议

### API Key 鉴权

X-Claw 的 `/api/notify`、`/api/console/*`、`/api/resume_last_task` 等接口使用统一的鉴权规则：

| 场景 | 行为 |
|------|------|
| `gateway.api_key` 为空 | 仅允许来自本机 loopback 的请求（例如 `127.0.0.1`） |
| `gateway.api_key` 非空 | 需携带 `Authorization: Bearer <api_key>` 或 `X-API-Key: <api_key>`，否则返回 401 |

### 公网暴露建议

如需远程/跨机器访问（通知、Console 等）：

- **强烈不建议**在 `gateway.api_key` 为空时暴露公网
- 建议优先使用 **反向代理（HTTPS）** 或 **私网方案**（如 Tailscale）再对外提供 API
- 如必须直连：将 `gateway.host` 设为 `0.0.0.0` 并配置强随机 `gateway.api_key`

### 安全清单

1. 设置强随机的 `gateway.api_key`
2. 使用 HTTPS 反向代理（如 Nginx / Caddy）或私网方案（如 Tailscale）
3. 不要把包含 API key 的 `config.json` 提交到公开仓库
4. 确保 `config.json` 文件权限合理（宿主机 `644`，不要设为 `777`）
5. 定期检查容器日志（`docker compose ... logs -f x-claw-gateway`）

---

## 健康检查与就绪探针

Gateway 提供以下端点用于健康检查和就绪探针：

| 端点 | 说明 |
|------|------|
| `/health` | 健康检查（存活探针） |
| `/healthz` | 同 `/health`（Kubernetes 风格别名） |
| `/ready` | 就绪探针 |
| `/readyz` | 同 `/ready`（Kubernetes 风格别名） |

```bash
curl -sS http://127.0.0.1:18790/health
curl -sS http://127.0.0.1:18790/ready
```
