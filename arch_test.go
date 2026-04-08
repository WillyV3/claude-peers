package main

// ADR-001 acceptance tests: the 10 scenarios from docs/ADR-001-agent-session-split.md.
// These define the contract for the Agent/Session architecture. They run against
// the broker at the unit level using an in-memory SQLite database (via testBroker).
//
// If any of these fail, the architecture is broken -- not the code calling into it.

import (
	"testing"
	"time"
)

// T1: Two named agents exchange messages, both sides receive and ack.
func TestT1_NamedAgentsExchangeMessages(t *testing.T) {
	b := testBroker(t)

	alice := mustRegister(t, b, RegisterRequest{AgentName: "alice", PID: 1, Machine: "m1", CWD: "/a"})
	bob := mustRegister(t, b, RegisterRequest{AgentName: "bob", PID: 2, Machine: "m2", CWD: "/b"})

	// Alice -> Bob
	r1 := b.sendMessage(SendMessageRequest{FromID: alice, ToAgent: "bob", Text: "hi bob"})
	if !r1.OK || r1.Queued {
		t.Fatalf("alice -> bob should deliver immediately, got OK=%v queued=%v err=%s", r1.OK, r1.Queued, r1.Error)
	}

	// Bob polls, finds message, ack.
	poll := b.pollMessages(PollMessagesRequest{ID: bob})
	if len(poll.Messages) != 1 || poll.Messages[0].Text != "hi bob" {
		t.Fatalf("bob should see 1 message, got %+v", poll.Messages)
	}
	if poll.Messages[0].FromAgent != "alice" {
		t.Fatalf("message from_agent should be 'alice', got %q", poll.Messages[0].FromAgent)
	}
	b.ackMessage(AckMessageRequest{SessionID: bob, MessageID: poll.Messages[0].ID})

	// Bob -> Alice
	r2 := b.sendMessage(SendMessageRequest{FromID: bob, ToAgent: "alice", Text: "hi alice"})
	if !r2.OK || r2.Queued {
		t.Fatalf("bob -> alice should deliver immediately, got %+v", r2)
	}

	poll2 := b.pollMessages(PollMessagesRequest{ID: alice})
	if len(poll2.Messages) != 1 || poll2.Messages[0].Text != "hi alice" {
		t.Fatalf("alice should see 1 message, got %+v", poll2.Messages)
	}
	b.ackMessage(AckMessageRequest{SessionID: alice, MessageID: poll2.Messages[0].ID})
}

// T2: Message to offline agent -> queues -> delivers on reconnect.
func TestT2_OfflineAgentQueuesThenDrainsOnReconnect(t *testing.T) {
	b := testBroker(t)

	alice := mustRegister(t, b, RegisterRequest{AgentName: "alice", PID: 1, Machine: "m1", CWD: "/a"})

	// Send to "bob" -- no session holds it.
	r := b.sendMessage(SendMessageRequest{FromID: alice, ToAgent: "bob", Text: "queued hi"})
	if !r.OK {
		t.Fatalf("send should succeed, got err=%s", r.Error)
	}
	if !r.Queued {
		t.Fatal("send to offline agent should be marked Queued=true")
	}

	// Bob now comes online.
	bob := mustRegister(t, b, RegisterRequest{AgentName: "bob", PID: 2, Machine: "m2", CWD: "/b"})

	// Bob polls and sees the queued message.
	poll := b.pollMessages(PollMessagesRequest{ID: bob})
	if len(poll.Messages) != 1 || poll.Messages[0].Text != "queued hi" {
		t.Fatalf("bob should receive queued message on reconnect, got %+v", poll.Messages)
	}
	b.ackMessage(AckMessageRequest{SessionID: bob, MessageID: poll.Messages[0].ID})
}

// T3: Name collision -- second register with held agent name returns error with conflict block.
func TestT3_NameCollisionHardFail(t *testing.T) {
	b := testBroker(t)

	first := b.register(RegisterRequest{
		AgentName: "caretaker", PID: 1, Machine: "m1",
		CWD: "/home/willy/projects/caretaker-repo",
	})
	if !first.OK {
		t.Fatalf("first register should succeed, got err=%s", first.Error)
	}

	// Second registration with the same agent name must fail.
	second := b.register(RegisterRequest{
		AgentName: "caretaker", PID: 2, Machine: "m2",
		CWD: "/somewhere/else",
	})
	if second.OK {
		t.Fatal("second register with held agent name should fail")
	}
	if second.Error == "" {
		t.Fatal("conflict error should be populated")
	}
	if second.HeldBySession != first.ID {
		t.Fatalf("HeldBySession should point to first session %s, got %s", first.ID, second.HeldBySession)
	}
	if second.HeldByMachine != "m1" {
		t.Fatalf("HeldByMachine should be m1, got %s", second.HeldByMachine)
	}
	if second.HeldByCWD != "/home/willy/projects/caretaker-repo" {
		t.Fatalf("HeldByCWD should be the first session's CWD, got %s", second.HeldByCWD)
	}
	if second.HeldBySince == "" {
		t.Fatal("HeldBySince should be populated")
	}
}

// T4: Ephemeral session cannot be messaged by agent name.
func TestT4_EphemeralSessionNotAddressableByName(t *testing.T) {
	b := testBroker(t)

	// Session registers with no agent name -- it's ephemeral.
	sender := mustRegister(t, b, RegisterRequest{AgentName: "sender", PID: 1, Machine: "m1", CWD: "/a"})
	ephemeral := mustRegister(t, b, RegisterRequest{PID: 2, Machine: "m2", CWD: "/b"})

	// Sending to agent "whatever" that nobody holds: queues (not an error on its own).
	r := b.sendMessage(SendMessageRequest{FromID: sender, ToAgent: "nobody", Text: "void"})
	if !r.OK || !r.Queued {
		t.Fatalf("send to unheld agent should queue, got OK=%v queued=%v", r.OK, r.Queued)
	}
	// Ephemeral session polling does NOT see queued messages for "nobody" -- it's not its agent.
	poll := b.pollMessages(PollMessagesRequest{ID: ephemeral})
	if len(poll.Messages) != 0 {
		t.Fatalf("ephemeral session should not receive messages addressed to agent names it doesn't hold, got %d", len(poll.Messages))
	}

	// But we CAN directly target the ephemeral session by session ID.
	r2 := b.sendMessage(SendMessageRequest{FromID: sender, ToSession: ephemeral, Text: "direct"})
	if !r2.OK || r2.Queued {
		t.Fatalf("direct-to-session send should deliver immediately, got %+v", r2)
	}
	poll2 := b.pollMessages(PollMessagesRequest{ID: ephemeral})
	if len(poll2.Messages) != 1 || poll2.Messages[0].Text != "direct" {
		t.Fatalf("ephemeral session should receive direct-to-session message, got %+v", poll2.Messages)
	}
}

// T5: Dirty death -- stale sweep frees agent name, new session can claim it.
func TestT5_DirtyDeathFreesAgentName(t *testing.T) {
	b := testBroker(t)
	cfg.StaleTimeout = 1

	first := mustRegister(t, b, RegisterRequest{AgentName: "caretaker", PID: 1, Machine: "m1", CWD: "/a"})
	_ = first

	// Simulate crash by forcing last_seen into the distant past.
	b.db.Exec("UPDATE peers SET last_seen = ? WHERE id = ?", "2020-01-01T00:00:00Z", first)

	// Stale sweep runs -- name should free.
	b.cleanStalePeers()

	// New session can now claim "caretaker".
	second := b.register(RegisterRequest{AgentName: "caretaker", PID: 2, Machine: "m2", CWD: "/b"})
	if !second.OK {
		t.Fatalf("new session should claim freed agent name, got err=%s", second.Error)
	}
}

// T6: Unacked messages remain in the queue after a dirty session death.
// The ack protocol is: poll() sets delivered_at; ack() sets ack_at. A message
// that was delivered but not acked, when the holding session dies, gets reset
// back to the agent queue for the next holder.
func TestT6_UnackedMessageReturnsToQueueOnSessionDeath(t *testing.T) {
	b := testBroker(t)
	cfg.StaleTimeout = 1

	sender := mustRegister(t, b, RegisterRequest{AgentName: "sender", PID: 1, Machine: "m1", CWD: "/a"})
	first := mustRegister(t, b, RegisterRequest{AgentName: "worker", PID: 2, Machine: "m2", CWD: "/b"})

	b.sendMessage(SendMessageRequest{FromID: sender, ToAgent: "worker", Text: "do the thing"})

	// First worker polls (delivered_at set) but crashes before acking.
	poll := b.pollMessages(PollMessagesRequest{ID: first})
	if len(poll.Messages) != 1 {
		t.Fatalf("first worker should receive 1 message, got %d", len(poll.Messages))
	}

	// Simulate dirty death: stale cleanup runs.
	b.db.Exec("UPDATE peers SET last_seen = ? WHERE id = ?", "2020-01-01T00:00:00Z", first)
	b.cleanStalePeers()

	// New worker takes over the agent name.
	second := mustRegister(t, b, RegisterRequest{AgentName: "worker", PID: 3, Machine: "m3", CWD: "/c"})

	// Second worker should receive the unacked message.
	poll2 := b.pollMessages(PollMessagesRequest{ID: second})
	if len(poll2.Messages) != 1 || poll2.Messages[0].Text != "do the thing" {
		t.Fatalf("second worker should receive the unacked message, got %+v", poll2.Messages)
	}
	b.ackMessage(AckMessageRequest{SessionID: second, MessageID: poll2.Messages[0].ID})
}

// T7: Queue TTL -- messages older than 24h get dead-lettered.
// We can't wait 24h in a test, so we insert a stale message directly and
// confirm the cleanup path would drop it. Tests the cleanup SQL directly.
func TestT7_QueueTTLDeadLetters(t *testing.T) {
	b := testBroker(t)

	sender := mustRegister(t, b, RegisterRequest{AgentName: "sender", PID: 1, Machine: "m1", CWD: "/a"})
	b.sendMessage(SendMessageRequest{FromID: sender, ToAgent: "never-comes", Text: "stale"})

	// Force the message's sent_at into the distant past.
	b.db.Exec("UPDATE messages SET sent_at = ?", "2020-01-01T00:00:00Z")

	// Run the cleanup query the background goroutine runs.
	b.db.Exec("DELETE FROM messages WHERE ack_at IS NULL AND sent_at < ?",
		time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339))

	// Agent "never-comes" comes online -- should see NO message, it was dead-lettered.
	nc := mustRegister(t, b, RegisterRequest{AgentName: "never-comes", PID: 2, Machine: "m2", CWD: "/b"})
	poll := b.pollMessages(PollMessagesRequest{ID: nc})
	if len(poll.Messages) != 0 {
		t.Fatalf("dead-lettered message should not be delivered, got %d", len(poll.Messages))
	}
}

// T8: Concurrent send to same agent -- all delivered in send order, no dupes.
func TestT8_ConcurrentSendsDeliveredInOrder(t *testing.T) {
	b := testBroker(t)

	sender := mustRegister(t, b, RegisterRequest{AgentName: "sender", PID: 1, Machine: "m1", CWD: "/a"})
	receiver := mustRegister(t, b, RegisterRequest{AgentName: "receiver", PID: 2, Machine: "m2", CWD: "/b"})

	for i := 0; i < 5; i++ {
		r := b.sendMessage(SendMessageRequest{
			FromID: sender, ToAgent: "receiver",
			Text: []string{"msg-0", "msg-1", "msg-2", "msg-3", "msg-4"}[i],
		})
		if !r.OK {
			t.Fatalf("send %d failed: %s", i, r.Error)
		}
		// Tiny sleep to guarantee distinct sent_at values.
		time.Sleep(2 * time.Millisecond)
	}

	poll := b.pollMessages(PollMessagesRequest{ID: receiver})
	if len(poll.Messages) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(poll.Messages))
	}
	for i, m := range poll.Messages {
		expected := []string{"msg-0", "msg-1", "msg-2", "msg-3", "msg-4"}[i]
		if m.Text != expected {
			t.Fatalf("message %d: expected %q, got %q", i, expected, m.Text)
		}
		b.ackMessage(AckMessageRequest{SessionID: receiver, MessageID: m.ID})
	}
}

// T9: Graceful unregister frees the agent name immediately.
func TestT9_GracefulUnregisterFreesName(t *testing.T) {
	b := testBroker(t)

	first := mustRegister(t, b, RegisterRequest{AgentName: "caretaker", PID: 1, Machine: "m1", CWD: "/a"})

	b.unregister(UnregisterRequest{ID: first})

	// Immediately claim the same agent name -- must succeed without waiting for stale sweep.
	second := b.register(RegisterRequest{AgentName: "caretaker", PID: 2, Machine: "m2", CWD: "/b"})
	if !second.OK {
		t.Fatalf("new session should claim name immediately after graceful unregister, got err=%s", second.Error)
	}
}

// T11: claim_agent_name -- an ephemeral session can adopt a name
// post-registration. Covers happy path, uniqueness enforcement, re-claim
// rejection, and queue drain on claim (same as register-time declaration).
func TestT11_ClaimAgentNameLateBinding(t *testing.T) {
	b := testBroker(t)

	// Two ephemeral sessions exist in the same "cwd" with no declared name.
	s1 := mustRegister(t, b, RegisterRequest{PID: 1, Machine: "m1", CWD: "/shared"})
	s2 := mustRegister(t, b, RegisterRequest{PID: 2, Machine: "m1", CWD: "/shared"})

	// An earlier sender queues a message for agent "worker" (before anyone claims it).
	sender := mustRegister(t, b, RegisterRequest{AgentName: "sender", PID: 3, Machine: "m2", CWD: "/a"})
	send := b.sendMessage(SendMessageRequest{FromID: sender, ToAgent: "worker", Text: "queued before claim"})
	if !send.OK || !send.Queued {
		t.Fatalf("pre-claim send should queue, got %+v", send)
	}

	// s1 claims "worker" -- should succeed and drain the queued message.
	claim1 := b.claimAgent(ClaimAgentRequest{SessionID: s1, AgentName: "worker"})
	if !claim1.OK {
		t.Fatalf("s1 claim should succeed, got err=%s", claim1.Error)
	}

	// s1 polls and finds the message that was queued before claim.
	poll := b.pollMessages(PollMessagesRequest{ID: s1})
	if len(poll.Messages) != 1 || poll.Messages[0].Text != "queued before claim" {
		t.Fatalf("s1 should drain queued message after claim, got %+v", poll.Messages)
	}

	// s2 tries to claim "worker" too -- collision, must return conflict block.
	claim2 := b.claimAgent(ClaimAgentRequest{SessionID: s2, AgentName: "worker"})
	if claim2.OK {
		t.Fatal("s2 claim of held name should fail")
	}
	if claim2.HeldBySession != s1 {
		t.Fatalf("HeldBySession should point to s1, got %s", claim2.HeldBySession)
	}

	// s2 claims "other" -- succeeds.
	claim3 := b.claimAgent(ClaimAgentRequest{SessionID: s2, AgentName: "other"})
	if !claim3.OK {
		t.Fatalf("s2 claim of 'other' should succeed, got err=%s", claim3.Error)
	}

	// s2 tries to re-claim (rename) -- rejected. Identity is explicit, no mutation.
	claim4 := b.claimAgent(ClaimAgentRequest{SessionID: s2, AgentName: "different"})
	if claim4.OK {
		t.Fatal("re-claim should be rejected; a session can only claim once")
	}

	// Claim with empty name: rejected.
	claim5 := b.claimAgent(ClaimAgentRequest{SessionID: s1, AgentName: ""})
	if claim5.OK {
		t.Fatal("empty name should be rejected")
	}

	// Claim for non-existent session: rejected.
	claim6 := b.claimAgent(ClaimAgentRequest{SessionID: "nonexistent", AgentName: "ghost"})
	if claim6.OK {
		t.Fatal("claim for nonexistent session should fail")
	}
}

// T10: Restart continuity -- agent "jim" sends, dies gracefully, reconnects,
// queued messages for him while away are delivered on the new session.
func TestT10_RestartContinuity(t *testing.T) {
	b := testBroker(t)

	sender := mustRegister(t, b, RegisterRequest{AgentName: "sender", PID: 1, Machine: "m1", CWD: "/a"})
	jim1 := mustRegister(t, b, RegisterRequest{AgentName: "jim", PID: 2, Machine: "m2", CWD: "/b"})

	// Jim goes away.
	b.unregister(UnregisterRequest{ID: jim1})

	// Sender sends to jim while he's away -- queues.
	r := b.sendMessage(SendMessageRequest{FromID: sender, ToAgent: "jim", Text: "welcome back"})
	if !r.OK || !r.Queued {
		t.Fatalf("send to offline jim should queue, got %+v", r)
	}

	// Jim comes back with a new session.
	jim2 := mustRegister(t, b, RegisterRequest{AgentName: "jim", PID: 3, Machine: "m2", CWD: "/b"})

	poll := b.pollMessages(PollMessagesRequest{ID: jim2})
	if len(poll.Messages) != 1 || poll.Messages[0].Text != "welcome back" {
		t.Fatalf("returning jim should see queued message, got %+v", poll.Messages)
	}
	b.ackMessage(AckMessageRequest{SessionID: jim2, MessageID: poll.Messages[0].ID})
}
