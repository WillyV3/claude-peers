package main

// ADR-001 HTTP integration proof: runs the same architecture scenarios through
// the real HTTP broker with UCAN auth. Exercises the full request path
// (handlers, middleware, JSON encode/decode, 409 status).

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// brokerHTTPFixture wires up the full broker mux for HTTP-level testing.
type brokerHTTPFixture struct {
	broker *Broker
	srv    *httptest.Server
	token  string
}

func newBrokerHTTPFixture(t *testing.T) *brokerHTTPFixture {
	t.Helper()
	b := testBroker(t)

	// Fresh keypair + peer session token for the test client.
	rootKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	childKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	v := NewTokenValidator(rootKP.PublicKey)
	rootToken, err := MintRootToken(rootKP.PrivateKey, AllCapabilities(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	v.RegisterToken(rootToken, AllCapabilities())
	peerToken, err := MintToken(rootKP.PrivateKey, childKP.PublicKey, PeerSessionCapabilities(), time.Hour, rootToken)
	if err != nil {
		t.Fatal(err)
	}
	b.validator = v

	mux := http.NewServeMux()
	mux.HandleFunc("POST /register", requireCapability("peer/register", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[RegisterRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		resp := b.register(req)
		if !resp.OK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(resp)
			return
		}
		writeJSON(w, resp)
	}))
	mux.HandleFunc("POST /send-message", requireCapability("msg/send", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[SendMessageRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
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
	mux.HandleFunc("POST /ack-message", requireCapability("msg/ack", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[AckMessageRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b.ackMessage(req)
		writeJSON(w, map[string]bool{"ok": true})
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

	srv := httptest.NewServer(ucanMiddleware(v)(mux))
	t.Cleanup(srv.Close)

	return &brokerHTTPFixture{broker: b, srv: srv, token: peerToken}
}

func (f *brokerHTTPFixture) post(t *testing.T, path string, body any) *http.Response {
	t.Helper()
	data, _ := json.Marshal(body)
	return doRequest(t, f.srv, "POST", path, f.token, data)
}

func (f *brokerHTTPFixture) postJSON(t *testing.T, path string, body, out any) int {
	t.Helper()
	resp := f.post(t, path, body)
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 400 {
		raw, _ := io.ReadAll(resp.Body)
		json.Unmarshal(raw, out)
	} else if out != nil {
		raw, _ := io.ReadAll(resp.Body)
		json.Unmarshal(raw, out)
	}
	return resp.StatusCode
}

// TestHTTP_EndToEnd_AgentMessaging runs the happy path through the HTTP layer:
// two agents register, exchange a message, ack. Proves handlers + middleware +
// JSON codec + broker logic all work together.
func TestHTTP_EndToEnd_AgentMessaging(t *testing.T) {
	f := newBrokerHTTPFixture(t)

	// Register alice.
	var aliceReg RegisterResponse
	if s := f.postJSON(t, "/register",
		RegisterRequest{AgentName: "alice-http", PID: 1, Machine: "m1", CWD: "/a"},
		&aliceReg,
	); s != 200 {
		t.Fatalf("alice register: expected 200, got %d (%+v)", s, aliceReg)
	}
	if !aliceReg.OK || aliceReg.ID == "" {
		t.Fatalf("alice register failed: %+v", aliceReg)
	}

	// Register bob.
	var bobReg RegisterResponse
	if s := f.postJSON(t, "/register",
		RegisterRequest{AgentName: "bob-http", PID: 2, Machine: "m2", CWD: "/b"},
		&bobReg,
	); s != 200 {
		t.Fatalf("bob register: expected 200, got %d", s)
	}

	// Alice -> Bob.
	var sendResp SendMessageResponse
	f.postJSON(t, "/send-message",
		SendMessageRequest{FromID: aliceReg.ID, ToAgent: "bob-http", Text: "hello over HTTP"},
		&sendResp,
	)
	if !sendResp.OK || sendResp.Queued {
		t.Fatalf("send failed or unexpectedly queued: %+v", sendResp)
	}

	// Bob polls.
	var pollResp PollMessagesResponse
	f.postJSON(t, "/poll-messages", PollMessagesRequest{ID: bobReg.ID}, &pollResp)
	if len(pollResp.Messages) != 1 {
		t.Fatalf("bob should have 1 message, got %d", len(pollResp.Messages))
	}
	if pollResp.Messages[0].Text != "hello over HTTP" {
		t.Fatalf("expected 'hello over HTTP', got %q", pollResp.Messages[0].Text)
	}
	if pollResp.Messages[0].FromAgent != "alice-http" {
		t.Fatalf("expected from_agent 'alice-http', got %q", pollResp.Messages[0].FromAgent)
	}

	// Bob acks.
	var ackResp map[string]bool
	f.postJSON(t, "/ack-message",
		AckMessageRequest{SessionID: bobReg.ID, MessageID: pollResp.Messages[0].ID},
		&ackResp,
	)
	if !ackResp["ok"] {
		t.Fatal("ack should return ok=true")
	}
}

// TestHTTP_NameCollisionReturns409 proves that a second register with a held
// agent name returns HTTP 409 with the populated conflict block in the body.
func TestHTTP_NameCollisionReturns409(t *testing.T) {
	f := newBrokerHTTPFixture(t)

	var first RegisterResponse
	if s := f.postJSON(t, "/register",
		RegisterRequest{AgentName: "caretaker-http", PID: 1, Machine: "m1", CWD: "/a"},
		&first,
	); s != 200 || !first.OK {
		t.Fatalf("first register: expected 200/OK, got %d/%+v", s, first)
	}

	var second RegisterResponse
	status := f.postJSON(t, "/register",
		RegisterRequest{AgentName: "caretaker-http", PID: 2, Machine: "m2", CWD: "/b"},
		&second,
	)
	if status != http.StatusConflict {
		t.Fatalf("expected HTTP 409 on name collision, got %d", status)
	}
	if second.OK {
		t.Fatal("conflict response should have OK=false")
	}
	if second.HeldBySession != first.ID {
		t.Fatalf("HeldBySession should be %s, got %s", first.ID, second.HeldBySession)
	}
	if second.HeldByMachine != "m1" || second.HeldByCWD != "/a" {
		t.Fatalf("conflict location wrong: machine=%q cwd=%q", second.HeldByMachine, second.HeldByCWD)
	}
}

// TestHTTP_RestartContinuity proves the real restart flow over HTTP:
// session dies (unregister), sender queues a message, same agent name
// registers again (new session ID), queued message drains.
func TestHTTP_RestartContinuity(t *testing.T) {
	f := newBrokerHTTPFixture(t)

	// Sender registers.
	var sender RegisterResponse
	f.postJSON(t, "/register",
		RegisterRequest{AgentName: "sender-http", PID: 1, Machine: "m1", CWD: "/a"},
		&sender,
	)

	// Jim registers, then leaves.
	var jim1 RegisterResponse
	f.postJSON(t, "/register",
		RegisterRequest{AgentName: "jim-http", PID: 2, Machine: "m2", CWD: "/b"},
		&jim1,
	)
	var ok map[string]bool
	f.postJSON(t, "/unregister", UnregisterRequest{ID: jim1.ID}, &ok)

	// Sender sends to offline jim -- should queue.
	var sendResp SendMessageResponse
	f.postJSON(t, "/send-message",
		SendMessageRequest{FromID: sender.ID, ToAgent: "jim-http", Text: "welcome back"},
		&sendResp,
	)
	if !sendResp.OK {
		t.Fatalf("send to offline agent should succeed, got %+v", sendResp)
	}
	if !sendResp.Queued {
		t.Fatal("send to offline agent should be marked Queued")
	}

	// Jim comes back with a NEW session ID.
	var jim2 RegisterResponse
	if s := f.postJSON(t, "/register",
		RegisterRequest{AgentName: "jim-http", PID: 3, Machine: "m2", CWD: "/b"},
		&jim2,
	); s != 200 || !jim2.OK {
		t.Fatalf("jim re-register: expected 200/OK, got %d/%+v", s, jim2)
	}
	if jim2.ID == jim1.ID {
		t.Fatal("new session should have a new session ID (ID rotation)")
	}

	// Jim polls -- should receive the queued message addressed to the agent.
	var poll PollMessagesResponse
	f.postJSON(t, "/poll-messages", PollMessagesRequest{ID: jim2.ID}, &poll)
	if len(poll.Messages) != 1 {
		t.Fatalf("jim should receive queued message on reconnect, got %d", len(poll.Messages))
	}
	if poll.Messages[0].Text != "welcome back" {
		t.Fatalf("expected 'welcome back', got %q", poll.Messages[0].Text)
	}
	if poll.Messages[0].FromAgent != "sender-http" {
		t.Fatalf("from_agent should survive the restart, got %q", poll.Messages[0].FromAgent)
	}
}
