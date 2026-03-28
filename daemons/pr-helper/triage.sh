#!/bin/bash
# PR Helper: skip if our machine is quarantined (don't push from compromised host).
health=$(curl -sf -H "Authorization: Bearer $(cat ~/.config/claude-peers/token.jwt 2>/dev/null)" http://100.109.211.128:7899/machine-health 2>/dev/null)
our_status=$(echo "$health" | python3 -c 'import sys,json; d=json.load(sys.stdin); h=d.get("ubuntu-homelab",{}); print(h.get("status","healthy"))' 2>/dev/null)
if [ "$our_status" = "quarantined" ]; then
    echo "SKIP: ubuntu-homelab is quarantined, refusing to push code"
    exit 1
fi
# Check for open PRs across orgs
for org in human-frontier-lab williavs WillyV3; do
    repos=$(gh repo list "$org" --no-archived --json name -q '.[].name' --limit 100 2>/dev/null)
    for repo in $repos; do
        echo "$repo" | grep -qi dotfiles && continue
        count=$(gh pr list --repo "$org/$repo" --state open --json number -q 'length' 2>/dev/null)
        [ "${count:-0}" -gt 0 ] && echo "$org/$repo has $count open PRs" && exit 0
    done
done
exit 1
