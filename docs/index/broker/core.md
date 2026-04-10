---
room: broker/core
subdomain: broker
source_paths: broker_core, main, types
architectural_health: warning
security_tier: normal
see_also:
  - auth/ucan.md
  - auth/middleware.md
  - fleet/state.md
hot_paths:
  - broker_core.go
  - main.go
committee_notes: "Single-threaded broker with mutex on _state. Long-running handlers can stall the fleet. Caretaker's heartbeat presence check (2026-04-09) was added here to catch silent eviction."
---

# broker_core.go

DOES: The HTTP server that backs the broker. Routes /register, /unregister, /heartbeat, /list, /msg/send, /msg/poll, /msg/ack, /events/read, /memory/read, /memory/write. Maintains the in-memory peer registry, message queues per agent, and the broadcast subscriber list. Mutex-protected mutation with a fanout broadcast queue for SSE-style push delivery.

SYMBOLS:
- handleRegister(w, r) → agent name claim, session id allocation, summary set
- handleHeartbeat(w, r) → updates last_seen, returns 200 if peer is in registry
- handleListPeers(w, r) → returns current peer list with last_seen timestamps
- handleMsgSend(w, r) → atomic enqueue to recipient's queue + broadcast notification
- handleMsgPoll(w, r) → drains queue for the requesting agent
- registerPeer(name, session, machine, cwd) → unique-name claim with session-isolation rules
- evictStale() → background job that removes peers whose last_seen exceeds stale_timeout

ROUTES:
- POST /register
- POST /unregister
- POST /heartbeat
- GET  /list
- POST /msg/send
- GET  /msg/poll?agent=<name>
- POST /msg/ack
- GET  /events/read
- GET  /memory/read?key=<k>
- POST /memory/write

PATTERNS:
- **Atomic registry mutation under mutex** — every state change holds the broker mutex briefly, releases before writing to subscriber queues
- **Stale eviction with grace period** — peers missing heartbeats for stale_timeout get evicted but the broker doesn't immediately tell them; they discover via list-peers presence check (caretaker's pattern in willybrain.sh)
- **Session-isolated agent claims** — agent names are unique per session id; a session can only claim one name; names are released on unregister or stale eviction

# main.go

DOES: Entry point. Parses CLI args, dispatches to subcommands (broker, server, status, peers, send, init, issue-token, save-token, refresh-token, mint-root, dream, dream-watch, generate-nkey, kill-broker, reauth-fleet). Loads config from $HOME/.config/claude-peers/config.json.

SYMBOLS:
- main() → CLI dispatcher
- subcommand handlers for each verb listed in `claude-peers --help`

# types.go

DOES: Shared types for the broker + CLI. Peer struct (Name, Machine, Session, CWD, LastSeen, Summary), Message struct (From, To, Body, Ts), config types.

USE WHEN: Adding a new field to the peer registry, modifying the message envelope, or extending the broker's request schema.
