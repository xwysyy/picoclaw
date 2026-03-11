# 故障排查

本文档汇总 X-Claw 常见问题的诊断方法与解决方案。

---

## 群聊不回复

**现象**：在飞书/Telegram 群里发消息，机器人不理你；但 `/health` 正常、`/api/notify` 也能给你推送消息。

**原因**：X-Claw 对群聊采用 safe-by-default 策略，默认只在满足触发条件时才回复群消息，否则直接忽略。

### 排查步骤

1. **确认出站正常**：先用 `/api/notify` 验证出站是否正常
   ```bash
   curl -sS -X POST http://127.0.0.1:18790/api/notify \
     -H 'Content-Type: application/json' \
     -d '{"channel":"feishu","to":"oc_xxx","content":"测试出站"}'
   ```

2. **检查 `group_trigger` 配置**：确认是否启用了合适的触发条件
   - `mention_only=true`：需要 `@机器人` 才回复
   - `mentionless=true`：不需要 `@` 也回复
   - `command_bypass=true`：以 `/` 开头的命令可绕过 `@` 限制

3. **检查 `allow_from` 配置**：确认发送者是否在允许列表中
   - `allow_from=[]` 表示允许所有人
   - 非空时只允许名单中的 sender

详细配置说明见 [Gateway 功能详解 - 群聊触发规则](gateway.md#群聊触发规则grouptrigger)。

---

## Docker 权限问题（permission denied）

**现象**：容器日志提示 `permission denied` 无法读取 `/home/xclaw/.x-claw/config.json`。

**原因**：宿主机上的 `config/config.json` 权限过严（例如 `600`），容器用户无法读取。

### 解决方案

```bash
chmod 644 config/config.json
```

然后重启容器：

```bash
docker compose -p x-claw -f docker/docker-compose.yml --profile gateway restart
```

---

## 常见 API 错误

### 401 Unauthorized（未授权）

**现象**：调用 `/api/notify` 或 `/api/console/*` 返回 401。

**原因**：`gateway.api_key` 已设置为非空，但请求未携带正确的认证信息。

**解决方案**：请求时携带 `Authorization: Bearer <api_key>` header：

```bash
curl -sS -X POST http://127.0.0.1:18790/api/notify \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer YOUR_API_KEY' \
  -d '{"content":"测试"}'
```

### channel is required

**现象**：调用 `/api/notify` 时返回 `channel is required`。

**原因**：请求中省略了 `channel/to` 参数，且 Gateway 刚启动，还没有任何外部对话记录（`last_active` 为空）。

**解决方案**：

- 方案 1：在飞书/Telegram 等渠道给机器人发一句话，建立 `last_active` 记录
- 方案 2：显式指定 `channel` 和 `to` 参数：
  ```bash
  curl -sS -X POST http://127.0.0.1:18790/api/notify \
    -H 'Content-Type: application/json' \
    -d '{"channel":"feishu","to":"oc_xxx","content":"消息内容"}'
  ```

---

## 飞书事件加密参数缺失

**现象**：飞书事件回调收不到，或返回解密错误。

**原因**：飞书开放平台的事件订阅配置了加密（Encrypt Key），但 X-Claw 配置中未正确设置对应参数。

### 排查步骤

1. 登录飞书开放平台，检查应用的"事件订阅"配置
2. 确认 Encrypt Key 和 Verification Token 是否已填入 `config.json` 的渠道配置中
3. 确认事件回调 URL 是否可从飞书服务器正常访问

详细飞书配置见 [飞书渠道文档](channels/feishu/README.zh.md)。

---

## 查看容器日志

排查问题时，首先查看容器日志：

```bash
docker compose -p x-claw -f docker/docker-compose.yml logs -f x-claw-gateway
```

如果需要查看最近 N 行日志：

```bash
docker compose -p x-claw -f docker/docker-compose.yml logs --tail=200 x-claw-gateway
```

---

## 健康检查不通过

**现象**：`/health` 或 `/ready` 返回非 200 状态。

### 排查步骤

1. 确认 Gateway 进程是否在运行
2. 确认端口（默认 `18790`）是否正确
3. 查看容器日志是否有启动错误
4. 确认配置文件是否有效（JSON 格式正确、必填字段完整）

```bash
# 检查健康状态
curl -sS http://127.0.0.1:18790/health

# 检查就绪状态
curl -sS http://127.0.0.1:18790/ready
```
