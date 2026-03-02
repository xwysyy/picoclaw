# AGENTS.md (Project-Level)

## Scope
This file applies to the `picoclaw` project root.

## Git Upstream and Periodic Sync

Current remote setup:
- `origin`: `https://github.com/xwysyy/picoclaw.git` (your fork)
- `upstream`: `https://github.com/sipeed/picoclaw.git` (original project)

If `upstream` is missing, add it:
- `git remote add upstream https://github.com/sipeed/picoclaw.git`

### Periodic sync routine (recommended: merge)
Run these commands regularly to sync latest upstream changes into your fork branch (keeps history non-linear, avoids force-push):
1. `source ~/.zshrc && proxy_on && git fetch upstream`
2. `git checkout main`
3. `GIT_EDITOR=true git merge --no-edit upstream/main`
4. `source ~/.zshrc && proxy_on && git push origin main`
5. Restart currently running `picoclaw` containers:
   - `docker ps -q --filter label=com.docker.compose.project=picoclaw | xargs -r docker restart`
6. Verify current runtime status:
   - `docker ps --filter label=com.docker.compose.project=picoclaw`
   - `curl -sS http://127.0.0.1:18790/health`

If you prefer a linear history (rebase), use:
- `git rebase upstream/main`
- then push with `git push --force-with-lease origin main`

## Known API Error: `No tool output found for function call`

### Symptom
OpenAI Responses API returns HTTP 400 with message:
`No tool output found for function call <call_id>`.

### Meaning
The request includes a `function_call` item, but the next round input is missing the matching
`function_call_output` for the same `call_id`.

### Project-specific root cause we fixed
In this repo, the failure was caused by history sanitization dropping valid tool outputs when
one assistant turn emitted multiple tool calls.

- Fixed logic: `pkg/agent/context.go` (`sanitizeHistoryForProvider`)
- Behavior now:
  - Tracks pending tool call IDs for an assistant tool-call turn.
  - Preserves multiple consecutive tool outputs from the same turn.
  - Drops truly orphaned tool outputs (unknown or empty `tool_call_id` when IDs are required).

### Regression tests
- `pkg/agent/context_test.go`
- Run:
  - `go test ./pkg/agent -run TestSanitizeHistoryForProvider -count=1`

If toolchain/dependency download needs proxy in this environment, run:
- `source ~/.zshrc && proxy_on && go test ./pkg/agent -run TestSanitizeHistoryForProvider -count=1`

## Docker Redeploy (Important: profiles are required)

Compose file lives at `docker/docker-compose.yml` and defines services under profiles (`agent`, `gateway`).
Running `docker compose up -d --build` without profile may fail with:
`no service selected`.

### Gateway redeploy
1. `cd docker`
2. `docker compose -p picoclaw down`
3. `docker compose -p picoclaw --profile gateway up -d --build`
4. `docker compose -p picoclaw ps`
5. `curl -sS http://127.0.0.1:18790/health`

Expected healthy state:
- container: `picoclaw-gateway`
- compose status: `Up ... (healthy)`
- health response contains `"status":"ok"`

### Useful checks
- `docker logs --tail 100 picoclaw-gateway`
- `docker ps --filter name=picoclaw-gateway`

## Proxy note for non-interactive shells

`proxy_on` is a shell function defined in `~/.zshrc` (not a standalone binary).
In non-interactive command contexts, use:

- `source ~/.zshrc && proxy_on && <your-command>`

## Post-change Notification (Feishu / last_active)

When finishing a coding task (feature/fix/docs), proactively notify the user via the running Gateway.

- Preferred: call `POST /api/notify` without `channel/to` so it targets `last_active`.
- If `gateway.api_key` is configured, include `Authorization: Bearer <api_key>`.
- This is intended for "done/ready-to-review" pings (e.g., after redeploy).

Example:

- `curl -sS -X POST http://127.0.0.1:18790/api/notify -H 'Content-Type: application/json' -d '{"content":"✅ PicoClaw: change complete (redeployed)."}'`
