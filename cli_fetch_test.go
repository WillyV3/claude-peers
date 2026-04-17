package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// T7 regression: cliFetchOnce must surface a 409 conflict response as a
// populated result with nil error. Both /register and /claim-agent emit
// 409 + a structured JSON body; callers (fleet_server.go register,
// handleToolCall claim_agent_name) need to inspect ok:false and the
// conflict fields. Pre-T7 cliFetchOnce treated all non-200 codes as
// errors, which caused fleet_server.go's T6 ephemeral-fallback to never
// execute (reg was never populated).

func TestCliFetchOnce_409ConflictPopulatesResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(RegisterResponse{
			OK:            false,
			Error:         `agent "keeper" already held by session abc123`,
			HeldBySession: "abc123",
			HeldByMachine: "omarchy",
			HeldByCWD:     "/home/willy/projects/claude-peers",
			HeldBySince:   "2026-04-17T00:00:00Z",
		})
	}))
	defer srv.Close()

	origURL := cfg.BrokerURL
	cfg.BrokerURL = srv.URL
	defer func() { cfg.BrokerURL = origURL }()

	var reg RegisterResponse
	err := cliFetchOnce("/register", RegisterRequest{AgentName: "keeper"}, &reg)
	if err != nil {
		t.Fatalf("expected nil error on 409 conflict, got %v", err)
	}
	if reg.OK {
		t.Fatal("expected reg.OK == false, got true")
	}
	if reg.HeldBySession != "abc123" {
		t.Fatalf("expected HeldBySession=abc123, got %q", reg.HeldBySession)
	}
	if reg.HeldByMachine != "omarchy" {
		t.Fatalf("expected HeldByMachine=omarchy, got %q", reg.HeldByMachine)
	}
	if reg.Error == "" {
		t.Fatal("expected Error populated, got empty string")
	}
}

func TestCliFetchOnce_500RemainsHardError(t *testing.T) {
	// Any non-200 status other than 409 should still surface as an error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer srv.Close()

	origURL := cfg.BrokerURL
	cfg.BrokerURL = srv.URL
	defer func() { cfg.BrokerURL = origURL }()

	var reg RegisterResponse
	err := cliFetchOnce("/health", nil, &reg)
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected error to mention 500, got %q", err.Error())
	}
}

func TestCliFetchOnce_409WithoutResultStillErrors(t *testing.T) {
	// If the caller passes nil result, we can't decode into anything, so
	// a 409 should remain an error (same as pre-T7 behavior).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"ok":false}`))
	}))
	defer srv.Close()

	origURL := cfg.BrokerURL
	cfg.BrokerURL = srv.URL
	defer func() { cfg.BrokerURL = origURL }()

	err := cliFetchOnce("/register", RegisterRequest{}, nil)
	if err == nil {
		t.Fatal("expected error on 409 with nil result, got nil")
	}
	if !strings.Contains(err.Error(), "409") {
		t.Fatalf("expected error to mention 409, got %q", err.Error())
	}
}

func TestCliFetchOnce_409WithGarbageBodyErrors(t *testing.T) {
	// If the 409 body isn't valid JSON for the result type, the decode
	// fails and we fall through to the normal error path -- no silent
	// success with zero-value result.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`not valid json`))
	}))
	defer srv.Close()

	origURL := cfg.BrokerURL
	cfg.BrokerURL = srv.URL
	defer func() { cfg.BrokerURL = origURL }()

	var reg RegisterResponse
	err := cliFetchOnce("/register", RegisterRequest{}, &reg)
	if err == nil {
		t.Fatal("expected error on 409 with garbage body, got nil")
	}
	if !strings.Contains(err.Error(), "409") {
		t.Fatalf("expected error to mention 409, got %q", err.Error())
	}
}
