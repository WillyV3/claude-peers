package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
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
		"description": "Send a message to another Claude Code instance by peer ID. The message will be pushed into their session immediately via channel notification.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to_id": map[string]any{
					"type":        "string",
					"description": "The peer ID of the target Claude Code instance (from list_peers)",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "The message to send",
				},
			},
			"required": []string{"to_id", "message"},
		},
	},
	{
		"name":        "set_summary",
		"description": "Set a brief summary (1-2 sentences) of what you are currently working on. This is visible to other Claude Code instances when they list peers.",
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
		"description": "Manually check for new messages from other Claude Code instances. Messages are normally pushed automatically via channel notifications, but you can use this as a fallback.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{},
		},
	},
}

var mcpInstructions = `You are connected to the claude-peers network. Other Claude Code instances across multiple machines can see you and send you messages.

IMPORTANT RULES:
1. When you START a new conversation, call check_messages to see if anyone sent you something.
2. When the user gives you a new prompt, call check_messages FIRST before doing anything else.
3. If there are messages, tell the user who sent what, and reply using send_message.
4. When you start, call set_summary to describe what you're working on.

If you receive a <channel source="claude-peers" ...> notification, respond to it immediately using send_message.

Available tools:
- list_peers: Discover other Claude Code instances (scope: all/machine/directory/repo)
- send_message: Send a message to another instance by ID
- set_summary: Set a 1-2 sentence summary of what you're working on (visible to other peers)
- check_messages: Check for new messages from other Claude Code instances -- CALL THIS ON EVERY USER PROMPT`

func handleInitialize(id any, t *MCPTransport) {
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
		"instructions": mcpInstructions,
	})
}

func handleToolsList(id any, t *MCPTransport) {
	t.respond(id, map[string]any{"tools": mcpTools})
}

func logMCP(msg string, args ...any) {
	log.Printf("[claude-peers] "+msg, args...)
}
