package main

import "time"

// Peer represents a registered Claude Code instance.
type Peer struct {
	ID           string `json:"id"`
	PID          int    `json:"pid"`
	Machine      string `json:"machine"`
	CWD          string `json:"cwd"`
	GitRoot      string `json:"git_root"`
	TTY          string `json:"tty"`
	Name         string `json:"name"`    // auto-generated: repo@branch or dir-basename
	Project      string `json:"project"` // repo or directory name
	Branch       string `json:"branch"`  // git branch
	Summary      string `json:"summary"`
	RegisteredAt string `json:"registered_at"`
	LastSeen     string `json:"last_seen"`
}

// Message is a queued message between peers.
type Message struct {
	ID        int    `json:"id"`
	FromID    string `json:"from_id"`
	ToID      string `json:"to_id"`
	Text      string `json:"text"`
	SentAt    string `json:"sent_at"`
	Delivered bool   `json:"delivered"`
}

// --- Broker API request/response types ---

type RegisterRequest struct {
	PID     int    `json:"pid"`
	Machine string `json:"machine"`
	CWD     string `json:"cwd"`
	GitRoot string `json:"git_root"`
	TTY     string `json:"tty"`
	Name    string `json:"name"`
	Project string `json:"project"`
	Branch  string `json:"branch"`
	Summary string `json:"summary"`
}

type RegisterResponse struct {
	ID string `json:"id"`
}

type HeartbeatRequest struct {
	ID string `json:"id"`
}

type SetSummaryRequest struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

type SetNameRequest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ListPeersRequest struct {
	Scope     string `json:"scope"`
	Machine   string `json:"machine"`
	CWD       string `json:"cwd"`
	GitRoot   string `json:"git_root"`
	ExcludeID string `json:"exclude_id"`
}

type SendMessageRequest struct {
	FromID string `json:"from_id"`
	ToID   string `json:"to_id"`
	Text   string `json:"text"`
}

type SendMessageResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type PollMessagesRequest struct {
	ID string `json:"id"`
}

type PollMessagesResponse struct {
	Messages []Message `json:"messages"`
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
	return time.Now().UTC().Format(time.RFC3339)
}
