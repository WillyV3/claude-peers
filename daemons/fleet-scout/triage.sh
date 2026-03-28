#!/bin/bash
# Fleet Scout: always run, but escalate if security events detected.
health=$(curl -sf -H "Authorization: Bearer $(cat ~/.config/claude-peers/token.jwt 2>/dev/null)" http://100.109.211.128:7899/machine-health 2>/dev/null)
degraded=$(echo "$health" | python3 -c 'import sys,json; d=json.load(sys.stdin); print(sum(1 for v in d.values() if v["status"]!="healthy"))' 2>/dev/null)
if [ "${degraded:-0}" -gt 0 ]; then
    echo "URGENT: $degraded machines unhealthy"
else
    echo "scheduled check"
fi
exit 0
