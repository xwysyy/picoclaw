#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
PicoClaw one-click installer (pico-scale).

Default behavior:
  - If running inside a PicoClaw repo: build from source
  - Otherwise: attempt to download a GitHub release asset

Usage:
  ./scripts/install.sh [options]

Options:
  --prefix <dir>         Install prefix (default: ~/.local)
  --repo <owner/name>    GitHub repo for release downloads (default: sipeed/picoclaw)
  --version <tag|latest> Release tag (or "latest") (default: latest)
  --from-source          Build from local source tree
  --from-release         Download release asset from GitHub
  --no-console           Do not install Console static assets
  --systemd-user         Install systemd *user* service (Linux)
  --launchd              Install launchd agent (macOS)
  -h, --help             Show this help

Notes:
  - Runtime config is read from config.json (default: ~/.picoclaw/config.json).
  - Gateway Console UI uses prebuilt static assets when present:
      ~/.picoclaw/console/
      ~/.local/share/picoclaw/console/
      /usr/local/share/picoclaw/console/
USAGE
}

log() { printf '[install] %s\n' "$*"; }
warn() { printf '[install] WARN: %s\n' "$*" >&2; }
die() { printf '[install] ERROR: %s\n' "$*" >&2; exit 1; }

prefix="${PREFIX:-$HOME/.local}"
repo="sipeed/picoclaw"
version="latest"
from=""
install_console=true
install_systemd_user=false
install_launchd=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix) prefix="${2-}"; shift 2 ;;
    --repo) repo="${2-}"; shift 2 ;;
    --version) version="${2-}"; shift 2 ;;
    --from-source) from="source"; shift ;;
    --from-release) from="release"; shift ;;
    --no-console) install_console=false; shift ;;
    --systemd-user) install_systemd_user=true; shift ;;
    --launchd) install_launchd=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "Unknown option: $1" ;;
  esac
done

prefix="$(cd "${prefix/#\~/$HOME}" 2>/dev/null && pwd -P || echo "$prefix")"
[[ -n "${prefix:-}" ]] || die "--prefix is required"

bin_dir="$prefix/bin"
share_dir="$prefix/share/picoclaw"
console_dir="$share_dir/console"

cfg_dir="${PICOCLAW_HOME:-$HOME/.picoclaw}"
cfg_dir="$(cd "${cfg_dir/#\~/$HOME}" 2>/dev/null && pwd -P || echo "$cfg_dir")"
cfg_path="$cfg_dir/config.json"

in_repo=false
if [[ -f "go.mod" && -d "cmd/picoclaw" ]]; then
  in_repo=true
fi

if [[ -z "$from" ]]; then
  from=$([[ "$in_repo" == true ]] && echo "source" || echo "release")
fi

mkdir -p "$bin_dir" "$share_dir" "$cfg_dir"

tmp="$(mktemp -d)"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

detect_os() {
  local u
  u="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$u" in
    linux*) echo "linux" ;;
    darwin*) echo "darwin" ;;
    *) die "Unsupported OS: $(uname -s)" ;;
  esac
}

detect_arch() {
  local m
  m="$(uname -m | tr '[:upper:]' '[:lower:]')"
  case "$m" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    riscv64) echo "riscv64" ;;
    *) die "Unsupported arch: $(uname -m)" ;;
  esac
}

build_from_source() {
  command -v go >/dev/null 2>&1 || die "go is required for --from-source"
  log "Building PicoClaw from source..."
  local out="$tmp/picoclaw"
  go build -trimpath -o "$out" ./cmd/picoclaw
  install -m 0755 "$out" "$bin_dir/picoclaw"
  log "Installed binary: $bin_dir/picoclaw"
}

github_api_url() {
  local r="$1" v="$2"
  if [[ "$v" == "latest" ]]; then
    printf 'https://api.github.com/repos/%s/releases/latest' "$r"
  else
    printf 'https://api.github.com/repos/%s/releases/tags/%s' "$r" "$v"
  fi
}

pick_release_asset_url() {
  local json="$1" os="$2" arch="$3"
  # Best-effort parsing without jq. Expect "browser_download_url": "..."
  # Try common patterns first.
  local patterns=(
    "picoclaw.*${os}.*${arch}.*\\.tar\\.gz"
    "picoclaw.*${os}.*${arch}.*\\.tgz"
    "picoclaw.*${os}.*${arch}.*\\.zip"
  )
  local p url
  for p in "${patterns[@]}"; do
    url="$(printf '%s\n' "$json" | grep -Eo '\"browser_download_url\"[[:space:]]*:[[:space:]]*\"[^\"]+\"' | sed -E 's/.*\"([^\"]+)\".*/\\1/' | grep -E "$p" | head -n 1 || true)"
    if [[ -n "$url" ]]; then
      printf '%s' "$url"
      return 0
    fi
  done
  return 1
}

download_from_release() {
  command -v curl >/dev/null 2>&1 || die "curl is required for --from-release"
  local os arch api json url asset
  os="$(detect_os)"
  arch="$(detect_arch)"
  api="$(github_api_url "$repo" "$version")"
  log "Resolving release asset from $repo ($version) for $os/$arch..."
  json="$(curl -fsSL "$api")" || die "Failed to fetch GitHub release metadata: $api"
  url="$(pick_release_asset_url "$json" "$os" "$arch")" || die "No matching release asset found for $os/$arch (repo=$repo, version=$version)"

  asset="$tmp/asset"
  log "Downloading: $url"
  curl -fsSL "$url" -o "$asset"

  local extract="$tmp/extract"
  mkdir -p "$extract"

  if [[ "$url" == *.zip ]]; then
    command -v unzip >/dev/null 2>&1 || die "unzip is required to extract: $url"
    unzip -q "$asset" -d "$extract"
  else
    tar -xzf "$asset" -C "$extract"
  fi

  # Expect a binary named "picoclaw" somewhere inside.
  local bin
  bin="$(find "$extract" -maxdepth 3 -type f -name picoclaw -perm -u+x 2>/dev/null | head -n 1 || true)"
  [[ -n "$bin" ]] || die "Release asset did not contain an executable 'picoclaw' binary"
  install -m 0755 "$bin" "$bin_dir/picoclaw"
  log "Installed binary: $bin_dir/picoclaw"

  # Optional console static assets: look for "console/" directory.
  if [[ "$install_console" == true ]]; then
    local cdir
    cdir="$(find "$extract" -maxdepth 4 -type d -name console 2>/dev/null | head -n 1 || true)"
    if [[ -n "$cdir" ]]; then
      rm -rf "$console_dir"
      mkdir -p "$console_dir"
      cp -R "$cdir"/. "$console_dir"/
      log "Installed Console static assets: $console_dir"
    else
      warn "No console/ directory found in release asset; Console UI will fall back to the built-in HTML."
    fi
  fi
}

install_console_from_repo() {
  if [[ "$install_console" != true ]]; then
    return 0
  fi
  if [[ -d "web/picoclaw-console/out" ]]; then
    rm -rf "$console_dir"
    mkdir -p "$console_dir"
    cp -R "web/picoclaw-console/out"/. "$console_dir"/
    log "Installed Console static assets from repo: $console_dir"
  else
    warn "Console static assets not found at web/picoclaw-console/out; skip (use Docker build or next export to generate)."
  fi
}

init_workspace() {
  if [[ -f "$cfg_path" ]]; then
    log "Config already exists: $cfg_path (skip onboard)"
    return 0
  fi
  log "Initializing config + workspace via onboard..."
  "$bin_dir/picoclaw" onboard
  log "Config created: $cfg_path"
}

install_systemd_user_service() {
  [[ "$(detect_os)" == "linux" ]] || die "--systemd-user is only supported on Linux"
  local unit_dir="$HOME/.config/systemd/user"
  local unit="$unit_dir/picoclaw-gateway.service"
  mkdir -p "$unit_dir"

  cat >"$unit" <<EOF
[Unit]
Description=PicoClaw Gateway
After=network-online.target

[Service]
Type=simple
ExecStart=$bin_dir/picoclaw gateway
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
EOF

  log "Installed systemd user unit: $unit"
  cat <<'NEXT'
Next:
  systemctl --user daemon-reload
  systemctl --user enable --now picoclaw-gateway.service
  journalctl --user -u picoclaw-gateway.service -f
NEXT
}

install_launchd_agent() {
  [[ "$(detect_os)" == "darwin" ]] || die "--launchd is only supported on macOS"
  local plist_dir="$HOME/Library/LaunchAgents"
  local plist="$plist_dir/io.picoclaw.gateway.plist"
  mkdir -p "$plist_dir"

  cat >"$plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key><string>io.picoclaw.gateway</string>
    <key>ProgramArguments</key>
    <array>
      <string>$bin_dir/picoclaw</string>
      <string>gateway</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>StandardOutPath</key><string>$cfg_dir/gateway.out.log</string>
    <key>StandardErrorPath</key><string>$cfg_dir/gateway.err.log</string>
  </dict>
</plist>
EOF

  log "Installed launchd plist: $plist"
  cat <<NEXT
Next:
  launchctl unload "$plist" 2>/dev/null || true
  launchctl load "$plist"
  tail -f "$cfg_dir/gateway.out.log"
NEXT
}

log "Install prefix: $prefix"
log "Config path:    $cfg_path"

case "$from" in
  source)
    [[ "$in_repo" == true ]] || die "--from-source requires running inside the PicoClaw repo"
    build_from_source
    install_console_from_repo
    ;;
  release)
    download_from_release
    ;;
  *)
    die "Invalid install mode: $from"
    ;;
esac

init_workspace

if [[ "$install_systemd_user" == true ]]; then
  install_systemd_user_service
fi

if [[ "$install_launchd" == true ]]; then
  install_launchd_agent
fi

cat <<EOF

Done.
  - binary: $bin_dir/picoclaw
  - config: $cfg_path

Tip:
  Add "$bin_dir" to PATH, then run:
    picoclaw gateway
    picoclaw status --json
EOF

