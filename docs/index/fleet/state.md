---
room: fleet/state
subdomain: fleet
source_paths: fleet_dream, fleet_server
architectural_health: normal
security_tier: normal
see_also:
  - broker/core.md
  - fleet/nats.md
hot_paths:
  - fleet_dream.go
---

# fleet_dream.go

DOES: Snapshots fleet state (current peer list, machine summaries, recent activity) into the operator's Claude Code memory directory at intervals. The "dream" command captures a moment in time so future Claude sessions on any fleet machine can read the snapshot and have an up-to-date picture of who is online and what they're working on.

SYMBOLS:
- DreamCommand() → snapshot to ~/.claude/projects/<project>/memory/fleet-activity.md
- DreamWatchCommand() → continuous dreams via NATS subscription, debounced

USE WHEN: Operator wants to refresh a Claude session's memory of what the fleet is currently doing. Typically run via cron or `claude-peers dream-watch` daemon.

PATTERNS:
- **Memory file as cross-session state** — by writing to ~/.claude/projects/.../memory/, the snapshot is auto-loaded by every new Claude Code session in that project. Cheap, no MCP needed.

# fleet_server.go

DOES: HTTP server bootstrap for the fleet endpoints (the non-broker side of claude-peers). Wires routes for memory read/write, dream snapshots, and the events stream.
