# API Reference

X-Claw Gateway exposes a set of HTTP endpoints for health checking, notification, control, and console access. All endpoints are served on the address configured by `gateway.host` and `gateway.port` (default `127.0.0.1:18790`).

## Health & Readiness

These endpoints require no authentication and are always accessible.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Health check (returns 200 when the process is alive) |
| GET | `/healthz` | Health check (alias for `/health`) |
| GET | `/ready` | Readiness probe (returns 200 when the gateway is fully initialized) |
| GET | `/readyz` | Readiness probe (alias for `/ready`) |

## Notification & Control

These endpoints are protected by the gateway API key (see [Authentication](#authentication) below).

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/notify` | Send a notification message via a configured channel |
| POST | `/api/resume_last_task` | Resume the last incomplete run (append to existing run trace) |
| GET/POST | `/api/session_model` | GET: query per-session model override; POST: set per-session model override |

### POST /api/notify

Send a message through a configured channel.

```json
{
  "channel": "feishu",
  "to": "oc_xxx",
  "content": "Task completed"
}
```

If `channel` and `to` are omitted, the message is sent to the most recent `last_active` conversation.

### POST /api/resume_last_task

Locate the most recent incomplete run and resume execution on the same `run_id`. Returns the resume candidate and response. Recommended to enable `tools.policy.confirm` and `tools.policy.idempotency` when using this endpoint.

### GET/POST /api/session_model

- **GET**: Returns the current model override for a session (query param: `session`).
- **POST**: Sets a model override for a session.

## Console API

All `/api/console/*` endpoints are read-only (GET/HEAD only) and protected by the gateway API key.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/console/` | List available console endpoints |
| GET | `/api/console/status` | Runtime status (last_active, model, feature flags) |
| GET | `/api/console/state` | Full `state.json` contents |
| GET | `/api/console/cron` | Cron jobs summary (`cron/jobs.json`) |
| GET | `/api/console/tokens` | Token usage statistics (`state/token_usage.json`) |
| GET | `/api/console/sessions` | Session list (metadata from `sessions/*.meta.json`) |
| GET | `/api/console/runs` | Run trace list (from `.x-claw/audit/runs/`) |
| GET | `/api/console/tools` | Tool trace list (from `.x-claw/audit/tools/`) |
| GET | `/api/console/file?path=X` | Download a workspace file (allowlisted paths only) |
| GET | `/api/console/tail?path=X&lines=N` | Tail last N lines of a file (useful for `events.jsonl`) |
| GET | `/api/console/stream?path=X&tail=N` | SSE stream (tail -f style, for live observation) |

### File access restrictions

The `/api/console/file`, `/api/console/tail`, and `/api/console/stream` endpoints only allow access to files under whitelisted directories (`.x-claw/audit/`, `cron/`, `state/`) with whitelisted extensions (`.json`, `.jsonl`, `.md`, etc.).

## Console UI

| Method | Path | Description |
|--------|------|-------------|
| GET | `/console/` | Web UI (Next.js + shadcn/ui) for browsing status, sessions, traces, and cron jobs |

The Console UI is served as static files. When `gateway.api_key` is set, the UI page itself is accessible in a browser, but it uses the API key (entered via a built-in input box) to call `/api/console/*` endpoints. When `gateway.api_key` is empty, both the UI and API are restricted to loopback access.

## Channel Webhooks (dynamic)

Channel webhook paths are configured per-channel in `config.json`. They are registered dynamically at gateway startup. Common examples:

| Channel | Default Webhook Path |
|---------|---------------------|
| LINE | `/webhook/line` |
| WeCom | `/webhook/wecom` |
| WeCom App | `/webhook/wecom-app` |
| WeCom AI Bot | `/webhook/wecom-aibot` |

Feishu and Telegram use long-polling or event subscription mechanisms rather than HTTP webhooks on the gateway, so they do not register webhook paths on the gateway mux.

## Authentication

### Rules summary

| Endpoint Pattern | `gateway.api_key` empty | `gateway.api_key` set |
|-----------------|------------------------|----------------------|
| `/health`, `/healthz`, `/ready`, `/readyz` | No auth required | No auth required |
| `/api/*` (notify, resume, session_model, console API) | Loopback only (127.0.0.1) | Requires `Authorization: Bearer <key>` or `X-API-Key: <key>` |
| `/console/` (UI) | Loopback only | Accessible from any address (browser-friendly) |
| Channel webhooks | Validated by channel-specific mechanisms | Validated by channel-specific mechanisms |

### Header formats

When `gateway.api_key` is set, API requests must include one of:

```
Authorization: Bearer <api_key>
```

or

```
X-API-Key: <api_key>
```

### Security recommendations

- **Never** expose the gateway on `0.0.0.0` without setting `gateway.api_key`
- For remote access, prefer a reverse proxy with HTTPS or a private network (e.g., Tailscale)
- If direct exposure is required, set `gateway.host` to `0.0.0.0` and configure a strong random `gateway.api_key`
