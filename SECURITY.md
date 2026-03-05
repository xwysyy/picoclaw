# Security (Trust Model + Defaults + Break‑Glass)

X-Claw is designed as a **personal, single-operator assistant** that runs locally (or on a private host) and can call tools with real side effects (file I/O, process execution, network access, MCP servers, etc.). This document explains:

- What X-Claw **does and does not** try to protect against
- The **trust boundaries** (what is in the TCB)
- The **default-safe posture** and how to open things up (break‑glass)

If you are deploying X-Claw in a shared / multi-tenant environment, read this first and consider whether your threat model is compatible.

---

## 1) Threat model (what we assume)

### In-scope (things we try to be safe against by default)

- **Accidental damage** from tool calls (writing files, running commands)
- **Over-broad filesystem access** (by default tools are constrained to the workspace)
- **Unintentional secret exposure** in logs/traces (best-effort redaction options exist)
- **Prompt-injection pressure** from untrusted inputs (web pages, user-provided text) by keeping policies explicit and configurable
- **Operational safety** (timeouts, caps, and kill-switches to prevent runaway loops)

### Out-of-scope (by design, not guaranteed)

- Strong isolation against a **malicious operator** (the person controlling config / skills / MCP servers)
- Hardened sandboxing for **multi-tenant** workloads (X-Claw is not a hosted platform)
- Perfect defense against **prompt injection** (LLMs can be socially engineered; policies help but do not eliminate the risk)
- OS/kernel escapes (if you run untrusted code via `exec`, that is your responsibility to sandbox at the OS/container layer)

---

## 2) Trust boundaries (TCB)

The following components are effectively **trusted** and should be treated as part of the Trusted Computing Base:

- **Configuration** (`config.json`, env vars): controls tool availability and security posture
- **Skills** (downloaded and executed code): can expand tool surface and prompt contents
- **MCP servers**: external tool providers; treat them like remote code execution interfaces
- **Exec backend**:
  - `tools.exec.backend=host` means tool calls can run on the host (higher risk)
  - Docker sandbox can reduce risk, but it is still not a perfect isolation boundary
- **Workspace**: anything readable/writable there is potentially accessible by the agent

User messages, web content, and external channel messages should be considered **untrusted inputs**.

---

## 3) Default posture (what we ship with)

X-Claw defaults are intentionally conservative for a personal assistant:

- Gateway binds to **localhost** by default (`127.0.0.1`)
- Agent filesystem tools default to **workspace-only** access
- Plan Mode is enabled by default to reduce accidental side effects during planning phases
- Tool trace / audit outputs are written under the workspace `.x-claw/` directory when enabled

Note: “default-safe” here means **reasonable for a single operator**. You should still enable additional guardrails for production or remote deployments.

---

## 4) Break‑glass configuration (how to open things up safely)

Use these knobs to explicitly widen capabilities:

### Filesystem access

- Keep `agents.defaults.restrict_to_workspace=true` unless you have a strong reason.
- Use `tools.allow_read_paths` / `tools.allow_write_paths` to *explicitly* allow additional paths.

### Side-effect tools and confirmations

- Enable the centralized tool policy layer (`tools.policy.enabled=true`) and configure:
  - allow/deny lists
  - timeouts
  - confirmation gating for side-effect tools (`exec`, `write_file`, `edit_file`, `append_file`, and `mcp_*`)

### Emergency stop (ESTOP)

- Enable ESTOP to globally block tools quickly during incidents.
- Prefer “fail-closed” behavior for higher-risk deployments so a broken state file does not accidentally allow tool execution.

### Exec sandbox backend

- Prefer Docker sandbox execution for untrusted commands.
- Keep container networking disabled unless explicitly required.

---

## 5) Operational recommendations

- Do not put long-lived secrets in prompts or files that the agent can read.
- Treat web content as hostile; prefer evidence-based outputs and keep tool policies strict.
- Keep traces/audits on local disk with restricted permissions.
- Regularly rotate audit logs and clean up old artifacts to avoid leaking sensitive data and to control disk usage.

---

## 6) Vulnerability reporting

If this repository is used privately, report issues to the maintainer through your normal private channel.
If it is public, prefer private disclosure before filing public issues.

