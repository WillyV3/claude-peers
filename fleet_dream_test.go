package main

import (
	"strings"
	"testing"
	"time"
)

func TestBuildFleetMemoryEmpty(t *testing.T) {
	content := buildFleetMemory(nil, nil)

	if !strings.Contains(content, "No active instances.") {
		t.Error("expected 'No active instances.' for empty peers")
	}
	if !strings.Contains(content, "No recent events.") {
		t.Error("expected 'No recent events.' for empty events")
	}
	if !strings.Contains(content, "fleet-activity") {
		t.Error("expected frontmatter with fleet-activity name")
	}
}

func TestBuildFleetMemoryWithPeers(t *testing.T) {
	peers := []Peer{
		{ID: "a1", Machine: "server1", CWD: "/home/user/projects/foo", GitRoot: "/home/user/projects/foo", Summary: "working on tests"},
		{ID: "a2", Machine: "server1", CWD: "/home/user/projects/bar", Summary: "debugging"},
		{ID: "b1", Machine: "laptop", CWD: "/home/user/code", Summary: "reviewing PR"},
	}

	content := buildFleetMemory(peers, nil)

	if !strings.Contains(content, "server1 (2 sessions)") {
		t.Error("expected server1 with 2 sessions")
	}
	if !strings.Contains(content, "laptop (1 sessions)") {
		t.Error("expected laptop with 1 session")
	}
	if !strings.Contains(content, "working on tests") {
		t.Error("expected peer summary in output")
	}
	if !strings.Contains(content, "(repo: foo)") {
		t.Error("expected repo name extracted from git root")
	}
}

func TestBuildFleetMemoryWithEvents(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	events := []Event{
		{ID: 1, Type: "peer_joined", PeerID: "a1", Machine: "server1", CreatedAt: now},
		{ID: 2, Type: "summary_changed", PeerID: "a1", Machine: "server1", Data: "writing tests", CreatedAt: now},
	}

	content := buildFleetMemory(nil, events)

	if !strings.Contains(content, "server1 joined") {
		t.Error("expected peer_joined event")
	}
	if !strings.Contains(content, "writing tests") {
		t.Error("expected summary data in event")
	}
}

func TestShortenPath(t *testing.T) {
	// Just test non-home paths since home dir varies
	got := shortenPath("/opt/something")
	if got != "/opt/something" {
		t.Errorf("shortenPath(/opt/something) = %q, want /opt/something", got)
	}
}

func TestTimeAgoStr(t *testing.T) {
	now := time.Now().UTC()

	cases := []struct {
		offset   time.Duration
		expected string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{48 * time.Hour, "2d"},
	}
	for _, tc := range cases {
		iso := now.Add(-tc.offset).Format(time.RFC3339)
		got := timeAgoStr(iso)
		if got != tc.expected {
			t.Errorf("timeAgoStr(%v ago) = %q, want %q", tc.offset, got, tc.expected)
		}
	}
}

func TestTimeAgoStrInvalid(t *testing.T) {
	got := timeAgoStr("not-a-date")
	if got != "?" {
		t.Errorf("expected '?' for invalid date, got %q", got)
	}
}
