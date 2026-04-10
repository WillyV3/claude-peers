---
subdomain: fleet
source_paths: fleet_dream.go, fleet_git.go, fleet_mcp.go, fleet_nats.go, fleet_nats_auth.go, fleet_server.go
---

# fleet — cross-machine coordination

Source paths: fleet_dream, fleet_git, fleet_mcp, fleet_nats, fleet_nats_auth, fleet_server

## TASK → LOAD

| Task | Load |
|------|------|
| Snapshot fleet state to memory | state.md |
| Subscribe / publish NATS events | nats.md |
| Wire git commit hooks into fleet broadcasts | git.md |
| Run the broker as an MCP stdio server | mcp.md |

## Rooms

| Room | Covers | Files |
|------|--------|-------|
| state.md | fleet_dream, fleet_server | 2 |
| nats.md | fleet_nats, fleet_nats_auth | 2 |
| git.md | fleet_git | 1 |
| mcp.md | fleet_mcp | 1 |
