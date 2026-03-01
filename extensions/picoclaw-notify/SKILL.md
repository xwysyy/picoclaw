---
name: picoclaw-notify
description: External-agent extension. Notify a running PicoClaw Gateway via `POST /api/notify` (Claude Code / Codex / CI scripts).
---

# PicoClaw Notify (Extension)

This is **NOT** a PicoClaw workspace skill.

It is an **external-agent extension**: use it in agents like **Claude Code** / **Codex** / CI runners so that after they finish work, they can notify a running **PicoClaw Gateway**, and PicoClaw will then forward the message to your configured chat channel (e.g. Feishu).

## What this extension does

- Call PicoClaw Gateway HTTP API: `POST /api/notify`
- PicoClaw routes the message to:
  - a specified `channel + to/chat_id`, or
  - the current `last_active` conversation (if both omitted)

## Preconditions

1) PicoClaw Gateway is running and reachable (default):
- `http://127.0.0.1:18790/health` returns `{"status":"ok",...}`

2) Security:
- Local-only (default): leave `gateway.api_key` empty → only loopback requests allowed.
- Remote / public: **must** set `gateway.api_key`, and you **must** send it in the request.

## Configuration (recommended as env vars)

Set these in your external agent environment:

- `PICOCLAW_NOTIFY_URL`  
  - Example (local): `http://127.0.0.1:18790/api/notify`
  - Example (remote via reverse proxy): `https://your-domain.example.com/api/notify`
  - Example (Tailscale Serve): `https://<device>.<tailnet>.ts.net/api/notify`

- `PICOCLAW_NOTIFY_API_KEY` (optional but strongly recommended for remote)  
  - Must match `gateway.api_key` on the PicoClaw side.

Optional routing (omit both to use `last_active`):
- `PICOCLAW_NOTIFY_CHANNEL` (e.g. `feishu`)
- `PICOCLAW_NOTIFY_TO` (e.g. `oc_xxx`)

## Usage patterns

### Pattern A: Send to `last_active` (simple, needs prior chat)

Prerequisite: you have already talked to PicoClaw once on the target channel so `last_active` is set.

```bash
curl -sS -X POST "${PICOCLAW_NOTIFY_URL}" \
  -H "Content-Type: application/json" \
  ${PICOCLAW_NOTIFY_API_KEY:+-H "Authorization: Bearer ${PICOCLAW_NOTIFY_API_KEY}"} \
  -d "{\"content\":\"✅ Task completed.\"}"
```

### Pattern B: Send to a specific destination (recommended for CI)

```bash
curl -sS -X POST "${PICOCLAW_NOTIFY_URL}" \
  -H "Content-Type: application/json" \
  ${PICOCLAW_NOTIFY_API_KEY:+-H "Authorization: Bearer ${PICOCLAW_NOTIFY_API_KEY}"} \
  -d "{\"channel\":\"${PICOCLAW_NOTIFY_CHANNEL}\",\"to\":\"${PICOCLAW_NOTIFY_TO}\",\"content\":\"✅ Task completed.\"}"
```

## Message format guidance (avoid spam)

- Always send **one** final notification:
  - success: `✅ ...`
  - failure: `❌ ...` + short error summary
- Keep it short (1–3 lines), add a pointer instead of dumping logs:
  - repo + branch
  - artifact path
  - run/session id

## Troubleshooting

- `401 unauthorized`
  - PicoClaw has `gateway.api_key` set, but you didn’t send it (or wrong key).
  - Fix: add `Authorization: Bearer <key>` header (or `X-API-Key`).

- `400 channel is required` / `to/chat_id is required`
  - You omitted destination but `last_active` is empty/unavailable.
  - Fix: either provide `channel + to`, or chat with PicoClaw once to establish `last_active`.

- `500`
  - PicoClaw failed to send via the configured channel.
  - Fix: check PicoClaw Gateway logs and channel configuration.
