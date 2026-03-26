#!/usr/bin/env bash
# Deploy claude-peers binary to all fleet machines.
# Kills running MCP servers first, deploys, verifies.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"

declare -A HOSTS=(
  [omarchy]="local"
  [ubuntu-homelab]="willy@100.109.211.128"
  [raspdeck]="willy@raspdeck"
  [macbook1]="williamvansickleiii@100.67.104.73"
  [thinkbook]="willy@100.119.90.27"
)

declare -A BINARIES=(
  [omarchy]="$DIR/claude-peers"
  [ubuntu-homelab]="$DIR/claude-peers-linux-amd64"
  [raspdeck]="$DIR/claude-peers-linux-arm64"
  [macbook1]="$DIR/claude-peers-darwin-arm64"
  [thinkbook]="$DIR/claude-peers-linux-amd64"
)

declare -A DEST=(
  [omarchy]="$HOME/.local/bin/claude-peers"
  [ubuntu-homelab]="~/.local/bin/claude-peers"
  [raspdeck]="~/.local/bin/claude-peers"
  [macbook1]="~/.local/bin/claude-peers"
  [thinkbook]="~/.local/bin/claude-peers"
)

for machine in "${!HOSTS[@]}"; do
  host="${HOSTS[$machine]}"
  bin="${BINARIES[$machine]}"
  dest="${DEST[$machine]}"

  echo -n "[$machine] "

  if [ "$host" = "local" ]; then
    pkill -f "claude-peers server" 2>/dev/null || true
    sleep 0.5
    cp "$bin" "$dest"
    chmod +x "$dest"
    echo "deployed (local)"
  else
    ssh -o ConnectTimeout=5 -o BatchMode=yes "$host" "pkill -f 'claude-peers server' 2>/dev/null; pkill -f 'claude-peers broker' 2>/dev/null; true" 2>/dev/null
    sleep 0.5
    scp -q "$bin" "$host:$dest" 2>/dev/null && echo "deployed" || echo "FAILED"
    ssh -o ConnectTimeout=5 -o BatchMode=yes "$host" "chmod +x $dest" 2>/dev/null
  fi
done

# Restart broker
echo ""
echo "Restarting broker on ubuntu-homelab..."
ssh -o ConnectTimeout=5 -o BatchMode=yes willy@100.109.211.128 "systemctl --user restart claude-peers-broker" 2>/dev/null
sleep 2

# Verify
echo ""
echo "Verifying..."
claude-peers status
