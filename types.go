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

// HeartbeatResponse tells the client whether its session is still known to
// the broker. Pre-T10 the /heartbeat endpoint returned only {"ok":true} as a
// fixed envelope: the UPDATE behind heartbeat no-ops silently on missing
// rows, so a client whose peer row had been stale-swept (broker restart,
// explicit unregister, or sweep after a network partition) kept heartbeating
// forever into the void. T10 surfaces the eviction: when RowsAffected == 0
// the broker returns {"ok":false,"reason":"unknown_session"} and the client
// transparently re-registers. Old clients that decode into a nil result
// still see HTTP 200 and ignore the body -- forward-compatible wire change.
type HeartbeatResponse struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// HeartbeatReasonUnknownSession signals the caller that its session_id no
// longer matches any peer row. The broker sets this when Reason is returned
// from /heartbeat or /set-summary. Callers detect it to trigger re-register.
const HeartbeatReasonUnknownSession = "unknown_session"

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

// DeliveryStatus is the broker's verdict on a sent message. Set by post-T11
// brokers to give callers a precise three-way signal; older brokers omit it
// and callers must fall back to the Queued bool. Empty means "broker did
// not report status -- treat as ambiguous and decide based on Queued".
type DeliveryStatus string

const (
	// DeliveryStatusBound: a live session is currently holding the recipient
	// (agent or session ID). The message is bound to that session, push
	// delivery + ACK still pending but the recipient is online.
	DeliveryStatusBound DeliveryStatus = "bound"
	// DeliveryStatusQueuedOffline: the recipient agent name has been claimed
	// before but no session currently holds it. Will deliver on reconnect.
	DeliveryStatusQueuedOffline DeliveryStatus = "queued_offline"
	// DeliveryStatusQueuedUnknown: no session has ever claimed this agent
	// name on this broker. Likely a typo -- the message will queue but may
	// sit indefinitely.
	DeliveryStatusQueuedUnknown DeliveryStatus = "queued_unknown"
)

type SendMessageResponse struct {
	OK        bool   `json:"ok"`
	MessageID int    `json:"message_id,omitempty"`
	Queued    bool   `json:"queued,omitempty"` // true if delivered to queue (recipient offline)
	// DeliveryStatus refines Queued with one of three named values. Old
	// brokers omit this field; new clients reading an old broker should fall
	// back to Queued alone (no typo warning, since the broker can't
	// distinguish offline-known from never-claimed).
	DeliveryStatus DeliveryStatus `json:"delivery_status,omitempty"`
	Error          string         `json:"error,omitempty"`
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

// ClaimAgentRequest lets a live session claim an agent name post-registration.
// Used by the MCP `claim_agent_name` tool so users can name sessions without
// restarting them. Still subject to the global uniqueness rule -- if the name
// is held by another live session, returns the same conflict block as register.
// A session can only claim once: sessions that already have an agent_name get
// an error (identity is explicit, mutation is not allowed).
type ClaimAgentRequest struct {
	SessionID string `json:"session_id"`
	AgentName string `json:"agent_name"`
}

type ClaimAgentResponse struct {
	OK            bool   `json:"ok"`
	Error         string `json:"error,omitempty"`
	HeldBySession string `json:"held_by_session,omitempty"`
	HeldByMachine string `json:"held_by_machine,omitempty"`
	HeldByCWD     string `json:"held_by_cwd,omitempty"`
	HeldBySince   string `json:"held_by_since,omitempty"`
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
