package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// loadBrokerRootToken loads the root token for the broker, with migration support.
// It prefers root-token.jwt over token.jwt.
// Migration: if root-token.jwt doesn't exist but token.jwt does AND it is a root token
// (issuer == audience), it copies it to root-token.jwt automatically.
func loadBrokerRootToken(dir string) (string, error) {
	token, err := LoadRootToken(dir)
	if err == nil {
		return token, nil
	}

	// root-token.jwt not found; fall back to token.jwt.
	token, err = LoadToken(dir)
	if err != nil {
		return "", fmt.Errorf("no root token or peer token found: %w", err)
	}

	// Check if the token from token.jwt is a root token (issuer == audience).
	// We do a best-effort parse without full validation here.
	parts := strings.SplitN(token, ".", 3)
	if len(parts) == 3 {
		payload, decErr := base64.RawURLEncoding.DecodeString(parts[1])
		if decErr == nil {
			var claims struct {
				Issuer   string   `json:"iss"`
				Audience []string `json:"aud"`
			}
			if jsonErr := json.Unmarshal(payload, &claims); jsonErr == nil {
				if len(claims.Audience) > 0 && claims.Issuer == claims.Audience[0] {
					// This is a root token: migrate it.
					if saveErr := SaveRootToken(token, dir); saveErr == nil {
						log.Printf("[broker] migrated root token to %s/root-token.jwt", dir)
					}
					return token, nil
				}
			}
		}
	}

	// token.jwt exists but is not a root token -- return an error so the operator
	// is aware that the root token is missing rather than silently using a peer token.
	return "", fmt.Errorf("token.jwt is not a root token and root-token.jwt does not exist")
}

// colMissing returns true if the given table does not exist or does not have
// the named column. Used for one-shot schema migrations in newBroker().
func colMissing(db *sql.DB, table, col string) bool {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return true // table doesn't exist
	}
	defer rows.Close()
	found := false
	any := false
	for rows.Next() {
		any = true
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk)
		if name == col {
			found = true
		}
	}
	if !any {
		return true // PRAGMA returned no rows -- table doesn't exist
	}
	return !found
}

func generatePeerID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type Broker struct {
	db          *sql.DB
	nats        *NATSPublisher
	fleetMemory string
	mu          sync.RWMutex
	validator   *TokenValidator
}

func newBroker() (*Broker, error) {
	dbPath := cfg.DBPath
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(3000)")
	if err != nil {
		return nil, err
	}

	// ADR-001: one-shot migration from the pre-rewrite schema. Only drops the
	// old tables if they lack the new columns (agent_name on peers, to_agent
	// on messages). Fresh deploys and upgraded deploys both end up at the
	// same schema; subsequent restarts are no-ops (tables already correct).
	if colMissing(db, "peers", "agent_name") {
		db.Exec(`DROP TABLE IF EXISTS peers`)
	}
	if colMissing(db, "messages", "to_agent") {
		db.Exec(`DROP TABLE IF EXISTS messages`)
	}

	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS peers (
			id TEXT PRIMARY KEY,
			agent_name TEXT NOT NULL DEFAULT '',
			pid INTEGER NOT NULL,
			machine TEXT NOT NULL DEFAULT '',
			cwd TEXT NOT NULL,
			git_root TEXT,
			tty TEXT,
			project TEXT NOT NULL DEFAULT '',
			branch TEXT NOT NULL DEFAULT '',
			summary TEXT NOT NULL DEFAULT '',
			started_at TEXT NOT NULL,
			last_seen TEXT NOT NULL
		)`,
		// Enforce agent_name uniqueness when non-empty.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_peers_agent_name
		 ON peers(agent_name) WHERE agent_name != ''`,
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			to_agent TEXT NOT NULL DEFAULT '',
			to_session TEXT NOT NULL DEFAULT '',
			from_session TEXT NOT NULL,
			from_agent TEXT NOT NULL DEFAULT '',
			text TEXT NOT NULL,
			sent_at TEXT NOT NULL,
			delivered_at TEXT,
			ack_at TEXT,
			ack_session TEXT,
			attempts INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_messages_to_agent ON messages(to_agent) WHERE to_agent != ''`,
		`CREATE INDEX IF NOT EXISTS idx_messages_to_session ON messages(to_session) WHERE to_session != ''`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT NOT NULL,
			peer_id TEXT,
			machine TEXT,
			data TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS kv (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
	} {
		db.Exec(stmt)
	}

	b := &Broker{db: db, nats: newNATSPublisher()}

	// Load UCAN keypair and create token validator.
	kp, err := LoadKeyPair(configDir())
	if err != nil {
		log.Printf("[broker] WARNING: no keypair found (%v) -- all requests will get 401", err)
	} else {
		b.validator = NewTokenValidator(kp.PublicKey)
		rootToken, err := loadBrokerRootToken(configDir())
		if err != nil {
			log.Printf("[broker] WARNING: no root token found (%v) -- all requests will get 401", err)
		} else {
			b.validator.RegisterToken(rootToken, AllCapabilities())
		}
	}

	// Restore fleet memory from SQLite if available
	var mem sql.NullString
	db.QueryRow("SELECT value FROM kv WHERE key = 'fleet_memory'").Scan(&mem)
	if mem.Valid {
		b.fleetMemory = mem.String
	}

	// Periodic stale cleanup. Runs every 2 seconds so dead peers are evicted
	// within a few seconds of their last heartbeat crossing the staleTimeout
	// threshold. WAL checkpoint + message cleanup still run, just on a slower
	// cadence (every 30 sweeps == 1 minute) since those are heavier ops.
	go func() {
		var tick int
		for {
			b.cleanStalePeers()
			tick++
			if tick%30 == 0 {
				db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
				db.Exec("DELETE FROM messages WHERE ack_at IS NOT NULL AND ack_at < ?",
					time.Now().UTC().Add(-1*time.Hour).Format(time.RFC3339))
				db.Exec("DELETE FROM messages WHERE ack_at IS NULL AND sent_at < ?",
					time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339))
			}
			time.Sleep(2 * time.Second)
		}
	}()

	return b, nil
}

func (b *Broker) emitEvent(eventType, peerID, machine, data string) {
	b.db.Exec(
		"INSERT INTO events (type, peer_id, machine, data, created_at) VALUES (?, ?, ?, ?, ?)",
		eventType, peerID, machine, data, nowISO(),
	)
}

func (b *Broker) recentEvents(limit int) []Event {
	rows, err := b.db.Query(
		"SELECT id, type, peer_id, machine, data, created_at FROM events ORDER BY id DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		var peerID, machine sql.NullString
		rows.Scan(&e.ID, &e.Type, &peerID, &machine, &e.Data, &e.CreatedAt)
		e.PeerID = peerID.String
		e.Machine = machine.String
		events = append(events, e)
	}
	if events == nil {
		events = []Event{}
	}
	return events
}

// cleanStalePeers removes sessions that haven't heartbeated within the timeout.
// Also resets any agent-queued messages that were bound to the dead session
// back into the queue so the next holder of that agent name receives them.
func (b *Broker) cleanStalePeers() {
	timeout := cfg.StaleTimeout
	if timeout <= 0 {
		timeout = 20
	}
	cutoff := time.Now().UTC().Add(-time.Duration(timeout) * time.Second).Format(time.RFC3339)

	// Collect IDs of sessions we're about to delete so we can reset their messages.
	rows, err := b.db.Query("SELECT id FROM peers WHERE last_seen < ?", cutoff)
	var deadIDs []string
	if err == nil {
		for rows.Next() {
			var id string
			rows.Scan(&id)
			deadIDs = append(deadIDs, id)
		}
		rows.Close()
	}

	// Delete stale peers -- agent names free immediately.
	b.db.Exec("DELETE FROM peers WHERE last_seen < ?", cutoff)

	// For each dead session: drop ephemeral messages, reset agent-queued messages.
	for _, id := range deadIDs {
		b.db.Exec("DELETE FROM messages WHERE to_session = ? AND to_agent = '' AND ack_at IS NULL", id)
		b.db.Exec(
			`UPDATE messages SET to_session = '', delivered_at = NULL
			 WHERE to_session = ? AND to_agent != '' AND ack_at IS NULL`,
			id,
		)
	}

	eventCutoff := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	b.db.Exec("DELETE FROM events WHERE created_at < ?", eventCutoff)
}

// register starts a new session. If AgentName is provided and already held by
// a live session, returns a conflict response (no silent disambiguation).
func (b *Broker) register(req RegisterRequest) RegisterResponse {
	now := nowISO()

	// Uniqueness check: agent names are global, hard-unique, fail fast on collision.
	if req.AgentName != "" {
		var held Peer
		var gitRoot, tty sql.NullString
		err := b.db.QueryRow(
			`SELECT id, machine, cwd, git_root, tty, started_at
			 FROM peers WHERE agent_name = ? LIMIT 1`,
			req.AgentName,
		).Scan(&held.ID, &held.Machine, &held.CWD, &gitRoot, &tty, &held.RegisteredAt)
		if err == nil {
			return RegisterResponse{
				OK:            false,
				Error:         fmt.Sprintf("agent %q already held by session %s", req.AgentName, held.ID),
				HeldBySession: held.ID,
				HeldByMachine: held.Machine,
				HeldByCWD:     held.CWD,
				HeldBySince:   held.RegisteredAt,
			}
		}
	}

	id := generatePeerID()
	b.db.Exec(
		`INSERT INTO peers (id, agent_name, pid, machine, cwd, git_root, tty, project, branch, summary, started_at, last_seen)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, req.AgentName, req.PID, req.Machine, req.CWD, req.GitRoot, req.TTY,
		req.Project, req.Branch, req.Summary, now, now,
	)

	// Drain any queued messages addressed to this agent. These were sent while
	// no session held the name; now that one has registered, they become deliverable.
	if req.AgentName != "" {
		b.db.Exec(
			`UPDATE messages SET to_session = ? WHERE to_agent = ? AND to_session = '' AND delivered_at IS NULL`,
			id, req.AgentName,
		)
	}

	b.emitEvent("peer_joined", id, req.Machine, req.Summary)
	b.nats.publish("fleet.peer.joined", FleetEvent{
		Type: "peer_joined", PeerID: id, Machine: req.Machine,
		Summary: req.Summary, CWD: req.CWD,
	})
	return RegisterResponse{OK: true, ID: id}
}

func (b *Broker) heartbeat(req HeartbeatRequest) {
	b.db.Exec("UPDATE peers SET last_seen = ? WHERE id = ?", nowISO(), req.ID)
}

func (b *Broker) setSummary(req SetSummaryRequest) {
	b.db.Exec("UPDATE peers SET summary = ? WHERE id = ?", req.Summary, req.ID)
	b.emitEvent("summary_changed", req.ID, "", req.Summary)
	b.nats.publish("fleet.summary", FleetEvent{
		Type: "summary_changed", PeerID: req.ID, Summary: req.Summary,
	})
}

func (b *Broker) listPeers(req ListPeersRequest) []Peer {
	var query string
	var args []any

	cols := "id, agent_name, pid, machine, cwd, git_root, tty, project, branch, summary, started_at, last_seen"
	switch req.Scope {
	case "directory":
		query = "SELECT " + cols + " FROM peers WHERE cwd = ?"
		args = []any{req.CWD}
	case "repo":
		if req.GitRoot != "" {
			query = "SELECT " + cols + " FROM peers WHERE git_root = ?"
			args = []any{req.GitRoot}
		} else {
			query = "SELECT " + cols + " FROM peers WHERE cwd = ?"
			args = []any{req.CWD}
		}
	case "machine":
		if req.Machine != "" {
			query = "SELECT " + cols + " FROM peers WHERE machine = ?"
			args = []any{req.Machine}
		} else {
			query = "SELECT " + cols + " FROM peers"
		}
	default:
		query = "SELECT " + cols + " FROM peers"
	}

	rows, err := b.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var peers []Peer
	for rows.Next() {
		var p Peer
		var gitRoot, tty sql.NullString
		rows.Scan(&p.ID, &p.AgentName, &p.PID, &p.Machine, &p.CWD, &gitRoot, &tty,
			&p.Project, &p.Branch, &p.Summary, &p.RegisteredAt, &p.LastSeen)
		p.GitRoot = gitRoot.String
		p.TTY = tty.String

		if req.ExcludeID != "" && p.ID == req.ExcludeID {
			continue
		}
		peers = append(peers, p)
	}
	return peers
}

// sendMessage routes a message either to a named agent or directly to a
// session ID. Exactly one of ToAgent or ToSession must be set. Messages to
// an agent with no live holder are queued; messages to a non-existent session
// error out immediately.
func (b *Broker) sendMessage(req SendMessageRequest) SendMessageResponse {
	if (req.ToAgent == "") == (req.ToSession == "") {
		return SendMessageResponse{OK: false, Error: "exactly one of to_agent or to_session must be set"}
	}

	now := nowISO()

	// Look up sender's agent name (if any) for the from_agent field.
	var fromAgent sql.NullString
	b.db.QueryRow("SELECT agent_name FROM peers WHERE id = ?", req.FromID).Scan(&fromAgent)

	var toSession, toAgent string
	var queued bool

	if req.ToSession != "" {
		// Direct session targeting. Session must exist -- no queueing for ephemeral peers.
		var exists bool
		b.db.QueryRow("SELECT EXISTS(SELECT 1 FROM peers WHERE id = ?)", req.ToSession).Scan(&exists)
		if !exists {
			return SendMessageResponse{OK: false, Error: fmt.Sprintf("session %s not found", req.ToSession)}
		}
		toSession = req.ToSession
	} else {
		// Agent targeting. Find the session currently holding the name, if any.
		toAgent = req.ToAgent
		var holder string
		err := b.db.QueryRow("SELECT id FROM peers WHERE agent_name = ? LIMIT 1", req.ToAgent).Scan(&holder)
		if err == nil && holder != "" {
			toSession = holder
		} else {
			queued = true // no live holder -- message sits on the agent queue
		}
	}

	result, err := b.db.Exec(
		`INSERT INTO messages (to_agent, to_session, from_session, from_agent, text, sent_at, attempts)
		 VALUES (?, ?, ?, ?, ?, ?, 0)`,
		toAgent, toSession, req.FromID, fromAgent.String, req.Text, now,
	)
	if err != nil {
		return SendMessageResponse{OK: false, Error: err.Error()}
	}
	msgID, _ := result.LastInsertId()

	msgPreview := req.Text
	if len(msgPreview) > 500 {
		msgPreview = msgPreview[:500] + "..."
	}
	target := toAgent
	if target == "" {
		target = toSession
	}
	eventData := fmt.Sprintf("to=%s text=%s", target, msgPreview)
	b.emitEvent("message_sent", req.FromID, "", eventData)

	b.nats.publish("fleet.message", FleetEvent{
		Type: "message_sent", PeerID: req.FromID, Data: target,
	})
	return SendMessageResponse{OK: true, MessageID: int(msgID), Queued: queued}
}

// pollMessages returns undelivered messages addressed to the session directly
// OR to any agent name the session currently holds. Messages returned here are
// marked delivered_at but not ack_at -- the caller should ack each one, or
// they will be surfaced again on the next poll after a retry window.
func (b *Broker) pollMessages(req PollMessagesRequest) PollMessagesResponse {
	// Look up agent name for the caller so we can drain the queue.
	var agentName sql.NullString
	b.db.QueryRow("SELECT agent_name FROM peers WHERE id = ?", req.ID).Scan(&agentName)

	rows, err := b.db.Query(
		`SELECT id, to_agent, to_session, from_session, from_agent, text, sent_at, attempts
		 FROM messages
		 WHERE delivered_at IS NULL
		   AND (to_session = ? OR (to_agent != '' AND to_agent = ?))
		 ORDER BY sent_at ASC`,
		req.ID, agentName.String,
	)
	if err != nil {
		return PollMessagesResponse{Messages: []Message{}}
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		rows.Scan(&m.ID, &m.ToAgent, &m.ToSession, &m.FromSession, &m.FromAgent, &m.Text, &m.SentAt, &m.Attempts)
		msgs = append(msgs, m)
	}

	now := nowISO()
	for _, m := range msgs {
		// Mark delivered (visible to caller) and bind to this session so ack can verify.
		// Not acked yet -- if no ack arrives, retry logic can reset delivered_at.
		b.db.Exec(
			`UPDATE messages SET delivered_at = ?, to_session = ?, attempts = attempts + 1 WHERE id = ?`,
			now, req.ID, m.ID,
		)
	}

	if msgs == nil {
		msgs = []Message{}
	}
	return PollMessagesResponse{Messages: msgs}
}

// peekMessages returns undelivered messages without marking them delivered.
// Used by the background poll loop -- messages stay available for check_messages.
func (b *Broker) peekMessages(req PollMessagesRequest) PollMessagesResponse {
	var agentName sql.NullString
	b.db.QueryRow("SELECT agent_name FROM peers WHERE id = ?", req.ID).Scan(&agentName)

	rows, err := b.db.Query(
		`SELECT id, to_agent, to_session, from_session, from_agent, text, sent_at, attempts
		 FROM messages
		 WHERE delivered_at IS NULL
		   AND (to_session = ? OR (to_agent != '' AND to_agent = ?))
		 ORDER BY sent_at ASC`,
		req.ID, agentName.String,
	)
	if err != nil {
		return PollMessagesResponse{Messages: []Message{}}
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		rows.Scan(&m.ID, &m.ToAgent, &m.ToSession, &m.FromSession, &m.FromAgent, &m.Text, &m.SentAt, &m.Attempts)
		msgs = append(msgs, m)
	}
	if msgs == nil {
		msgs = []Message{}
	}
	return PollMessagesResponse{Messages: msgs}
}

// claimAgent lets a live session adopt an agent name post-registration.
// Subject to the same uniqueness rule as register. A session may claim exactly
// once -- if it already has a name, this returns an error (no runtime rename).
// On success, any messages queued for the claimed agent name drain to this
// session immediately, same as register.
func (b *Broker) claimAgent(req ClaimAgentRequest) ClaimAgentResponse {
	if req.AgentName == "" {
		return ClaimAgentResponse{OK: false, Error: "agent_name is required"}
	}

	// Session must exist.
	var currentName sql.NullString
	err := b.db.QueryRow("SELECT agent_name FROM peers WHERE id = ?", req.SessionID).Scan(&currentName)
	if err != nil {
		return ClaimAgentResponse{OK: false, Error: fmt.Sprintf("session %s not found", req.SessionID)}
	}

	// A session can only claim once. Re-claim or rename is intentionally rejected.
	if currentName.String != "" {
		return ClaimAgentResponse{
			OK:    false,
			Error: fmt.Sprintf("session %s already has agent name %q -- cannot re-claim", req.SessionID, currentName.String),
		}
	}

	// Uniqueness check: is the target name held by another live session?
	var held Peer
	var gitRoot, tty sql.NullString
	err = b.db.QueryRow(
		`SELECT id, machine, cwd, git_root, tty, started_at
		 FROM peers WHERE agent_name = ? LIMIT 1`,
		req.AgentName,
	).Scan(&held.ID, &held.Machine, &held.CWD, &gitRoot, &tty, &held.RegisteredAt)
	if err == nil {
		return ClaimAgentResponse{
			OK:            false,
			Error:         fmt.Sprintf("agent %q already held by session %s", req.AgentName, held.ID),
			HeldBySession: held.ID,
			HeldByMachine: held.Machine,
			HeldByCWD:     held.CWD,
			HeldBySince:   held.RegisteredAt,
		}
	}

	// Claim the name.
	b.db.Exec("UPDATE peers SET agent_name = ? WHERE id = ?", req.AgentName, req.SessionID)

	// Drain any queued messages for this agent name.
	b.db.Exec(
		`UPDATE messages SET to_session = ? WHERE to_agent = ? AND to_session = '' AND delivered_at IS NULL`,
		req.SessionID, req.AgentName,
	)

	b.emitEvent("agent_claimed", req.SessionID, "", req.AgentName)
	b.nats.publish("fleet.agent.claimed", FleetEvent{
		Type: "agent_claimed", PeerID: req.SessionID, Data: req.AgentName,
	})
	return ClaimAgentResponse{OK: true}
}

// ackMessage confirms a client successfully received a message. Only after
// ack is the message permanently marked as delivered (ack_at set).
func (b *Broker) ackMessage(req AckMessageRequest) {
	b.db.Exec(
		`UPDATE messages SET ack_at = ?, ack_session = ? WHERE id = ? AND to_session = ?`,
		nowISO(), req.SessionID, req.MessageID, req.SessionID,
	)
}

func (b *Broker) unregister(req UnregisterRequest) {
	b.emitEvent("peer_left", req.ID, "", "")
	b.nats.publish("fleet.peer.left", FleetEvent{
		Type: "peer_left", PeerID: req.ID,
	})
	// Delete session. Agent name frees immediately.
	b.db.Exec("DELETE FROM peers WHERE id = ?", req.ID)
	// Drop direct-to-session messages (no agent queue to keep them alive).
	b.db.Exec("DELETE FROM messages WHERE to_session = ? AND to_agent = '' AND ack_at IS NULL", req.ID)
	// For agent-queued messages delivered to this session but not yet acked,
	// reset them so the next holder of the agent sees them.
	b.db.Exec(
		`UPDATE messages SET to_session = '', delivered_at = NULL
		 WHERE to_session = ? AND to_agent != '' AND ack_at IS NULL`,
		req.ID,
	)
}

func (b *Broker) setFleetMemory(content string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fleetMemory = content
	b.db.Exec("INSERT OR REPLACE INTO kv (key, value) VALUES ('fleet_memory', ?)", content)
}

func (b *Broker) getFleetMemory() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.fleetMemory
}

func (b *Broker) peerCount() int {
	var count int
	b.db.QueryRow("SELECT COUNT(*) FROM peers").Scan(&count)
	return count
}

func decodeBody[T any](r *http.Request) (T, error) {
	var v T
	err := json.NewDecoder(r.Body).Decode(&v)
	return v, err
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// stripPort removes the port suffix from a host:port address.
func stripPort(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// shortKID returns a short prefix of a peer pubkey string for scannable
// logs. Full pubkeys are 64+ chars; tailing journalctl should never leak
// complete audience identities in plain text.
func shortKID(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}

func runBroker(ctx context.Context) error {
	b, err := newBroker()
	if err != nil {
		return fmt.Errorf("init broker: %w", err)
	}
	defer b.db.Close()

	// Rate limiters: 10 req/min for send-message, 5 req/min for register.
	sendRL := newRateLimiter(10, time.Minute)
	registerRL := newRateLimiter(5, time.Minute)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, HealthResponse{Status: "ok", Peers: b.peerCount(), Machine: cfg.MachineName})
	})

	mux.HandleFunc("POST /challenge", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[ChallengeRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		kp, err := LoadKeyPair(configDir())
		if err != nil {
			http.Error(w, "broker has no keypair", 500)
			return
		}
		sig := ed25519.Sign(kp.PrivateKey, []byte(req.Nonce))
		log.Printf("[challenge] ip=%s", stripPort(r.RemoteAddr))
		writeJSON(w, ChallengeResponse{
			Nonce:     req.Nonce,
			Signature: base64.RawURLEncoding.EncodeToString(sig),
			PublicKey: pubKeyToString(kp.PublicKey),
		})
	})

	mux.HandleFunc("POST /refresh-token", func(w http.ResponseWriter, r *http.Request) {
		// Every branch of this handler logs. Before T3+log-patch the
		// handler was completely silent (middleware bypass at
		// auth_middleware.go plus zero log.Printf here) which left
		// refresh-token flows unobservable in journalctl.
		ip := stripPort(r.RemoteAddr)

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			log.Printf("[refresh] status=denied ip=%s reason=no_auth_header", ip)
			writeAuthError(w, http.StatusUnauthorized, "missing authorization header", "NO_AUTH")
			return
		}
		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenStr == authHeader {
			log.Printf("[refresh] status=denied ip=%s reason=no_bearer_prefix", ip)
			writeAuthError(w, http.StatusUnauthorized, "missing bearer token", "NO_AUTH")
			return
		}

		if b.validator == nil {
			log.Printf("[refresh] status=error ip=%s reason=no_validator", ip)
			http.Error(w, "broker has no validator", 500)
			return
		}

		// Accept tokens that are valid OR expired within a 1-hour grace window.
		claims, err := b.validator.Validate(tokenStr)
		if err != nil {
			claims, err = b.validator.ValidateWithGrace(tokenStr, time.Hour)
			if err != nil {
				log.Printf("[refresh] status=denied ip=%s reason=token_expired_beyond_grace err=%v", ip, err)
				writeAuthError(w, http.StatusUnauthorized, err.Error(), "TOKEN_EXPIRED")
				return
			}
		}

		// Load broker keypair to mint a new delegated token.
		kp, err := LoadKeyPair(configDir())
		if err != nil {
			log.Printf("[refresh] status=error ip=%s reason=no_keypair err=%v", ip, err)
			http.Error(w, "broker has no keypair", 500)
			return
		}

		// The token's audience is the intended recipient (the machine's public key).
		if len(claims.Audience) == 0 {
			log.Printf("[refresh] status=denied ip=%s reason=no_audience", ip)
			writeAuthError(w, http.StatusBadRequest, "token has no audience", "INVALID_TOKEN")
			return
		}
		aud := shortKID(claims.Audience[0])
		audiencePub, err := pubKeyFromString(claims.Audience[0])
		if err != nil {
			log.Printf("[refresh] status=denied aud=%s ip=%s reason=bad_audience_key", aud, ip)
			writeAuthError(w, http.StatusBadRequest, "invalid audience key", "INVALID_TOKEN")
			return
		}
		// Refuse to refresh a root token (issuer == audience).
		issuerPub, err := pubKeyFromString(claims.Issuer)
		if err != nil {
			log.Printf("[refresh] status=denied aud=%s ip=%s reason=bad_issuer_key", aud, ip)
			writeAuthError(w, http.StatusBadRequest, "invalid issuer key", "INVALID_TOKEN")
			return
		}
		if issuerPub.Equal(audiencePub) {
			log.Printf("[refresh] status=denied aud=%s ip=%s reason=root_token_refused", aud, ip)
			writeAuthError(w, http.StatusForbidden, "root tokens cannot be refreshed via this endpoint", "ROOT_TOKEN")
			return
		}

		parentToken, err := loadBrokerRootToken(configDir())
		if err != nil {
			log.Printf("[refresh] status=error aud=%s ip=%s reason=no_parent_root err=%v", aud, ip, err)
			http.Error(w, "broker root token unavailable", 500)
			return
		}

		// Use the broker-configured default TTL so a 30d-issued fleet
		// stays 30d after refresh. Before T3 this was hardcoded to 24h,
		// which meant every refresh dropped peers back onto the 24h
		// rotation treadmill even if they started with a long token.
		ttl := defaultChildTTL()
		newToken, err := MintToken(kp.PrivateKey, audiencePub, claims.Capabilities, ttl, parentToken)
		if err != nil {
			log.Printf("[refresh] status=error aud=%s ip=%s reason=mint_failed err=%v", aud, ip, err)
			http.Error(w, fmt.Sprintf("mint token: %v", err), 500)
			return
		}

		// Register the new token in the validator so it's immediately usable.
		b.validator.RegisterToken(newToken, claims.Capabilities)

		newExp := time.Now().Add(ttl).UTC().Format(time.RFC3339)
		log.Printf("[refresh] status=granted aud=%s ip=%s ttl=%s new_exp=%s", aud, ip, ttl, newExp)
		writeJSON(w, map[string]string{"token": newToken})
	})

	mux.HandleFunc("POST /register", requireCapability("peer/register", func(w http.ResponseWriter, r *http.Request) {
		if !registerRL.allow(stripPort(r.RemoteAddr)) {
			writeRateLimited(w)
			return
		}
		req, err := decodeBody[RegisterRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		resp := b.register(req)
		if !resp.OK {
			// Agent-name collision -- return 409 with the conflict block as JSON.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(resp)
			return
		}
		writeJSON(w, resp)
	}))

	mux.HandleFunc("POST /heartbeat", requireCapability("peer/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[HeartbeatRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b.heartbeat(req)
		writeJSON(w, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("POST /set-summary", requireCapability("peer/set-summary", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[SetSummaryRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b.setSummary(req)
		writeJSON(w, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("POST /ack-message", requireCapability("msg/ack", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[AckMessageRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b.ackMessage(req)
		writeJSON(w, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("POST /claim-agent", requireCapability("peer/set-summary", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[ClaimAgentRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		resp := b.claimAgent(req)
		if !resp.OK {
			w.Header().Set("Content-Type", "application/json")
			// 409 if collision, 400 if already-claimed or missing name.
			if resp.HeldBySession != "" {
				w.WriteHeader(http.StatusConflict)
			} else {
				w.WriteHeader(http.StatusBadRequest)
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		writeJSON(w, resp)
	}))

	mux.HandleFunc("POST /list-peers", requireCapability("peer/list", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[ListPeersRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, b.listPeers(req))
	}))

	mux.HandleFunc("POST /send-message", requireCapability("msg/send", func(w http.ResponseWriter, r *http.Request) {
		if !sendRL.allow(stripPort(r.RemoteAddr)) {
			writeRateLimited(w)
			return
		}
		req, err := decodeBody[SendMessageRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}

		// Sender verification: log the verified identity.
		claims := claimsFromContext(r.Context())
		if claims != nil && len(claims.Audience) > 0 {
			peerIdentity := claims.Audience[0]
			if req.FromID == "" {
				http.Error(w, "from_id is required", 400)
				return
			}
			sourceIP := stripPort(r.RemoteAddr)
			log.Printf("[broker] send-message: from_id=%s token_audience=%s source_ip=%s", req.FromID, peerIdentity, sourceIP)
		}

		writeJSON(w, b.sendMessage(req))
	}))

	mux.HandleFunc("POST /poll-messages", requireCapability("msg/poll", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[PollMessagesRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, b.pollMessages(req))
	}))

	mux.HandleFunc("POST /peek-messages", requireCapability("msg/poll", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[PollMessagesRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, b.peekMessages(req))
	}))


	mux.HandleFunc("POST /unregister", requireCapability("peer/unregister", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[UnregisterRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b.unregister(req)
		writeJSON(w, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("GET /events", requireCapability("events/read", func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			fmt.Sscanf(v, "%d", &limit)
		}
		writeJSON(w, b.recentEvents(limit))
	}))

	mux.HandleFunc("GET /fleet-memory", requireCapability("memory/read", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		w.Write([]byte(b.getFleetMemory()))
	}))

	mux.HandleFunc("POST /fleet-memory", requireCapability("memory/write", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b.setFleetMemory(string(data))
		writeJSON(w, map[string]bool{"ok": true})
	}))

	addr := cfg.Listen
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	srv := &http.Server{Handler: ucanMiddleware(b.validator)(mux)}

	log.Printf("[claude-peers broker] listening on %s (db: %s, machine: %s)", addr, cfg.DBPath, cfg.MachineName)

	context.AfterFunc(ctx, func() {
		srv.Shutdown(context.Background())
	})

	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return err
	}

	return nil
}
