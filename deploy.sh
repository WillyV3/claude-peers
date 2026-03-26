#!/usr/bin/env bash
# Deploy claude-peers to fleet machines.
# Configure hosts in deploy.conf (one per line: name:user@host:binary)
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
CONF="$DIR/deploy.conf"

if [ ! -f "$CONF" ]; then
  cat <<'EOF'
No deploy.conf found. Create one with your fleet hosts:

  # deploy.conf — one line per machine
  # format: name:ssh_target:binary_name
  # binary_name is one of: claude-peers, claude-peers-linux-amd64,
  #   claude-peers-linux-arm64, claude-peers-darwin-arm64
  ubuntu-homelab:willy@homelab-ip:claude-peers-linux-amd64
  raspdeck:willy@raspdeck-ip:claude-peers-linux-arm64
  macbook1:user@macbook-ip:claude-peers-darwin-arm64
  thinkbook:willy@thinkbook-ip:claude-peers-linux-amd64

Broker host (for restart after deploy):
  # Set BROKER_SSH to the broker's ssh target
  BROKER_SSH=willy@homelab-ip
EOF
  exit 1
fi

BROKER_SSH=""

while IFS= read -r line; do
  # Skip comments and empty lines
  [[ "$line" =~ ^[[:space:]]*# ]] && continue
  [[ -z "${line// }" ]] && continue

  # Handle BROKER_SSH directive
  if [[ "$line" =~ ^BROKER_SSH= ]]; then
    BROKER_SSH="${line#BROKER_SSH=}"
    continue
  fi

  IFS=: read -r name host bin <<< "$line"
  echo -n "[$name] "

  if [ ! -f "$DIR/$bin" ]; then
    echo "SKIP (binary $bin not found, run go build first)"
    continue
  fi

  ssh -o ConnectTimeout=5 -o BatchMode=yes "$host" \
    "pkill -f 'claude-peers server' 2>/dev/null; pkill -f 'claude-peers broker' 2>/dev/null; true" 2>/dev/null
  sleep 0.3
  scp -q "$DIR/$bin" "$host:~/.local/bin/claude-peers" 2>/dev/null && echo "ok" || echo "FAIL"
done < "$CONF"

if [ -n "$BROKER_SSH" ]; then
  echo ""
  echo "Restarting broker..."
  ssh -o ConnectTimeout=5 -o BatchMode=yes "$BROKER_SSH" \
    "systemctl --user restart claude-peers-broker 2>/dev/null" || true
  sleep 2
  claude-peers status 2>/dev/null || echo "(run 'claude-peers status' to verify)"
fi
