package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// JSON-RPC 2.0 types -- minimal, no SDK needed.

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      any            `json:"id"`
	Result  any            `json:"result,omitempty"`
	Error   *jsonrpcError  `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonrpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// MCPTransport reads JSON-RPC from stdin, writes to stdout.
// Thread-safe writes via mutex.
type MCPTransport struct {
	scanner *bufio.Scanner
	writer  io.Writer
	mu      sync.Mutex
}

func newMCPTransport() *MCPTransport {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer
	return &MCPTransport{
		scanner: scanner,
		writer:  os.Stdout,
	}
}

func (t *MCPTransport) readRequest() (jsonrpcRequest, error) {
	if !t.scanner.Scan() {
		if err := t.scanner.Err(); err != nil {
			return jsonrpcRequest{}, err
		}
		return jsonrpcRequest{}, io.EOF
	}
	var req jsonrpcRequest
	err := json.Unmarshal(t.scanner.Bytes(), &req)
	return req, err
}

func (t *MCPTransport) writeResponse(resp jsonrpcResponse) {
	t.mu.Lock()
	defer t.mu.Unlock()
	data, _ := json.Marshal(resp)
	fmt.Fprintf(t.writer, "%s\n", data)
}

func (t *MCPTransport) writeNotification(method string, params any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	notif := jsonrpcNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	data, _ := json.Marshal(notif)
	fmt.Fprintf(t.writer, "%s\n", data)
}

func (t *MCPTransport) respond(id any, result any) {
	t.writeResponse(jsonrpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (t *MCPTransport) respondError(id any, code int, msg string) {
	t.writeResponse(jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonrpcError{Code: code, Message: msg},
	})
}

// MCP protocol constants
const (
	mcpProtocolVersion = "2025-03-26"
	serverName         = "claude-peers"
	serverVersion      = "1.0.0"
)

// Tool schema definitions for MCP
var mcpTools = []map[string]any{
	{
		"name":        "list_peers",
		"description": "List other Claude Code instances across the network. Returns their ID, machine, working directory, git repo, and summary.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"scope": map[string]any{
					"type":        "string",
					"enum":        []string{"all", "machine", "directory", "repo"},
					"description": `Scope of peer discovery. "all" = every peer on every machine. "machine" = only this machine. "directory" = same working directory. "repo" = same git repository.`,
				},
			},
			"required": []string{"scope"},
		},
	},
	{
		"name":        "send_message",
		"description": "Send a message to another Claude Code session. Use the agent name (stable handle) when you know it; fall back to session ID for ephemeral peers. Agent-addressed messages queue if the recipient is offline and deliver on reconnect. Session-addressed messages drop if the session is gone.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to": map[string]any{
					"type":        "string",
					"description": "Agent name (preferred, stable across restarts) or session ID (for ephemeral peers). Agent names are listed by list_peers under the (agent) label.",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "The message to send",
				},
			},
			"required": []string{"to", "message"},
		},
	},
	{
		"name":        "set_summary",
		"description": "Set a brief summary (1-2 sentences) of what you are currently working on. Visible to other Claude Code instances when they list peers.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary": map[string]any{
					"type":        "string",
					"description": "A 1-2 sentence summary of your current work",
				},
			},
			"required": []string{"summary"},
		},
	},
	{
		"name":        "check_messages",
		"description": "Manually check for new messages. Normally messages push automatically via notifications/claude/channel with ACK; this tool is the fallback path.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{},
		},
	},
	{
		"name":        "claim_agent_name",
		"description": "Claim a stable agent name for THIS session without restarting. Use this when the user tells you to call yourself something (e.g. 'you are jim'), or when you want an addressable stable handle. Names are globally unique while held -- if another session already holds the name, you get a 409 with the holder's info. A session can only claim once; after that, the name is stuck. Preferred over restarting with --as when the session is already mid-work.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "The agent name to claim (e.g. 'jim', 'caretaker'). Must be unique across the fleet.",
				},
			},
			"required": []string{"name"},
		},
	},
}

var mcpInstructions = `You are connected to the claude-peers network. Other Claude Code sessions across the fleet can see you and send you messages.

IDENTITY:
- Your identity on the network is your "agent name" -- declared at startup via the --as flag, CLAUDE_PEERS_AGENT env var, or a .claude-peers-agent file in the working directory.
- If you have no agent name, you are ephemeral: visible in list_peers but NOT addressable by name. You can still send messages.
- Agent names are globally unique while held. Never change names mid-session -- identity is declared at startup.

MESSAGING:
- Push delivery: messages arrive as notifications/claude/channel with from_agent / from_session / message_id in the meta block. You don't need to poll on every prompt -- push + ack is reliable.
- To reply to a notification, call send_message with to = from_agent (stable, preferred) or from_session (ephemeral fallback).
- send_message to an offline agent name queues the message -- it delivers when that agent reconnects. Don't assume the recipient is online.
- On new conversations: call check_messages ONCE to drain anything that came in before the MCP channel was up. After that, trust the push.

TOOLS:
- list_peers: Discover sessions on the network (scope: all/machine/directory/repo).
- send_message(to, message): Send a message. "to" is an agent name or session ID.
- set_summary: Set a 1-2 sentence summary of your current work.
- check_messages: Drain undelivered messages (fallback -- push is the normal path).
- claim_agent_name(name): Claim a stable agent name for THIS session without restarting. Use when the user says "you are X" or "call yourself X". Names are globally unique while held. A session can only claim once.`

func handleInitialize(id any, t *MCPTransport) {
	// Build dynamic instructions with fleet context injection.
	instructions := mcpInstructions + buildFleetContext()

	t.respond(id, map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities": map[string]any{
			"experimental": map[string]any{
				"claude/channel": map[string]any{},
			},
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    serverName,
			"version": serverVersion,
		},
		"instructions": instructions,
	})
}

// buildFleetContext fetches active peers, recent events, and fleet memory
// from the broker and returns a context string to inject into Claude's session.
// Runs at session start -- gives Claude immediate awareness of the fleet.
func buildFleetContext() string {
	var ctx string

	// Active peers
	var peers []Peer
	if err := cliFetch("/list-peers", ListPeersRequest{Scope: "all"}, &peers); err == nil && len(peers) > 0 {
		ctx += "\n\n--- FLEET CONTEXT (injected at session start) ---"
		ctx += fmt.Sprintf("\n%d active Claude session(s) on the network:", len(peers))
		for _, p := range peers {
			label := p.AgentName
			if label == "" {
				label = "session " + p.ID + " (ephemeral)"
			}
			line := fmt.Sprintf("\n- %s on %s", label, p.Machine)
			if p.Summary != "" {
				line += fmt.Sprintf(" -- %s", p.Summary)
			}
			ctx += line
		}
	}

	// Recent events (last 5)
	var events []Event
	if err := cliFetch("/events?limit=5", nil, &events); err == nil && len(events) > 0 {
		ctx += "\n\nRecent fleet events:"
		for _, e := range events {
			ctx += fmt.Sprintf("\n- [%s] %s %s", e.Type, e.PeerID, e.Data)
		}
	}

	// Fleet memory snippet (first 500 chars)
	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequest("GET", cfg.BrokerURL+"/fleet-memory", nil)
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	if resp, err := client.Do(req); err == nil && resp.StatusCode == 200 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		mem := string(body)
		if len(mem) > 500 {
			mem = mem[:500] + "..."
		}
		if len(mem) > 10 {
			ctx += "\n\nFleet memory:\n" + mem
		}
	}

	return ctx
}

func handleToolsList(id any, t *MCPTransport) {
	t.respond(id, map[string]any{"tools": mcpTools})
}

func logMCP(msg string, args ...any) {
	log.Printf("[claude-peers] "+msg, args...)
}
