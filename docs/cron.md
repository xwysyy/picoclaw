# 定时任务（Cron）

X-Claw 支持通过 cron 配置定时执行任务，任务状态会持久化到工作区，便于运维与回放。定时任务适用于周期性数据抓取、巡检、日报生成等场景。

---

## 任务状态持久化（runHistory / lastStatus）

Cron 的任务状态会持久化到工作区：

```
<workspace>/cron/jobs.json
```

其中 `state` 会记录：

| 字段 | 说明 |
|------|------|
| `lastStatus` | 最近一次运行状态（`ok` / `error` 等） |
| `lastRunAtMs` | 最近一次运行的时间戳（毫秒） |
| `lastDurationMs` | 最近一次运行的耗时（毫秒） |
| `lastOutputPreview` | 最近一次输出的截断预览 |
| `runHistory` | 最近 N 次运行记录（数组） |

### 查看方式

可以通过 Console API 查看 cron 状态：

```bash
curl -sS http://127.0.0.1:18790/api/console/cron
```

也可以直接下载 `jobs.json`：

```bash
curl -sS -OJ "http://127.0.0.1:18790/api/console/file?path=cron/jobs.json"
```

### runHistory 结构

`runHistory` 是一个数组，记录最近 N 次运行的摘要信息，便于快速回顾任务执行历史。每条记录包含运行时间、耗时、状态和输出预览。

---

## 无更新不提醒（NO_UPDATE 静默约定）

对于"抓取/巡检/日报"类定时任务，经常会出现"本次没有新内容"的情况。为了减少打扰，你可以在任务提示词里约定：

> **如果没有新发现，最终回复必须是 `NO_UPDATE`**（大小写不敏感）

### 工作原理

当 cron 的 `deliver=false`（让 agent 处理）时，X-Claw 会把 `NO_UPDATE` 视为"无更新"，从而：

- **不向聊天渠道推送** `Cron job '...' completed` 消息（安静）
- 但 `jobs.json` 的 `runHistory/lastOutputPreview` **仍会记录** `NO_UPDATE`（便于运维/回放）

### 静默关键词

以下关键词（大小写不敏感）会被视为"无需通知"：

| 关键词 | 适用场景 |
|--------|----------|
| `NO_UPDATE` | 抓取/巡检类任务无新内容 |
| `HEARTBEAT_OK` | 心跳/健康检查任务 |

### 提示词示例

在 cron 任务的提示词中加入约定：

```
请检查 https://example.com/feed 是否有新文章。
- 如果有新内容，总结标题和链接
- 如果没有新发现，仅回复 NO_UPDATE
```

### 与任务完成提醒的配合

当 `notify.on_task_complete=true` 时，如果内部任务最终输出为 `NO_UPDATE` 或 `HEARTBEAT_OK`（大小写不敏感），同样不会触发完成提醒。这确保了"无更新不打扰"的一致性。详见 [Gateway 功能详解](gateway.md) 中的"任务完成提醒"章节。
