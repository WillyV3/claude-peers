# Daemon Architecture

## Vision

Daemons are persistent AI background processes that maintain infrastructure, codebases, and operations without human prompting. They build on top of claude-peers' existing broker, messaging, and event infrastructure.

> "Agents create work. Daemons maintain it." — Riley Tomasek

## What we already have

```
┌─────────────────────────────────────────────────────┐
│  LAYER 3: Gridwatch Dashboard (raspdeck kiosk)      │
│  - Fleet health monitoring (7 machines)              │
│  - Claude agent status per machine                   │
│  - LLM server metrics (sonia's macbook)              │
│  - Event feed                                        │
├─────────────────────────────────────────────────────┤
│  LAYER 2: Claude Peers (cross-machine messaging)     │
│  - MCP server per Claude instance                    │
│  - Channel notifications (live push)                 │
│  - Peer discovery, summaries, events API             │
│  - Claude wrapper with channel flag                  │
├─────────────────────────────────────────────────────┤
│  LAYER 1: Infrastructure                             │
│  - Broker on ubuntu-homelab (systemd, SQLite)        │
│  - Tailscale mesh (7 machines)                       │
│  - SSH keys distributed fleet-wide                   │
│  - deploy.sh for fleet-wide binary updates           │
│  - Hooks (commit broadcast, etc.)                    │
└─────────────────────────────────────────────────────┘
```

## What we're adding

```
┌─────────────────────────────────────────────────────┐
│  LAYER 4: Daemons                                    │
│  - Daemon definitions (config files in repo)         │
│  - Daemon supervisor (manages lifecycle)             │
│  - Event subscriptions (daemons watch for triggers)  │
│  - NATS pub/sub (phase 2, replaces HTTP polling)     │
└─────────────────────────────────────────────────────┘
```

## Daemon definition

Each daemon is a directory with two files:

```
daemons/
  pr-helper/
    daemon.json        # config: what to watch, where to run, constraints
    agent.md           # claude code agent instructions
  bug-triage/
    daemon.json
    agent.md
  librarian/
    daemon.json
    agent.md
```

### daemon.json

```json
{
  "name": "pr-helper",
  "description": "Keep PRs mergeable, descriptions accurate, conflicts resolved",
  "machine": "ubuntu-homelab",
  "schedule": {
    "type": "event",
    "watch": ["github.pr.opened", "github.pr.updated", "github.ci.failed"]
  },
  "constraints": {
    "max_concurrent": 1,
    "cooldown_seconds": 60,
    "allowed_repos": ["WillyV3/*", "Human-Frontier-Labs-Inc/*"],
    "deny_actions": ["git push --force", "git branch -D"]
  },
  "model": "sonnet",
  "timeout_minutes": 10
}
```

Alternative schedule types:

```json
// Cron-based (runs on schedule)
"schedule": { "type": "cron", "cron": "*/15 * * * *" }

// Continuous (always running, reacts to events)
"schedule": { "type": "continuous", "poll_seconds": 30 }

// Event-driven (wakes on specific broker events)
"schedule": { "type": "event", "watch": ["peer_joined", "fleet.commit"] }
```

### agent.md

Standard Claude Code agent markdown. The daemon supervisor passes it via `claude --agent daemons/pr-helper/agent.md`. The agent has access to all normal tools plus claude-peers MCP tools for fleet communication.

```markdown
# PR Helper Daemon

You are a PR maintenance daemon. Your job is to keep pull requests
mergeable and review-ready across the fleet's repositories.

## When triggered

1. Check for open PRs with merge conflicts → resolve them
2. Check for PRs with failing CI → diagnose and fix lint/format issues
3. Check for PRs with stale descriptions → update to match the diff
4. Report actions taken via claude-peers set_summary

## Constraints

- Never force push
- Never merge without human approval
- Never modify business logic, only fix lint/format/conflicts
- If unsure, send a peer message asking for guidance
```

## Daemon supervisor

New subcommand: `claude-peers daemon`

```
claude-peers daemon start <name>        Start a daemon
claude-peers daemon stop <name>         Stop a daemon
claude-peers daemon list                List all daemons and their status
claude-peers daemon logs <name>         Show daemon logs
claude-peers daemon reload              Reload daemon configs
```

The supervisor:

1. Reads daemon definitions from a configurable directory
2. Spawns Claude Code with `--agent` flag pointing to the agent.md
3. Passes events from the broker to the daemon via environment or stdin
4. Monitors health (restarts on crash, respects cooldown)
5. Reports daemon status to the broker (new peer type: "daemon")
6. Tracks runs in SQLite (start time, trigger, duration, outcome)

### Supervisor architecture

```
                        ┌─────────────────────┐
                        │  Daemon Supervisor   │
                        │  (claude-peers       │
                        │   daemon run-all)    │
                        └──────┬──────────────┘
                               │
              ┌────────────────┼────────────────┐
              │                │                │
    ┌─────────┴──────┐  ┌─────┴────────┐  ┌────┴──────────┐
    │  pr-helper      │  │  bug-triage  │  │  librarian    │
    │  claude --agent │  │  claude      │  │  claude       │
    │  (sonnet)       │  │  --agent     │  │  --agent      │
    └────────┬───────┘  └──────┬───────┘  └───────┬───────┘
             │                 │                   │
             └─────────────────┼───────────────────┘
                               │
                        ┌──────┴──────┐
                        │   Broker    │
                        │   :7899     │
                        └─────────────┘
```

## Event routing

### Current events (broker already emits these)

```
peer_joined       — Claude instance registered
peer_left         — Claude instance unregistered
summary_changed   — Peer updated work summary
message_sent      — Message sent between peers
```

### New events (hooks publish these)

```
fleet.commit           — Git commit on any machine (from hook)
fleet.test.failed      — Test failure detected
fleet.deploy           — Deployment triggered
github.pr.opened       — PR opened (from GitHub webhook)
github.pr.updated      — PR updated
github.ci.failed       — CI check failed
github.issue.created   — New issue
sentry.alert           — Error alert from Sentry
llm.request            — LLM inference request on sonia's macbook
```

### How events reach daemons

**Phase 1 (now):** Daemons poll `/events` endpoint with a filter. Supervisor manages the polling loop and dispatches to the right daemon.

**Phase 2 (NATS):** Broker publishes events to NATS subjects. Daemons subscribe to subjects matching their `watch` config. No polling, instant delivery.

```
Phase 1:                              Phase 2:

Supervisor ──poll──> Broker           NATS Server
    │                  │                  │
    │ dispatch         │                  │ subscribe
    ▼                  │                  ▼
  Daemon               │               Daemon

(HTTP, 5s latency)                    (NATS, <1ms latency)
```

## NATS integration plan

### Why NATS

- Lightweight single binary (like claude-peers itself)
- Subject-based pub/sub maps perfectly to event types
- JetStream for persistence (events survive broker restart)
- Request/reply pattern for daemon-to-daemon communication
- Proven at scale (used by Synadia, Netlify, Rakuten)

### NATS subjects

```
fleet.>                    — All fleet events
fleet.commit.{machine}     — Commits from a specific machine
fleet.peer.joined          — Peer registrations
daemon.{name}.trigger      — Trigger a specific daemon
daemon.{name}.status       — Daemon status updates
github.{repo}.pr.>         — PR events for a repo
llm.{machine}.request      — LLM inference activity
```

### Migration path

1. Install NATS server on ubuntu-homelab alongside broker
2. Broker publishes events to NATS after writing to SQLite (dual-write)
3. Supervisor subscribes to NATS instead of polling HTTP
4. Gradually move all event routing through NATS
5. HTTP events API becomes a read-only view of NATS history

### NATS deployment

```bash
# On ubuntu-homelab (systemd service)
nats-server --jetstream --store_dir /var/lib/nats --addr 0.0.0.0 --port 4222

# Machines connect via Tailscale
nats sub "fleet.>" --server nats://<broker-ip>:4222
```

## Gridwatch integration

New section on dashboard: DAEMONS panel

```
┌─────────────────────────────────────┐
│ DAEMONS                        3/3 │
│ ┌──────────┐ ┌──────────┐ ┌──────┐│
│ │pr-helper │ │bug-triage│ │libr. ││
│ │ IDLE     │ │ RUNNING  │ │ IDLE ││
│ │ last: 4m │ │ 2m ago   │ │12m   ││
│ └──────────┘ └──────────┘ └──────┘│
└─────────────────────────────────────┘
```

Each daemon card shows:
- Name and current state (idle/running/error)
- Last trigger and time since
- Run count (today)
- Machine it runs on

## Daemon examples for Willy's fleet

### 1. PR Helper (ubuntu-homelab)
Watch GitHub webhooks. Fix merge conflicts, update descriptions, fix lint.

### 2. Dependency Guardian (ubuntu-homelab)
Weekly cron. Check all repos for outdated/vulnerable deps. Open PRs.

### 3. Docs Librarian (ubuntu-homelab)
Watch commits. When code changes, verify docs still match. Update stale READMEs.

### 4. Fleet Health Monitor (raspdeck)
Continuous. Watch gridwatch data. Alert via peer message when machines go offline, disk fills up, or LLM server goes down.

### 5. LLM Server Watchdog (raspdeck)
Watch sonia's macbook LLM metrics. Restart llama-server if it crashes. Alert on high error rates.

### 6. Bug Triage (ubuntu-homelab)
Watch Sentry/error webhooks. Triage bugs, create issues, link to relevant code.

### 7. Deployment Daemon (ubuntu-homelab)
Watch for merged PRs to main. Run deploys to humanfrontiertests.com. Report status via peer messages.

## Cloud compatibility

The entire stack runs anywhere:
- **Bare metal** — Current setup (Tailscale mesh, commodity hardware)
- **Docker** — Broker, NATS, and daemons as containers
- **Sysbox/VM** — Isolated daemon environments with their own filesystems
- **Cloud VMs** — GCP/AWS instances join the Tailscale mesh, same architecture

The broker doesn't care where peers connect from. A daemon running in a GCP sysbox container registers the same way as one on the thinkbook.

## Implementation order

### Sprint 1: Daemon definitions + supervisor
- [ ] Daemon config format (daemon.json + agent.md)
- [ ] `claude-peers daemon start/stop/list` commands
- [ ] Supervisor spawns Claude Code with `--agent`
- [ ] Daemon status reported to broker as special peer type
- [ ] Gridwatch daemon panel

### Sprint 2: Event routing
- [ ] Hooks publish typed events to broker (github, fleet, llm)
- [ ] Supervisor filters events and dispatches to matching daemons
- [ ] Daemon run history in SQLite (trigger, duration, outcome)
- [ ] `claude-peers daemon logs` command

### Sprint 3: NATS
- [ ] NATS server on ubuntu-homelab (systemd)
- [ ] Broker dual-writes events to SQLite + NATS
- [ ] Supervisor subscribes to NATS instead of HTTP polling
- [ ] Dashboard shows NATS subject activity

### Sprint 4: Production daemons
- [ ] PR Helper daemon
- [ ] Fleet Health Monitor daemon
- [ ] LLM Watchdog daemon
- [ ] Docs Librarian daemon

## Principles

- **Build on what exists.** Every piece of this uses infrastructure we already deployed tonight. Broker, events, hooks, gridwatch, deploy.sh, SSH keys, Tailscale, systemd.
- **Single binary.** claude-peers stays one binary. Daemon supervisor is a subcommand, not a separate tool.
- **Self-documenting Go.** Clean, opinionated, no abstractions until the third use. Any experienced Go dev should read the code and immediately understand the architecture.
- **Portable.** Runs on a Pi Zero, a ThinkPad, a cloud VM, or a container. Same binary, same config format, same broker protocol.
- **Trust boundary is Tailscale.** If you're on the mesh, you're trusted. No extra auth layer.
