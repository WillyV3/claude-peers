#!/bin/bash
# Sync Janitor: skip if quarantined, otherwise check for conflicts.
health=$(curl -sf -H "Authorization: Bearer $(cat ~/.config/claude-peers/token.jwt 2>/dev/null)" http://100.109.211.128:7899/machine-health 2>/dev/null)
our_status=$(echo "$health" | python3 -c 'import sys,json; d=json.load(sys.stdin); h=d.get("ubuntu-homelab",{}); print(h.get("status","healthy"))' 2>/dev/null)
if [ "$our_status" = "quarantined" ]; then
    echo "SKIP: quarantined"
    exit 1
fi
count=$(find ~/projects ~/hfl-projects -name "*.sync-conflict-*" 2>/dev/null | wc -l)
[ "$count" -gt 0 ] && echo "$count conflicts" && exit 0
exit 1
