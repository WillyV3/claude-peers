package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSendMessageDeliveryStatus_Bound covers the case where the recipient
// agent name is currently held by a live session. Expect Queued=false and
// DeliveryStatus="bound".
func TestSendMessageDeliveryStatus_Bound(t *testing.T) {
	b := testBroker(t)

	sender := mustRegister(t, b, RegisterRequest{PID: 1, Machine: "m", CWD: "/s"})
	mustRegister(t, b, RegisterRequest{
		AgentName: "alice", PID: 2, Machine: "m", CWD: "/r",
	})

	resp := b.sendMessage(SendMessageRequest{FromID: sender, ToAgent: "alice", Text: "hi"})
	if !resp.OK {
		t.Fatalf("expected OK, got error %q", resp.Error)
	}
	if resp.Queued {
		t.Fatal("expected Queued=false (live session bound)")
	}
	if resp.DeliveryStatus != DeliveryStatusBound {
		t.Fatalf("expected DeliveryStatusBound, got %q", resp.DeliveryStatus)
	}
}

// TestSendMessageDeliveryStatus_QueuedOffline covers the case where the
// agent name has been claimed before but no session holds it now. Expect
// Queued=true, DeliveryStatus="queued_offline".
func TestSendMessageDeliveryStatus_QueuedOffline(t *testing.T) {
	b := testBroker(t)
	cfg.StaleTimeout = 1

	// alice registers, then goes stale.
	aliceID := mustRegister(t, b, RegisterRequest{
		AgentName: "alice", PID: 1, Machine: "m", CWD: "/r",
	})
	b.db.Exec("UPDATE peers SET last_seen = ? WHERE id = ?", "2020-01-01T00:00:00Z", aliceID)
	b.cleanStalePeers()

	sender := mustRegister(t, b, RegisterRequest{PID: 2, Machine: "m", CWD: "/s"})
	resp := b.sendMessage(SendMessageRequest{FromID: sender, ToAgent: "alice", Text: "hi"})
	if !resp.OK {
		t.Fatalf("expected OK, got error %q", resp.Error)
	}
	if !resp.Queued {
		t.Fatal("expected Queued=true (recipient stale)")
	}
	if resp.DeliveryStatus != DeliveryStatusQueuedOffline {
		t.Fatalf("expected DeliveryStatusQueuedOffline, got %q", resp.DeliveryStatus)
	}
}

// TestSendMessageDeliveryStatus_QueuedUnknown covers the typo case: no
// session has ever claimed this agent name. Expect Queued=true and
// DeliveryStatus="queued_unknown".
func TestSendMessageDeliveryStatus_QueuedUnknown(t *testing.T) {
	b := testBroker(t)

	sender := mustRegister(t, b, RegisterRequest{PID: 1, Machine: "m", CWD: "/s"})
	resp := b.sendMessage(SendMessageRequest{FromID: sender, ToAgent: "neverclaimed", Text: "hi"})
	if !resp.OK {
		t.Fatalf("expected OK, got error %q", resp.Error)
	}
	if !resp.Queued {
		t.Fatal("expected Queued=true (no live holder)")
	}
	if resp.DeliveryStatus != DeliveryStatusQueuedUnknown {
		t.Fatalf("expected DeliveryStatusQueuedUnknown, got %q", resp.DeliveryStatus)
	}
}

// TestSendMessageDeliveryStatus_ClaimAgentRecordsHistory verifies that names
// adopted via claim_agent_name (not just register) get into agent_names_seen.
// Same machinery: register without name, claim, go offline, send -> queued_offline.
func TestSendMessageDeliveryStatus_ClaimAgentRecordsHistory(t *testing.T) {
	b := testBroker(t)
	cfg.StaleTimeout = 1

	// Bob registers without an agent name, then claims one.
	bobID := mustRegister(t, b, RegisterRequest{PID: 1, Machine: "m", CWD: "/r"})
	claimResp := b.claimAgent(ClaimAgentRequest{SessionID: bobID, AgentName: "bob"})
	if !claimResp.OK {
		t.Fatalf("claim failed: %s", claimResp.Error)
	}

	// Bob goes stale.
	b.db.Exec("UPDATE peers SET last_seen = ? WHERE id = ?", "2020-01-01T00:00:00Z", bobID)
	b.cleanStalePeers()

	// Sending to bob should report QueuedOffline (history present), not Unknown.
	sender := mustRegister(t, b, RegisterRequest{PID: 2, Machine: "m", CWD: "/s"})
	resp := b.sendMessage(SendMessageRequest{FromID: sender, ToAgent: "bob", Text: "hi"})
	if resp.DeliveryStatus != DeliveryStatusQueuedOffline {
		t.Fatalf("expected DeliveryStatusQueuedOffline (claim history present), got %q", resp.DeliveryStatus)
	}
}

// TestSendMessageDeliveryStatus_BackfillFromMessages exercises the broker
// backfill path: a message addressed to an agent name that NEVER had a peer
// row (e.g. an old broker that received a typo'd send) should still be
// classified as "seen" once the broker restarts and the backfill runs from
// messages.to_agent. This guards against regressions in the upgrade path.
func TestSendMessageDeliveryStatus_BackfillFromMessages(t *testing.T) {
	b := testBroker(t)

	// Insert an orphan message directly so messages.to_agent has an entry
	// for "ghost", but no peer ever existed.
	b.db.Exec(
		`INSERT INTO messages (to_agent, to_session, from_session, from_agent, text, sent_at, attempts)
		 VALUES (?, '', ?, '', ?, ?, 0)`,
		"ghost", "fake-sender", "old message", nowISO(),
	)
	// Wipe agent_names_seen to simulate a pre-T11 broker that just upgraded.
	b.db.Exec("DELETE FROM agent_names_seen")

	// Re-run the backfill against the same DB. We invoke the same statement
	// newBroker uses on startup so the test is honest about behavior.
	b.db.Exec(`INSERT OR IGNORE INTO agent_names_seen (name, first_seen)
		SELECT to_agent, MIN(sent_at) FROM messages
		WHERE to_agent != '' GROUP BY to_agent`)

	// Now ghost should be considered "seen" -- a fresh send queues offline.
	sender := mustRegister(t, b, RegisterRequest{PID: 1, Machine: "m", CWD: "/s"})
	resp := b.sendMessage(SendMessageRequest{FromID: sender, ToAgent: "ghost", Text: "hello"})
	if resp.DeliveryStatus != DeliveryStatusQueuedOffline {
		t.Fatalf("expected DeliveryStatusQueuedOffline after backfill, got %q", resp.DeliveryStatus)
	}
}

// TestSendMessageDeliveryStatus_OldClientDecode verifies the wire format is
// backwards-compatible: an old client that doesn't know about
// DeliveryStatus must still be able to decode the response and read
// OK/Queued/MessageID correctly.
func TestSendMessageDeliveryStatus_OldClientDecode(t *testing.T) {
	resp := SendMessageResponse{
		OK:             true,
		MessageID:      42,
		Queued:         true,
		DeliveryStatus: DeliveryStatusQueuedUnknown,
	}
	wire, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	// Simulate an old client that has no DeliveryStatus field.
	type oldSendMessageResponse struct {
		OK        bool   `json:"ok"`
		MessageID int    `json:"message_id,omitempty"`
		Queued    bool   `json:"queued,omitempty"`
		Error     string `json:"error,omitempty"`
	}
	var old oldSendMessageResponse
	if err := json.Unmarshal(wire, &old); err != nil {
		t.Fatalf("old client failed to decode: %v", err)
	}
	if !old.OK {
		t.Fatal("old client lost OK")
	}
	if old.MessageID != 42 {
		t.Fatalf("old client lost MessageID: %d", old.MessageID)
	}
	if !old.Queued {
		t.Fatal("old client lost Queued")
	}
}

// TestSendMessageDeliveryStatus_NewClientDecodesOldBroker verifies the
// reverse: a new client decoding an old broker's response (no
// DeliveryStatus field) must see DeliveryStatus="" and rely on Queued. The
// MCP frontend's default branch handles this case.
func TestSendMessageDeliveryStatus_NewClientDecodesOldBroker(t *testing.T) {
	// Old broker wire format: ok + message_id + queued, no delivery_status.
	wire := []byte(`{"ok":true,"message_id":7,"queued":true}`)
	var resp SendMessageResponse
	if err := json.Unmarshal(wire, &resp); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if !resp.OK {
		t.Fatal("expected OK=true")
	}
	if !resp.Queued {
		t.Fatal("expected Queued=true")
	}
	if resp.DeliveryStatus != "" {
		t.Fatalf("expected empty DeliveryStatus from old broker wire, got %q", resp.DeliveryStatus)
	}
}

// TestHTTP_DeliveryStatus_AllThreeBranches exercises the full request path
// (HTTP, middleware, JSON codec, broker logic) for each DeliveryStatus
// branch. Catches regressions in the JSON wire format on every layer of the
// stack -- mirrors the architecture coverage in arch_http_test.go.
func TestHTTP_DeliveryStatus_AllThreeBranches(t *testing.T) {
	f := newBrokerHTTPFixture(t)

	// alice and bob register (live). carol is named but goes stale before the send.
	for _, agent := range []struct {
		name string
		pid  int
	}{{"alice-ds", 1}, {"bob-ds", 2}, {"carol-ds", 3}} {
		var reg RegisterResponse
		if s := f.postJSON(t, "/register",
			RegisterRequest{AgentName: agent.name, PID: agent.pid, Machine: "m", CWD: "/a"},
			&reg,
		); s != 200 || !reg.OK {
			t.Fatalf("register %s: status=%d resp=%+v", agent.name, s, reg)
		}
	}

	// Stale-sweep carol so her name is in agent_names_seen but no live peer holds it.
	cfg.StaleTimeout = 1
	f.broker.db.Exec("UPDATE peers SET last_seen = ? WHERE agent_name = ?", "2020-01-01T00:00:00Z", "carol-ds")
	f.broker.cleanStalePeers()

	// alice (sender) lookup
	var senderReg RegisterResponse
	f.postJSON(t, "/register",
		RegisterRequest{PID: 99, Machine: "m", CWD: "/sender"},
		&senderReg,
	)

	// Branch 1: bound (live agent)
	var boundResp SendMessageResponse
	f.postJSON(t, "/send-message",
		SendMessageRequest{FromID: senderReg.ID, ToAgent: "bob-ds", Text: "live"},
		&boundResp,
	)
	if !boundResp.OK || boundResp.Queued || boundResp.DeliveryStatus != DeliveryStatusBound {
		t.Fatalf("expected bound status, got %+v", boundResp)
	}

	// Branch 2: queued_offline (carol claimed before, now stale)
	var offlineResp SendMessageResponse
	f.postJSON(t, "/send-message",
		SendMessageRequest{FromID: senderReg.ID, ToAgent: "carol-ds", Text: "offline"},
		&offlineResp,
	)
	if !offlineResp.OK || !offlineResp.Queued || offlineResp.DeliveryStatus != DeliveryStatusQueuedOffline {
		t.Fatalf("expected queued_offline status, got %+v", offlineResp)
	}

	// Branch 3: queued_unknown (typo)
	var unknownResp SendMessageResponse
	f.postJSON(t, "/send-message",
		SendMessageRequest{FromID: senderReg.ID, ToAgent: "totally-not-an-agent", Text: "void"},
		&unknownResp,
	)
	if !unknownResp.OK || !unknownResp.Queued || unknownResp.DeliveryStatus != DeliveryStatusQueuedUnknown {
		t.Fatalf("expected queued_unknown status, got %+v", unknownResp)
	}
}

// TestDeliveryStatus_JSONRoundTrip pins down the wire-format constants so a
// rename or typo in the named-type values trips a test rather than rolling
// silently across the fleet.
func TestDeliveryStatus_JSONRoundTrip(t *testing.T) {
	cases := []struct {
		status  DeliveryStatus
		jsonVal string
	}{
		{DeliveryStatusBound, "bound"},
		{DeliveryStatusQueuedOffline, "queued_offline"},
		{DeliveryStatusQueuedUnknown, "queued_unknown"},
	}
	for _, c := range cases {
		resp := SendMessageResponse{OK: true, DeliveryStatus: c.status}
		wire, _ := json.Marshal(resp)
		if !strings.Contains(string(wire), `"delivery_status":"`+c.jsonVal+`"`) {
			t.Fatalf("expected wire to contain delivery_status=%q, got %s", c.jsonVal, wire)
		}
	}
}
