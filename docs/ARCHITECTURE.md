# Architecture

claude-peers is a peer discovery and messaging system for Claude Code instances. It uses a broker-client model with cryptographic authentication.

## Components

### Broker (`broker_core.go`)
Central HTTP server backed by SQLite. Handles peer registration, message routing, event logging, and fleet memory storage. Rate-limited with UCAN capability-based auth.

### MCP Server (`fleet_server.go`, `fleet_mcp.go`)
Runs as a Model Context Protocol (MCP) stdio server, started by Claude Code. Registers with the broker, polls for messages, and exposes tools (list_peers, send_message, set_summary, set_name, check_messages) to the LLM.

### UCAN Auth (`auth_ucan.go`, `auth_ucan_keys.go`, `auth_middleware.go`)
Ed25519 keypair-based authentication using UCAN (User Controlled Authorization Networks) tokens. Supports delegation chains: a root token can mint child tokens with attenuated capabilities. Tokens are JWTs signed with EdDSA.

### NATS Integration (`fleet_nats.go`, `fleet_nats_auth.go`)
Optional NATS JetStream integration for real-time event streaming. The broker dual-writes events to both SQLite and NATS. Supports NKey authentication for per-machine NATS auth. Falls back gracefully to HTTP polling when NATS is unavailable.

### Dream System (`fleet_dream.go`)
Snapshots fleet state (peers, events) into a markdown file stored in Claude's memory directory. Two modes: one-shot (`dream`) and continuous NATS-based watch (`dream-watch`).

### Git Integration (`fleet_git.go`)
Auto-detects git repository, branch, recent files, and generates peer names from repo context. Optionally generates LLM-powered summaries of what each session is working on.

## Data Flow

```
Claude Code -> MCP stdio -> fleet_server.go -> HTTP -> broker_core.go -> SQLite
                                                                      -> NATS (optional)
```

## Authentication Flow

1. Broker generates Ed25519 keypair and root token on `init broker`
2. Client generates its own keypair on `init client`
3. Broker issues delegated token to client's public key
4. All API requests carry Bearer token, validated by UCAN middleware
5. Tokens auto-refresh when nearing expiry
6. Broker identity verified via challenge-response on connect

## File Layout

| File | Purpose |
|------|---------|
| `main.go` | CLI entry point, subcommand dispatch |
| `broker_core.go` | Broker HTTP server and database |
| `fleet_server.go` | MCP server, tool handlers, message polling |
| `fleet_mcp.go` | MCP transport, tool schemas, JSON-RPC |
| `fleet_git.go` | Git integration, naming, LLM summaries |
| `fleet_dream.go` | Fleet memory snapshots |
| `fleet_nats.go` | NATS JetStream publisher/subscriber |
| `fleet_nats_auth.go` | NATS NKey authentication |
| `auth_ucan.go` | UCAN token minting, validation, capabilities |
| `auth_ucan_keys.go` | Ed25519 key management, token storage |
| `auth_middleware.go` | HTTP middleware for UCAN validation |
| `config.go` | Configuration loading, CLI init |
| `types.go` | Shared types and request/response structs |
| `helpers.go` | Utility functions (email, SSH) |
| `rate_limiter.go` | Sliding-window rate limiter |
