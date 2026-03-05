# AGENTS.md (Project-Level)

## Scope
This file applies to the `x-claw` project root.

## Git Upstream and Periodic Sync

Current remote setup:
- `origin`: `https://github.com/xwysyy/X-Claw.git` (your fork)
- `upstream`: (original upstream project; URL intentionally not hardcoded here)

If `upstream` is missing, add it:
- `git remote add upstream <UPSTREAM_GIT_URL>`

### Periodic sync routine (recommended: merge)
Run these commands regularly to sync latest upstream changes into your fork branch (keeps history non-linear, avoids force-push):
1. `source ~/.zshrc && proxy_on && git fetch upstream`
2. `git checkout main`
3. `GIT_EDITOR=true git merge --no-edit upstream/main`
4. `source ~/.zshrc && proxy_on && git push origin main`
5. Restart currently running `x-claw` containers:
   - `docker ps -q --filter label=com.docker.compose.project=x-claw | xargs -r docker restart`
6. Verify current runtime status:
   - `docker ps --filter label=com.docker.compose.project=x-claw`
   - `curl -sS http://127.0.0.1:18790/health`

If you prefer a linear history (rebase), use:
- `git rebase upstream/main`
- then push with `git push --force-with-lease origin main`

### Upstream "Learning Mode" (no merge) + Selective Porting (recommended when heavily diverged)

When your branch is already far from `upstream/main`, doing full merges frequently is expensive.
Use this workflow to (1) continuously *learn* what upstream changed, and (2) port only the changes you actually want.

#### 1) 只看不合 / Read-only triage: `fetch` + `diff/log` to understand upstream changes

Keep `upstream` remote configured, then fetch upstream objects (no history pollution on your branch):

- `source ~/.zshrc && proxy_on && git fetch upstream`

Find the common ancestor between your `main` and `upstream/main`, then only view commits added by upstream since then:

- `BASE=$(git merge-base main upstream/main)`
- `git log --oneline --decorate $BASE..upstream/main`

Quickly estimate the change size (decide whether it is worth porting):

- `git diff --stat $BASE..upstream/main`
- `git diff --name-status $BASE..upstream/main`

If you only care about a specific directory (example: `pkg/agent` or `pkg/tools`):

- `git log --oneline $BASE..upstream/main -- pkg/agent`
- `git diff $BASE..upstream/main -- pkg/agent`

Core value: you can continuously "study upstream" without merging.

#### 2) 精选搬运 / Selective porting: use `cherry-pick` to migrate valuable upstream commits

When you identify upstream commits that are valuable (bugfix, small optimization, tests, docs, tool improvements), port them explicitly.

Before porting, inspect what files a commit touches:

- `git show --name-only <upstream_commit_sha>`

Port a single commit (recommended default: keep origin trace via `-x`):

- `git cherry-pick -x <upstream_commit_sha>`

Port a contiguous commit range (feature series / bugfix series):

- `git cherry-pick -x <from_sha>^..<to_sha>`

If you expect conflicts, or you want to "adapt upstream changes to our architecture" and keep a clean history:

- `git cherry-pick --no-commit <sha1> <sha2> <sha3>`
- Resolve conflicts / adapt changes for our codebase
- Optionally drop unwanted files (example: do NOT override our local-first README policy):
  - `git restore --source=HEAD -- README.md README.en.md`
- `git commit -m "port: adopt upstream <topic> improvements"`

Advantages vs merge:
- You only bring in changes you explicitly want.
- Conflicts are smaller and more controllable.
- You can avoid dragging unrelated upstream changes (for example, upstream doc/translation churn).

Tradeoff:
- You must choose which commits are worth porting.

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
2. `docker compose -p x-claw down`
3. `docker compose -p x-claw --profile gateway up -d --build`
4. `docker compose -p x-claw ps`
5. `curl -sS http://127.0.0.1:18790/health`

Expected healthy state:
- container: `x-claw-gateway`
- compose status: `Up ... (healthy)`
- health response contains `"status":"ok"`

### Useful checks
- `docker logs --tail 100 x-claw-gateway`
- `docker ps --filter name=x-claw-gateway`

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

- `curl -sS -X POST http://127.0.0.1:18790/api/notify -H 'Content-Type: application/json' -d '{"content":"✅ X-Claw: change complete (redeployed)."}'`
