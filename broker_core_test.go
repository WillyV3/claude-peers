package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testBroker(t *testing.T) *Broker {
	t.Helper()
	dir := t.TempDir()
	cfg.DBPath = filepath.Join(dir, "test.db")
	cfg.StaleTimeout = 300
	b, err := newBroker()
	if err != nil {
		t.Fatal(err)
	}
	// Create a test validator with a fresh root key.
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	b.validator = NewTokenValidator(kp.PublicKey)
	rootToken, err := MintRootToken(kp.PrivateKey, AllCapabilities(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b.validator.RegisterToken(rootToken, AllCapabilities())
	t.Cleanup(func() { b.db.Close() })
	return b
}

// mustRegister registers a session and fails the test on conflict.
func mustRegister(t *testing.T, b *Broker, req RegisterRequest) string {
	t.Helper()
	resp := b.register(req)
	if !resp.OK {
		t.Fatalf("register failed: %s (held by %s)", resp.Error, resp.HeldBySession)
	}
	return resp.ID
}

func TestRegisterAndList(t *testing.T) {
	b := testBroker(t)

	id := mustRegister(t, b, RegisterRequest{
		PID: os.Getpid(), Machine: "test-machine",
		CWD: "/tmp", Summary: "testing",
	})
	if id == "" {
		t.Fatal("expected session ID")
	}

	peers := b.listPeers(ListPeersRequest{Scope: "all"})
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	if peers[0].Machine != "test-machine" {
		t.Fatalf("expected machine test-machine, got %s", peers[0].Machine)
	}
	if peers[0].Summary != "testing" {
		t.Fatalf("expected summary testing, got %s", peers[0].Summary)
	}
}

func TestSendToSessionAndPoll(t *testing.T) {
	b := testBroker(t)

	s1 := mustRegister(t, b, RegisterRequest{PID: 1, Machine: "m1", CWD: "/a"})
	s2 := mustRegister(t, b, RegisterRequest{PID: 2, Machine: "m2", CWD: "/b"})

	resp := b.sendMessage(SendMessageRequest{FromID: s1, ToSession: s2, Text: "hello"})
	if !resp.OK {
		t.Fatalf("send failed: %s", resp.Error)
	}

	// Peek should return without marking delivered.
	peek := b.peekMessages(PollMessagesRequest{ID: s2})
	if len(peek.Messages) != 1 {
		t.Fatalf("expected 1 peeked message, got %d", len(peek.Messages))
	}

	// Poll marks delivered_at (but not ack_at), so a subsequent poll on the
	// same session returns empty.
	poll := b.pollMessages(PollMessagesRequest{ID: s2})
	if len(poll.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(poll.Messages))
	}
	if poll.Messages[0].Text != "hello" {
		t.Fatalf("expected hello, got %s", poll.Messages[0].Text)
	}

	poll2 := b.pollMessages(PollMessagesRequest{ID: s2})
	if len(poll2.Messages) != 0 {
		t.Fatalf("expected 0 messages after poll, got %d", len(poll2.Messages))
	}
}

func TestSendToNonexistentSession(t *testing.T) {
	b := testBroker(t)

	// Register the sender so FromID is valid (though broker doesn't enforce that).
	s1 := mustRegister(t, b, RegisterRequest{PID: 1, Machine: "m1", CWD: "/a"})
	resp := b.sendMessage(SendMessageRequest{FromID: s1, ToSession: "nonexistent", Text: "hi"})
	if resp.OK {
		t.Fatal("expected send to fail for nonexistent session")
	}
}

func TestSetSummary(t *testing.T) {
	b := testBroker(t)

	id := mustRegister(t, b, RegisterRequest{PID: 1, Machine: "m1", CWD: "/a", Summary: "old"})
	b.setSummary(SetSummaryRequest{ID: id, Summary: "new"})

	peers := b.listPeers(ListPeersRequest{Scope: "all"})
	if peers[0].Summary != "new" {
		t.Fatalf("expected summary new, got %s", peers[0].Summary)
	}
}

func TestListPeersByScope(t *testing.T) {
	b := testBroker(t)

	mustRegister(t, b, RegisterRequest{PID: 1, Machine: "m1", CWD: "/project-a", GitRoot: "/project-a"})
	mustRegister(t, b, RegisterRequest{PID: 2, Machine: "m1", CWD: "/project-b", GitRoot: "/project-b"})
	mustRegister(t, b, RegisterRequest{PID: 3, Machine: "m2", CWD: "/project-a", GitRoot: "/project-a"})

	all := b.listPeers(ListPeersRequest{Scope: "all"})
	if len(all) != 3 {
		t.Fatalf("expected 3 peers for all, got %d", len(all))
	}

	machine := b.listPeers(ListPeersRequest{Scope: "machine", Machine: "m1"})
	if len(machine) != 2 {
		t.Fatalf("expected 2 peers for machine m1, got %d", len(machine))
	}

	repo := b.listPeers(ListPeersRequest{Scope: "repo", GitRoot: "/project-a"})
	if len(repo) != 2 {
		t.Fatalf("expected 2 peers for repo /project-a, got %d", len(repo))
	}

	dir := b.listPeers(ListPeersRequest{Scope: "directory", CWD: "/project-b"})
	if len(dir) != 1 {
		t.Fatalf("expected 1 peer for dir /project-b, got %d", len(dir))
	}
}

func TestFleetMemory(t *testing.T) {
	b := testBroker(t)

	b.setFleetMemory("# Fleet Status\nAll good.")
	got := b.getFleetMemory()
	if got != "# Fleet Status\nAll good." {
		t.Fatalf("expected fleet memory content, got %q", got)
	}
}

func TestEvents(t *testing.T) {
	b := testBroker(t)

	mustRegister(t, b, RegisterRequest{PID: 1, Machine: "m1", CWD: "/a"})
	events := b.recentEvents(10)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Type != "peer_joined" {
		t.Fatalf("expected peer_joined, got %s", events[0].Type)
	}
}

func TestCleanStalePeers(t *testing.T) {
	b := testBroker(t)
	cfg.StaleTimeout = 1

	id := mustRegister(t, b, RegisterRequest{PID: 1, Machine: "m1", CWD: "/a"})
	if id == "" {
		t.Fatal("expected session ID")
	}

	b.db.Exec("UPDATE peers SET last_seen = ?", "2020-01-01T00:00:00Z")
	b.cleanStalePeers()

	peers := b.listPeers(ListPeersRequest{Scope: "all"})
	if len(peers) != 0 {
		t.Fatalf("expected 0 peers after stale cleanup, got %d", len(peers))
	}
}

func TestCleanStalePeersKeepsFresh(t *testing.T) {
	b := testBroker(t)
	cfg.StaleTimeout = 300

	mustRegister(t, b, RegisterRequest{PID: 1, Machine: "m1", CWD: "/a"})
	b.cleanStalePeers()

	peers := b.listPeers(ListPeersRequest{Scope: "all"})
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer (fresh), got %d", len(peers))
	}
}

func TestHeartbeat(t *testing.T) {
	b := testBroker(t)

	id := mustRegister(t, b, RegisterRequest{PID: 1, Machine: "m1", CWD: "/a"})

	b.db.Exec("UPDATE peers SET last_seen = ?", "2020-01-01T00:00:00Z")
	b.heartbeat(HeartbeatRequest{ID: id})

	peers := b.listPeers(ListPeersRequest{Scope: "all"})
	if peers[0].LastSeen == "2020-01-01T00:00:00Z" {
		t.Fatal("heartbeat did not update last_seen")
	}
}

func TestPeerCount(t *testing.T) {
	b := testBroker(t)

	if c := b.peerCount(); c != 0 {
		t.Fatalf("expected 0 peers initially, got %d", c)
	}

	mustRegister(t, b, RegisterRequest{PID: 1, Machine: "m1", CWD: "/a"})
	mustRegister(t, b, RegisterRequest{PID: 2, Machine: "m2", CWD: "/b"})
	mustRegister(t, b, RegisterRequest{PID: 3, Machine: "m3", CWD: "/c"})

	if c := b.peerCount(); c != 3 {
		t.Fatalf("expected 3 peers, got %d", c)
	}
}

func TestGeneratePeerID(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := generatePeerID()
		if id == "" {
			t.Fatal("generated empty peer ID")
		}
		if len(id) != 8 {
			t.Fatalf("expected 8 char hex ID, got %q (len %d)", id, len(id))
		}
		if seen[id] {
			t.Fatalf("duplicate peer ID: %s", id)
		}
		seen[id] = true
	}
}

func TestNowISO(t *testing.T) {
	ts := nowISO()
	if ts == "" {
		t.Fatal("nowISO returned empty string")
	}
	if len(ts) < 20 {
		t.Fatalf("nowISO returned suspiciously short timestamp: %s", ts)
	}
}

func TestListPeersExcludeID(t *testing.T) {
	b := testBroker(t)

	s1 := mustRegister(t, b, RegisterRequest{PID: 1, Machine: "m1", CWD: "/a"})
	mustRegister(t, b, RegisterRequest{PID: 2, Machine: "m2", CWD: "/b"})

	peers := b.listPeers(ListPeersRequest{Scope: "all", ExcludeID: s1})
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer after excluding, got %d", len(peers))
	}
	if peers[0].ID == s1 {
		t.Fatal("excluded peer should not appear")
	}
}

func TestRecentEventsEmpty(t *testing.T) {
	b := testBroker(t)
	events := b.recentEvents(10)
	if events == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}
