#!/bin/sh
set -e

CONFIG_PATH="${X_CLAW_CONFIG_PATH:-${HOME}/.x-claw/config.json}"

setup_git_from_config() {
    # tools.git in config.json is optional; unknown fields are ignored by x-claw itself.
    # This hook only prepares git credentials/user identity for shell-based git push/commit.
    [ -f "${CONFIG_PATH}" ] || return 0
    command -v git >/dev/null 2>&1 || return 0
    command -v python3 >/dev/null 2>&1 || return 0

    python3 - "${CONFIG_PATH}" <<'PY'
import json
import os
import subprocess
import sys
import urllib.parse

path = sys.argv[1]
try:
    with open(path, "r", encoding="utf-8") as f:
        data = json.load(f)
except Exception:
    sys.exit(0)

tools = data.get("tools") or {}
git_cfg = tools.get("git") or {}
if not isinstance(git_cfg, dict):
    sys.exit(0)
if git_cfg.get("enabled") is False:
    sys.exit(0)

def get_str(key: str, default: str = "") -> str:
    v = git_cfg.get(key, default)
    return v if isinstance(v, str) else default

username = get_str("username").strip()
pat = get_str("pat").strip()
user_name = get_str("user_name").strip()
user_email = get_str("user_email").strip()
host = get_str("host", "github.com").strip() or "github.com"
protocol = get_str("protocol", "https").strip() or "https"

devnull = subprocess.DEVNULL

if user_name:
    subprocess.run(["git", "config", "--global", "user.name", user_name], stdout=devnull, stderr=devnull, check=False)
if user_email:
    subprocess.run(["git", "config", "--global", "user.email", user_email], stdout=devnull, stderr=devnull, check=False)

if username and pat:
    home = os.path.expanduser("~")
    cred_file = os.path.join(home, ".git-credentials")
    # URL-encode to avoid malformed credential URLs when token contains special chars.
    url = f"{protocol}://{urllib.parse.quote(username, safe='')}:{urllib.parse.quote(pat, safe='')}@{host}"
    with open(cred_file, "w", encoding="utf-8") as f:
        f.write(url + "\n")
    os.chmod(cred_file, 0o600)
    subprocess.run(
        ["git", "config", "--global", "credential.helper", f"store --file={cred_file}"],
        stdout=devnull,
        stderr=devnull,
        check=False,
    )
    # Keep host-level matching so one GitHub PAT can be reused across repos.
    subprocess.run(["git", "config", "--global", "credential.useHttpPath", "false"], stdout=devnull, stderr=devnull, check=False)
PY
}

# First-run: neither config nor workspace exists.
# If config.json is already mounted but workspace is missing we skip onboard to
# avoid the interactive "Overwrite? (y/n)" prompt hanging in a non-TTY container.
if [ ! -d "${HOME}/.x-claw/workspace" ] && [ ! -f "${CONFIG_PATH}" ]; then
    x-claw onboard
    echo ""
    echo "First-run setup complete."
    echo "Edit ${HOME}/.x-claw/config.json (add your API key, etc.) then restart the container."
    exit 0
fi

setup_git_from_config

# Keep `docker compose run ... x-claw-agent -m "hello"` compatible:
# when only flags are passed, default to `agent` subcommand.
if [ "$#" -eq 0 ]; then
    set -- gateway
fi
case "$1" in
    -*)
        set -- agent "$@"
        ;;
esac

exec x-claw "$@"
