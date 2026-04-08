package main

import "time"

// Peer represents a registered claude-peers session -- one running subprocess.
// A peer is ephemeral (lifetime of one `claude` invocation). If the peer declared
// an AgentName at registration, other peers can address it by that stable handle
// across restarts; otherwise it is unnameable and can only be reached by session ID.
type Peer struct {
	ID           string `json:"id"`
	AgentName    string `json:"agent_name"` // "" = ephemeral, not addressable by name
	PID          int    `json:"pid"`
	Machine      string `json:"machine"`
	CWD          string `json:"cwd"`
	GitRoot      string `json:"git_root"`
	TTY          string `json:"tty"`
	Project      string `json:"project"`
	Branch       string `json:"branch"`
	Summary      string `json:"summary"`
	RegisteredAt string `json:"registered_at"`
	LastSeen     string `json:"last_seen"`
}

// Message is a queued message routed to an agent name or directly to a session ID.
// Messages addressed ToAgent persist even if the holding session dies; they drain
// when a new session claims the same agent name. Messages addressed ToSession are
// dropped if the session is gone.
type Message struct {
	ID          int    `json:"id"`
	ToAgent     string `json:"to_agent"`    // "" if addressed to session directly
	ToSession   string `json:"to_session"`  // "" if addressed to an agent name
	FromSession string `json:"from_session"`
	FromAgent   string `json:"from_agent"`  // "" if sender was ephemeral
	Text        string `json:"text"`
	SentAt      string `json:"sent_at"`
	DeliveredAt string `json:"delivered_at,omitempty"`
	AckAt       string `json:"ack_at,omitempty"`
	AckSession  string `json:"ack_session,omitempty"`
	Attempts    int    `json:"attempts"`
}

// --- Broker API request/response types ---

// RegisterRequest declares a new session. AgentName is optional; if provided
// and already held by a live session, register fails with a populated conflict
// block (no silent disambiguation -- fail fast).
type RegisterRequest struct {
	AgentName string `json:"agent_name"` // optional stable handle, must be unique if set
	PID       int    `json:"pid"`
	Machine   string `json:"machine"`
	CWD       string `json:"cwd"`
	GitRoot   string `json:"git_root"`
	TTY       string `json:"tty"`
	Project   string `json:"project"`
	Branch    string `json:"branch"`
	Summary   string `json:"summary"`
}

type RegisterResponse struct {
	OK bool   `json:"ok"`
	ID string `json:"id,omitempty"` // session ID on success
	// Conflict block (populated only when OK=false due to agent-name collision):
	Error         string `json:"error,omitempty"`
	HeldBySession string `json:"held_by_session,omitempty"`
	HeldByMachine string `json:"held_by_machine,omitempty"`
	HeldByCWD     string `json:"held_by_cwd,omitempty"`
	HeldBySince   string `json:"held_by_since,omitempty"`
}

type HeartbeatRequest struct {
	ID string `json:"id"`
}

type SetSummaryRequest struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

// ListPeersRequest scopes a peer listing.
type ListPeersRequest struct {
	Scope     string `json:"scope"`
	Machine   string `json:"machine"`
	CWD       string `json:"cwd"`
	GitRoot   string `json:"git_root"`
	ExcludeID string `json:"exclude_id"`
}

// SendMessageRequest routes either to a named agent (ToAgent) or directly to
// a session ID (ToSession). Exactly one must be set. If ToAgent is set and
// no live session currently holds that agent, the message queues.
type SendMessageRequest struct {
	FromID    string `json:"from_id"` // caller's session ID
	ToAgent   string `json:"to_agent"`
	ToSession string `json:"to_session"`
	Text      string `json:"text"`
}

type SendMessageResponse struct {
	OK        bool   `json:"ok"`
	MessageID int    `json:"message_id,omitempty"`
	Queued    bool   `json:"queued,omitempty"` // true if delivered to queue (recipient offline)
	Error     string `json:"error,omitempty"`
}

// PollMessagesRequest fetches undelivered messages for a session. If the session
// holds an agent name, queued messages for that agent are drained too.
type PollMessagesRequest struct {
	ID string `json:"id"` // session ID
}

type PollMessagesResponse struct {
	Messages []Message `json:"messages"`
}

// AckMessageRequest confirms delivery to the MCP client. The broker only marks
// a message as delivered after receiving an ACK, so push failures can be retried.
type AckMessageRequest struct {
	SessionID string `json:"session_id"`
	MessageID int    `json:"message_id"`
}

type UnregisterRequest struct {
	ID string `json:"id"`
}

type HealthResponse struct {
	Status  string `json:"status"`
	Peers   int    `json:"peers"`
	Machine string `json:"machine"`
}

// Event represents a broker event for pub/sub.
type Event struct {
	ID        int    `json:"id"`
	Type      string `json:"type"`
	PeerID    string `json:"peer_id"`
	Machine   string `json:"machine"`
	Data      string `json:"data"`
	CreatedAt string `json:"created_at"`
}

// ChallengeRequest is sent by a client to verify the broker's identity.
type ChallengeRequest struct {
	Nonce string `json:"nonce"`
}

// ChallengeResponse is the broker's signed proof of identity.
type ChallengeResponse struct {
	Nonce     string `json:"nonce"`
	Signature string `json:"signature"`  // base64url-encoded Ed25519 signature
	PublicKey string `json:"public_key"` // base64url-encoded public key
}

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
