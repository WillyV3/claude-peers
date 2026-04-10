---
room: fleet/git
subdomain: fleet
source_paths: fleet_git
architectural_health: normal
security_tier: normal
see_also:
  - fleet/nats.md
---

# fleet_git.go

DOES: Git event integration. When a peer's git hook fires (e.g., post-commit), it can publish the commit summary to the fleet so other peers see "machine X committed this, here's the summary." Used by the `peers-broadcast-commit` hook in ~/.claude/hooks/.

SYMBOLS:
- BroadcastCommit(repo, hash, msg) → publishes to fleet.git.commit
- ParseRepoMeta(path) → (repo name, branch, last commit)

USE WHEN: Wiring a new git hook into the fleet broadcast, or debugging missing commit notifications.
