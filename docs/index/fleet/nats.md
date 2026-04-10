---
room: fleet/nats
subdomain: fleet
source_paths: fleet_nats, fleet_nats_auth
architectural_health: normal
security_tier: normal
see_also:
  - broker/core.md
  - fleet/state.md
---

# fleet_nats.go

DOES: NATS pub/sub client for ephemeral fleet events. Subscribes to subjects like `fleet.peer.heartbeat`, `fleet.security.process`, `fleet.gridwatch.*`. Publishes from the broker side when peers register or unregister.

SYMBOLS:
- ConnectNATS(url, token) → (*nats.Conn, error)
- SubscribeFleetEvents(conn, callback)
- PublishPeerEvent(conn, event, payload)

PATTERNS:
- **Ephemeral pub/sub for cross-machine events** — events that don't need to be persisted (heartbeats, presence, security alerts) flow over NATS. Persistent state (peer list, message queues) flows over the broker HTTP API.

# fleet_nats_auth.go

DOES: NATS NKey-based authentication. Each fleet machine has its own NATS NKey pair so the NATS server can distinguish peers.

USE WHEN: Adding a new fleet machine to the NATS mesh, rotating NATS credentials, debugging NATS auth failures.
