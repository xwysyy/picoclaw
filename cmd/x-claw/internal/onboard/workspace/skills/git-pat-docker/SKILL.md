---
name: git-pat-docker
description: Configure and troubleshoot Git HTTPS PAT auth inside X-Claw Docker containers (`gateway`/`agent`), including `tools.git`, credential helper checks, clone/push workflow, and safety-guard workarounds.
---

# Git PAT in Docker

Use this skill when:
- You need `git clone/pull/push` from inside X-Claw containers.
- `tools.git` is configured but Git still asks for username/password.
- Agent-side shell commands are blocked by safety guard (`path outside working dir`).

## Source Of Truth

- Runtime config file inside container: `/home/xclaw/.x-claw/config.json`
- Host-mounted source in this repo: `config/config.json`
- Required section:

```json
{
  "tools": {
    "git": {
      "enabled": true,
      "username": "your-github-username",
      "pat": "github_pat_xxx",
      "user_name": "Your Name",
      "user_email": "you@example.com",
      "host": "github.com",
      "protocol": "https"
    }
  }
}
```

## Apply Config

Rebuild/restart `gateway` so entrypoint rewrites Git credentials:

```bash
source ~/.zshrc && proxy_on && \
docker compose -p x-claw -f docker/docker-compose.yml --profile gateway up -d --build
```

For one-shot `agent` runs, also build the agent image:

```bash
source ~/.zshrc && proxy_on && \
docker compose -p x-claw -f docker/docker-compose.yml --profile agent build x-claw-agent
```

## Verify Credentials

Check helper and identity:

```bash
docker exec x-claw-gateway sh -lc '
git config --global --get credential.helper
git config --global --get credential.useHttpPath
git config --global --get user.name
git config --global --get user.email
'
```

Check resolved credential (safe output, masked password):

```bash
docker exec x-claw-gateway sh -lc '
printf "protocol=https\nhost=github.com\n\n" | git credential fill |
  sed -E "s#(password=).+#\1***#"
'
```

Expected:
- `credential.helper` contains `store --file=/home/xclaw/.git-credentials`
- `git credential fill` returns `username=` and `password=***`

## Clone / Push Workflow

Run git operations in container workspace (not via restricted agent shell):

```bash
source ~/.zshrc && proxy_on && \
docker exec x-claw-gateway sh -lc '
cd "$HOME/.x-claw/workspace" &&
git clone https://github.com/<owner>/<repo>.git
'
```

Push example:

```bash
docker exec x-claw-gateway sh -lc '
cd "$HOME/.x-claw/workspace/<repo>" &&
git add -A &&
git commit -m "update" &&
git push origin HEAD
'
```

## SSH Remote Pitfall

PAT does not work with `git@github.com:...` remotes.

Check and convert:

```bash
docker exec x-claw-gateway sh -lc '
cd "$HOME/.x-claw/workspace/<repo>" &&
git remote -v &&
git remote set-url origin https://github.com/<owner>/<repo>.git
'
```

## Common Failures

`path outside working dir`
- Cause: agent shell sandbox restriction.
- Fix: run with `docker exec ... sh -lc '...'` in container.

`could not read Username for https://github.com/...`
- Usually credential match failure.
- Verify with `git credential fill`.
- Ensure `credential.useHttpPath` is `false` for host-level PAT reuse.

`403` on push
- PAT scopes missing (`repo` for private repos; appropriate org permissions).
- Confirm target repo and account match `username` + PAT owner.
