package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	log.SetFlags(0)
	initConfig()
	authToken = loadAuthToken()

	// Parse --as <agent-name> before dispatching. Strips the flag from os.Args
	// so subcommand switch below sees a clean slice.
	parseGlobalFlags()

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	switch os.Args[1] {
	case "broker":
		if err := runBroker(ctx); err != nil {
			log.Fatal(err)
		}
	case "server":
		if err := runServer(ctx); err != nil {
			log.Fatal(err)
		}
	case "init":
		cliInit(os.Args[2:])
	case "config":
		cliShowConfig()
	case "status":
		cliStatus()
	case "peers":
		cliPeers()
	case "send":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "Usage: claude-peers send <peer-id> <message>")
			os.Exit(1)
		}
		cliSend(os.Args[2], strings.Join(os.Args[3:], " "))
	case "dream":
		cliDream()
	case "dream-watch":
		cliDreamWatch()
	case "issue-token":
		cliIssueToken(os.Args[2:])
	case "mint-root":
		kp, err := LoadKeyPair(configDir())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading keypair: %v\n", err)
			os.Exit(1)
		}
		token, err := MintRootToken(kp.PrivateKey, AllCapabilities(), 365*24*time.Hour)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error minting root token: %v\n", err)
			os.Exit(1)
		}
		if err := SaveRootToken(token, configDir()); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving root token: %v\n", err)
			os.Exit(1)
		}
		// Also save as peer token for the broker's own use
		if err := SaveToken(token, configDir()); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving token: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Root token minted and saved")
	case "save-token":
		cliSaveToken(os.Args[2:])
	case "refresh-token":
		cliRefreshToken()
	case "generate-nkey":
		cliGenerateNKey()
	case "kill-broker":
		cliKillBroker()
	case "reauth-fleet":
		cliReauthFleet()
	case "run":
		runClaudeWrapped(os.Args[2:])
	default:
		printUsage()
		os.Exit(1)
	}
}

// parseGlobalFlags extracts --as <name> from os.Args and stores it in
// agentNameOverride. Removes the flag + its value from os.Args so subcommand
// dispatch isn't confused by it.
func parseGlobalFlags() {
	out := make([]string, 0, len(os.Args))
	for i := 0; i < len(os.Args); i++ {
		a := os.Args[i]
		switch {
		case a == "--as":
			if i+1 >= len(os.Args) {
				fmt.Fprintln(os.Stderr, "--as requires an agent name")
				os.Exit(2)
			}
			agentNameOverride = os.Args[i+1]
			i++
		case strings.HasPrefix(a, "--as="):
			agentNameOverride = strings.TrimPrefix(a, "--as=")
		default:
			out = append(out, a)
		}
	}
	os.Args = out
}

func printUsage() {
	fmt.Println(`claude-peers - peer discovery and messaging for Claude Code

Usage:
  claude-peers init <role> [url]                Generate config (broker or client)
  claude-peers config                           Show current config
  claude-peers broker                           Start the broker daemon
  claude-peers server                           Start MCP stdio server (used by Claude Code)
  claude-peers run [claude-args...]             Launch claude with the claude-peers dev channel
                                                loaded (exec-replaces this process). Strips --as
                                                into CLAUDE_PEERS_AGENT for the child. Skips the
                                                channel flag on -p / --print (daemon-call friendly).
  claude-peers status                           Show broker status and all peers
  claude-peers peers                            List all peers
  claude-peers send <id> <msg>                  Send a message to a peer
  claude-peers issue-token [--ttl D] <pub> <role>  Issue a UCAN token (default 24h; "30d", "720h", etc)
  claude-peers save-token <jwt>                 Save a UCAN token locally
  claude-peers refresh-token                    Renew current token (auto-refreshes with broker)
  claude-peers mint-root                        Mint a new root token (broker only)
  claude-peers dream                            Snapshot fleet state to Claude memory
  claude-peers dream-watch                      Watch fleet via NATS and keep memory fresh
  claude-peers generate-nkey                    Generate a NATS NKey pair for per-machine auth
  claude-peers kill-broker                      Stop the broker daemon
  claude-peers reauth-fleet                     Re-issue tokens for all fleet machines via SSH

Token roles: peer-session, fleet-read, fleet-write, cli

Setup:
  # On the broker machine (e.g. your always-on server):
  claude-peers init broker
  claude-peers broker

  # On each client machine:
  claude-peers init client http://<broker-ip>:7899
  # Copy root.pub from broker, then on broker:
  claude-peers issue-token /path/to/client-identity.pub peer-session
  # On client, save the issued token:
  claude-peers save-token <jwt>`)
}

func cliFetch(path string, body any, result any) error {
	err := cliFetchOnce(path, body, result)
	if err == nil {
		return nil
	}

	// Auto-refresh on TOKEN_EXPIRED: try once to get a new token then retry.
	if strings.Contains(err.Error(), "TOKEN_EXPIRED") || strings.Contains(err.Error(), "token expired") {
		if refreshErr := doRefreshToken(); refreshErr == nil {
			return cliFetchOnce(path, body, result)
		}
	}
	return err
}

func cliFetchOnce(path string, body any, result any) error {
	data, _ := json.Marshal(body)
	client := http.Client{Timeout: 3 * time.Second}

	var req *http.Request
	if body != nil {
		req, _ = http.NewRequest("POST", cfg.BrokerURL+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest("GET", cfg.BrokerURL+path, nil)
	}
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		// T7: a 409 carries a structured conflict response. Both /register
		// and /claim-agent return 409 + a JSON body shaped identically to
		// the success response (ok:false, error, held_by_session,
		// held_by_machine, held_by_cwd, held_by_since). Pre-T7, cliFetchOnce
		// surfaced this as an opaque error string, which meant T6's
		// ephemeral-fallback in fleet_server.go never executed (its guard
		// `if !reg.OK && agentName != ""` sits after the error-bail at
		// register's caller, so reg was never populated). Decoding the body
		// into the caller's result lets them inspect ok:false and branch
		// on the conflict fields. All other non-200 codes remain hard errors.
		if resp.StatusCode == http.StatusConflict && result != nil {
			if decErr := json.Unmarshal(body, result); decErr == nil {
				return nil
			}
		}
		return fmt.Errorf("%d: %s", resp.StatusCode, string(body))
	}
	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}

// doRefreshToken calls /refresh-token, saves the new JWT, and updates authToken.
func doRefreshToken() error {
	current := authToken
	if current == "" {
		var err error
		current, err = LoadToken(configDir())
		if err != nil {
			return fmt.Errorf("no token to refresh: %w", err)
		}
	}

	data, _ := json.Marshal(map[string]string{})
	client := http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("POST", cfg.BrokerURL+"/refresh-token", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+current)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("refresh request: %w", err)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, string(b))
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return fmt.Errorf("decode refresh response: %w", err)
	}
	if result.Token == "" {
		return fmt.Errorf("broker returned empty token")
	}

	if err := SaveToken(result.Token, configDir()); err != nil {
		return fmt.Errorf("save refreshed token: %w", err)
	}
	authToken = result.Token
	return nil
}

func cliRefreshToken() {
	if err := doRefreshToken(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Token refreshed and saved to %s\n", filepath.Join(configDir(), tokenFile))
	if claims, err := func() (*UCANClaims, error) {
		t, err := LoadToken(configDir())
		if err != nil {
			return nil, err
		}
		rootPubPath := filepath.Join(configDir(), rootPubKeyFile)
		rootPub, err := LoadPublicKey(rootPubPath)
		if err != nil {
			return nil, err
		}
		v := NewTokenValidator(rootPub)
		return v.Validate(t)
	}(); err == nil {
		if claims.ExpiresAt != nil {
			fmt.Printf("New expiry: %s\n", claims.ExpiresAt.Time.Format(time.RFC3339))
		}
	}
}

func cliShowConfig() {
	fmt.Printf("Config: %s\n\n", configPath())
	fmt.Printf("  role:         %s\n", cfg.Role)
	fmt.Printf("  machine_name: %s\n", cfg.MachineName)
	fmt.Printf("  broker_url:   %s\n", cfg.BrokerURL)
	fmt.Printf("  listen:       %s\n", cfg.Listen)
	fmt.Printf("  db_path:      %s\n", cfg.DBPath)
	fmt.Printf("  stale_timeout: %ds\n", cfg.StaleTimeout)
}

func cliStatus() {
	var health HealthResponse
	if err := cliFetch("/health", nil, &health); err != nil {
		fmt.Printf("Broker at %s is not reachable.\n", cfg.BrokerURL)
		return
	}
	fmt.Printf("Broker: %s (%d peer(s), host: %s)\n", health.Status, health.Peers, health.Machine)
	fmt.Printf("URL: %s\n", cfg.BrokerURL)

	if health.Peers > 0 {
		var peers []Peer
		cliFetch("/list-peers", ListPeersRequest{Scope: "all"}, &peers)
		fmt.Println("\nPeers:")
		for _, p := range peers {
			printPeerLine(p, "  ")
		}
	}
}

func cliPeers() {
	var peers []Peer
	if err := cliFetch("/list-peers", ListPeersRequest{Scope: "all"}, &peers); err != nil {
		fmt.Printf("Broker at %s is not reachable.\n", cfg.BrokerURL)
		return
	}
	if len(peers) == 0 {
		fmt.Println("No peers registered.")
		return
	}
	for _, p := range peers {
		printPeerLine(p, "")
	}
}

// printPeerLine renders one peer with agent-or-session identity prefix.
func printPeerLine(p Peer, indent string) {
	if p.AgentName != "" {
		fmt.Printf("%s%s (agent) on %s [session %s]  %s\n", indent, p.AgentName, p.Machine, p.ID, p.CWD)
	} else {
		fmt.Printf("%ssession %s on %s (ephemeral)  %s\n", indent, p.ID, p.Machine, p.CWD)
	}
	if p.Summary != "" {
		fmt.Printf("%s  %s\n", indent, p.Summary)
	}
	fmt.Printf("%s  Last seen: %s\n", indent, p.LastSeen)
}

// cliSend sends a message by agent name (stable handle, queues if offline)
// or by session ID when the target is clearly a session ID (8-char hex that
// matches a live session but NOT any agent name). Default is ToAgent -- we
// never silently downgrade an offline-agent send to a session lookup, because
// that drops messages the user meant to queue.
func cliSend(to, msg string) {
	var peers []Peer
	cliFetch("/list-peers", ListPeersRequest{Scope: "all"}, &peers)

	// Is "to" the literal session ID of a live, ephemeral peer (no agent name)?
	// Only then do we route as ToSession. Otherwise always ToAgent.
	var targetEphemeralSession string
	for _, p := range peers {
		if p.ID == to && p.AgentName == "" {
			targetEphemeralSession = to
			break
		}
	}

	req := SendMessageRequest{FromID: "cli", Text: msg}
	if targetEphemeralSession != "" {
		req.ToSession = targetEphemeralSession
	} else {
		req.ToAgent = to
	}

	var resp SendMessageResponse
	if err := cliFetch("/send-message", req, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "Failed: %s\n", resp.Error)
		os.Exit(1)
	}
	switch resp.DeliveryStatus {
	case DeliveryStatusBound:
		fmt.Printf("Message sent to %s (recipient is online).\n", to)
	case DeliveryStatusQueuedOffline:
		fmt.Printf("Agent %q is offline. Message queued -- will deliver when that agent reconnects.\n", to)
	case DeliveryStatusQueuedUnknown:
		fmt.Printf("WARNING: no session has ever claimed agent name %q on this broker. Message queued but may sit indefinitely if the name is wrong. Run `claude-peers peers` to verify.\n", to)
	default:
		// Old broker (no DeliveryStatus) -- fall back to two-state output.
		if resp.Queued {
			fmt.Printf("Message queued for agent %q (no live session holds it right now -- will deliver on reconnect).\n", to)
		} else {
			fmt.Printf("Message sent to %s\n", to)
		}
	}
}

// issueTokenArgs is the parsed result of the issue-token CLI argument list.
// Separated from cliIssueToken so the flag-handling logic can be unit-tested
// without running the full command (which loads broker keypairs, etc.).
type issueTokenArgs struct {
	positional []string
	ttl        time.Duration
	ttlFromFlag bool
}

// parseIssueTokenArgs walks the raw arg slice, extracts --ttl / --ttl=<d>,
// and returns the remaining positional args plus the resolved TTL. The
// default TTL comes from the broker config (cfg.DefaultChildTTL, historically
// 24h) so callers that never pass --ttl keep pre-T3 behaviour.
func parseIssueTokenArgs(args []string) (*issueTokenArgs, error) {
	out := &issueTokenArgs{
		positional: make([]string, 0, len(args)),
		ttl:        defaultChildTTL(),
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		var raw string
		switch {
		case a == "--ttl":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("--ttl requires a duration (e.g. 30d, 720h)")
			}
			raw = args[i+1]
			i++
		case strings.HasPrefix(a, "--ttl="):
			raw = strings.TrimPrefix(a, "--ttl=")
		default:
			out.positional = append(out.positional, a)
			continue
		}
		d, err := ParseFlexibleDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("--ttl: %w", err)
		}
		out.ttl = d
		out.ttlFromFlag = true
	}
	return out, nil
}

func cliIssueToken(args []string) {
	parsed, err := parseIssueTokenArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ttl := parsed.ttl
	ttlSource := "default"
	if parsed.ttlFromFlag {
		ttlSource = "flag"
	}
	positional := parsed.positional

	if len(positional) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: claude-peers issue-token [--ttl <duration>] <machine-pub-path> <role>")
		fmt.Fprintln(os.Stderr, "Roles: peer-session, fleet-read, fleet-write, cli")
		fmt.Fprintln(os.Stderr, "TTL:   24h (default), 30d, 720h, 72h30m — max 365d")
		os.Exit(1)
	}

	pubPath := positional[0]
	role := positional[1]

	var caps []Capability
	switch role {
	case "peer-session":
		caps = PeerSessionCapabilities()
	case "fleet-read":
		caps = FleetReadCapabilities()
	case "fleet-write":
		caps = FleetWriteCapabilities()
	case "cli":
		caps = CLICapabilities()
	default:
		fmt.Fprintf(os.Stderr, "Unknown role: %s (use peer-session, fleet-read, fleet-write, or cli)\n", role)
		os.Exit(1)
	}

	kp, err := LoadKeyPair(configDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading broker keypair: %v\n", err)
		os.Exit(1)
	}

	parentToken, err := LoadRootToken(configDir())
	if err != nil {
		// Fallback to token.jwt for backward compat with existing installs.
		parentToken, err = LoadToken(configDir())
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading broker root token: %v\n", err)
			os.Exit(1)
		}
	}

	targetPub, err := LoadPublicKey(pubPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading target public key: %v\n", err)
		os.Exit(1)
	}

	token, err := MintToken(kp.PrivateKey, targetPub, caps, ttl, parentToken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error minting token: %v\n", err)
		os.Exit(1)
	}

	// Print to stdout so the caller can pipe into save-token. Print the
	// audit line to stderr so it doesn't contaminate the JWT on stdout.
	fmt.Fprintf(os.Stderr, "issued %s token for %s (ttl=%s, source=%s, expires=%s)\n",
		role, filepath.Base(pubPath), ttl, ttlSource,
		time.Now().Add(ttl).UTC().Format(time.RFC3339))
	fmt.Println(token)
}

func cliSaveToken(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: claude-peers save-token <jwt-string>")
		os.Exit(1)
	}

	token := args[0]
	if err := SaveToken(token, configDir()); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving token: %v\n", err)
		os.Exit(1)
	}

	// Validate the token if root.pub is available.
	rootPubPath := filepath.Join(configDir(), rootPubKeyFile)
	rootPub, err := LoadPublicKey(rootPubPath)
	if err != nil {
		fmt.Printf("Token saved to %s\n", filepath.Join(configDir(), tokenFile))
		fmt.Println("WARNING: could not load root.pub for validation")
		return
	}

	v := NewTokenValidator(rootPub)
	// Register root token as known parent if available (for delegated tokens).
	if rootToken, err := LoadToken(configDir()); err == nil && rootToken != token {
		v.RegisterToken(rootToken, AllCapabilities())
	}

	claims, err := v.Validate(token)
	if err != nil {
		fmt.Printf("Token saved to %s\n", filepath.Join(configDir(), tokenFile))
		fmt.Printf("WARNING: token validation failed: %v\n", err)
		return
	}

	fmt.Printf("Token saved to %s\n", filepath.Join(configDir(), tokenFile))
	fmt.Printf("Capabilities:\n")
	for _, c := range claims.Capabilities {
		fmt.Printf("  %s\n", c.Resource)
	}
	if claims.ExpiresAt != nil {
		fmt.Printf("Expires: %s\n", claims.ExpiresAt.Time.Format(time.RFC3339))
	}
}

func cliKillBroker() {
	var health HealthResponse
	if err := cliFetch("/health", nil, &health); err != nil {
		fmt.Println("Broker is not running.")
		return
	}
	fmt.Printf("Broker has %d peer(s). Shutting down...\n", health.Peers)

	port := strings.TrimPrefix(cfg.Listen, "0.0.0.0:")
	if strings.Contains(port, ":") {
		parts := strings.Split(port, ":")
		port = parts[len(parts)-1]
	}
	out, err := execOutput("lsof", "-ti", ":"+port)
	if err != nil {
		fmt.Println("Could not find broker process.")
		return
	}
	for pid := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if pid != "" {
			execOutput("kill", pid)
		}
	}
	fmt.Println("Broker stopped.")
}

// cliReauthFleet re-issues peer-session tokens for fleet machines via SSH.
// Configure SSH targets in your SSH config or pass them as arguments.
func cliReauthFleet() {
	fmt.Println("reauth-fleet: Configure SSH targets in your SSH config.")
	fmt.Println("For each machine:")
	fmt.Println("  1. scp <machine>:~/.config/claude-peers/identity.pub /tmp/machine.pub")
	fmt.Println("  2. claude-peers issue-token /tmp/machine.pub peer-session")
	fmt.Println("  3. ssh <machine> 'claude-peers save-token <jwt>'")
}

func execOutput(name string, args ...string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = &buf
	err := cmd.Run()
	return buf.String(), err
}
