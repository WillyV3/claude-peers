---
generated: 2026-04-09
source_paths: auth_ucan.go, auth_ucan_keys.go, auth_middleware.go, broker_core.go, main.go, types.go, fleet_dream.go, fleet_git.go, fleet_mcp.go, fleet_nats.go, fleet_nats_auth.go, fleet_server.go, config.go, helpers.go, rate_limiter.go
index_version: 1
---

# LOI Index — claude-peers

**What this project is:** the broker + CLI for a multi-machine Claude Code peer network. Sessions across the fleet (omarchy, ubuntu-homelab, willyv4, raspdeck, etc.) register with a central broker, exchange messages, share fleet state, and coordinate via UCAN tokens. Voice devices, daemons, and interactive Claude Code sessions all join the same mesh and address each other by stable agent name.

**The question this project answers:** *"how do my fleet machines and the AI sessions running on them talk to each other safely without me wiring point-to-point?"*

## TASK → LOAD

| Task | Load |
|------|------|
| Understand broker request routing / handlers | broker/core.md |
| Issue or rotate a UCAN token | auth/ucan.md |
| Add a new auth-protected route | auth/middleware.md |
| Read or write fleet shared state | fleet/state.md |
| Subscribe to NATS for inter-peer events | fleet/nats.md |
| Wire git events into the fleet (commit broadcast) | fleet/git.md |
| Run the broker as an MCP stdio server | fleet/mcp.md |
| Read or change the config schema | infra/config.md |
| Add or tune a rate limit | infra/rate-limit.md |

## PATTERN → LOAD

| Pattern | Load |
|---------|------|
| UCAN delegation chain (root → admin → peer-session) | auth/ucan.md |
| Token rotation without service restart (caretaker's patch, 2026-04-09) | auth/ucan.md, fleet/state.md |
| Heartbeat + presence check for silent eviction recovery | broker/core.md |
| Atomic peer state mutation with broadcast queue | broker/core.md |
| NATS pub/sub for ephemeral events | fleet/nats.md |
| Per-route rate limiting (token bucket) | infra/rate-limit.md |

## GOVERNANCE WATCHLIST

| Room | Health | Security | Note |
|------|--------|----------|------|
| auth/ucan.md | normal | sensitive | UCAN signing logic. Any change here affects the entire fleet's trust model. Test against refresh_token_test.go before merging. |
| broker/core.md | warning | normal | Broker process is single-threaded with mutex on `_state`. Long-running handlers can stall the whole fleet. |

## Buildings

| Subdomain | Description | Rooms |
|-----------|-------------|-------|
| broker/ | The broker process: HTTP server, handlers, peer registry, message queue | core.md |
| auth/ | UCAN tokens, signing, middleware, key management | ucan.md, middleware.md |
| fleet/ | Cross-machine state, NATS pub/sub, git event hooks, MCP integration | state.md, nats.md, git.md, mcp.md |
| infra/ | Config loading, helpers, rate limiting | config.md, rate-limit.md |
