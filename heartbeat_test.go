package main

import (
	"os"
	"testing"
)

// T10: the broker's heartbeat endpoint distinguishes "known session, row
// updated" from "unknown session, row missing". Pre-T10 the UPDATE silently
// no-op'd on missing rows, so a client whose peer row had been stale-swept
// during a broker restart kept heartbeating into void forever. The OK flag
// + Reason field let the client detect eviction and re-register.

func TestHeartbeat_KnownSessionReturnsOK(t *testing.T) {
	b := testBroker(t)

	id := mustRegister(t, b, RegisterRequest{
		PID:     os.Getpid(),
		Machine: "test",
		CWD:     "/tmp/heartbeat-known",
		Summary: "heartbeat-test",
	})

	resp := b.heartbeat(HeartbeatRequest{ID: id})
	if !resp.OK {
		t.Fatalf("expected OK for known session %s, got %+v", id, resp)
	}
	if resp.Reason != "" {
		t.Fatalf("expected empty Reason for OK response, got %q", resp.Reason)
	}
}

func TestHeartbeat_UnknownSessionReportsEviction(t *testing.T) {
	// This is the scenario that prompted T10. A client has a cached
	// session_id from before a broker restart or a sweep; the matching
	// peer row no longer exists. The heartbeat response must surface
	// that clearly so the client can rebind.
	b := testBroker(t)

	resp := b.heartbeat(HeartbeatRequest{ID: "bogus-never-registered"})
	if resp.OK {
		t.Fatal("expected OK=false for unknown session id")
	}
	if resp.Reason != HeartbeatReasonUnknownSession {
		t.Fatalf("expected reason=%q, got %q", HeartbeatReasonUnknownSession, resp.Reason)
	}
}

func TestHeartbeat_AfterUnregisterReportsEviction(t *testing.T) {
	// Simulates the real failure mode: the session existed, the broker
	// restarted, stale_sweep evicted the row. From the client's POV the
	// session_id is still good; from the broker's POV there's no row.
	// Heartbeat must return unknown_session so the client rebinds.
	b := testBroker(t)

	id := mustRegister(t, b, RegisterRequest{
		PID: os.Getpid(), Machine: "test", CWD: "/tmp/heartbeat-evict",
	})
	// Confirm baseline: heartbeat works while the row is live.
	if got := b.heartbeat(HeartbeatRequest{ID: id}); !got.OK {
		t.Fatalf("baseline heartbeat failed: %+v", got)
	}

	b.unregister(UnregisterRequest{ID: id})

	got := b.heartbeat(HeartbeatRequest{ID: id})
	if got.OK {
		t.Fatalf("heartbeat should report eviction after unregister, got %+v", got)
	}
	if got.Reason != HeartbeatReasonUnknownSession {
		t.Fatalf("expected reason=%q, got %q", HeartbeatReasonUnknownSession, got.Reason)
	}
}
