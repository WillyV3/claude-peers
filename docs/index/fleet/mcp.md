---
room: fleet/mcp
subdomain: fleet
source_paths: fleet_mcp
architectural_health: normal
security_tier: normal
see_also:
  - broker/core.md
hot_paths:
  - fleet_mcp.go
---

# fleet_mcp.go

DOES: Implements the MCP (Model Context Protocol) stdio server interface for claude-peers. When `claude-peers server` is invoked, this is the entry point. Exposes peer/list/send/check_messages/set_summary/claim_agent_name as MCP tools that Claude Code sessions can call directly.

SYMBOLS:
- ServerCommand() → MCP stdio server loop
- handleListPeersTool / handleSendMessageTool / handleCheckMessagesTool / handleSetSummaryTool / handleClaimAgentNameTool

PATTERNS:
- **MCP push delivery via notifications/claude/channel** — when a peer message arrives for the current session, the MCP server sends a `notifications/claude/channel` notification that auto-injects into the Claude Code conversation. This is the "push" path; pull is `check_messages`.
- **Schema strict** — meta values must be `Record<string, string>`. Sending an int (e.g. message_id as a number) silently fails to surface (documented in feedback_mcp_push_not_visible.md).

USE WHEN: Debugging MCP integration failures, adding a new MCP tool, modifying push notification format.
