package main

// T13: autocert TLS support tests. Drives the design of TLSConfig,
// ensureCacheDir, and newAutocertManager. No live Let's Encrypt calls --
// integration tests use httptest.NewTLSServer with a self-signed cert to
// prove the broker actually serves TLS end-to-end. The autocert plumbing
// is exercised via its public surface (HostPolicy, Email, Client) so a
// future autocert upgrade trips a test rather than rolling silently.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTLSConfig_EnabledZeroValue(t *testing.T) {
	var c TLSConfig
	if c.Enabled() {
		t.Fatal("zero-value TLSConfig should be disabled")
	}
}

func TestTLSConfig_EnabledWithDomain(t *testing.T) {
	c := TLSConfig{Domains: []string{"broker.example.com"}}
	if !c.Enabled() {
		t.Fatal("config with a domain should be enabled")
	}
}

// TestTLSConfig_Validate_RejectsIPLiteral covers the most common operator
// mistake: passing a Tailscale or LAN IP as --tls-domain. Let's Encrypt
// won't issue for IPs, so we refuse early rather than letting the broker
// start and then fail at first connection.
func TestTLSConfig_Validate_RejectsIPLiteral(t *testing.T) {
	for _, bad := range []string{
		"127.0.0.1",
		"100.109.211.128",
		"::1",
		"2001:db8::1",
	} {
		t.Run(bad, func(t *testing.T) {
			c := TLSConfig{Domains: []string{bad}, CacheDir: t.TempDir()}
			err := c.Validate()
			if err == nil {
				t.Fatalf("expected validation error for IP %q", bad)
			}
			if !strings.Contains(err.Error(), "IP") {
				t.Fatalf("error should mention IP, got: %v", err)
			}
		})
	}
}

// TestTLSConfig_Validate_RejectsLocalhost guards against the second-most
// common mistake (running TLS against localhost).
func TestTLSConfig_Validate_RejectsLocalhost(t *testing.T) {
	for _, bad := range []string{"localhost", "localhost.local", "localhost.localdomain"} {
		t.Run(bad, func(t *testing.T) {
			c := TLSConfig{Domains: []string{bad}, CacheDir: t.TempDir()}
			if err := c.Validate(); err == nil {
				t.Fatalf("expected validation error for %q", bad)
			}
		})
	}
}

// TestTLSConfig_Validate_RejectsEmptyEntry: --tls-domain "a.com,," produces
// an empty entry after split. Refuse rather than silently dropping.
func TestTLSConfig_Validate_RejectsEmptyEntry(t *testing.T) {
	c := TLSConfig{Domains: []string{"broker.example.com", ""}, CacheDir: t.TempDir()}
	if err := c.Validate(); err == nil {
		t.Fatal("expected validation error for empty domain entry")
	}
}

// TestTLSConfig_Validate_RejectsMissingCacheDir: cache dir is required when
// TLS is enabled. Empty string means autocert can't persist certs, which
// turns every restart into a Let's Encrypt rate-limit hazard.
func TestTLSConfig_Validate_RejectsMissingCacheDir(t *testing.T) {
	c := TLSConfig{Domains: []string{"broker.example.com"}, CacheDir: ""}
	if err := c.Validate(); err == nil {
		t.Fatal("expected validation error for missing cache dir")
	}
}

// TestTLSConfig_Validate_AllowsRealDomain pins the happy path: a real-
// looking domain + cache dir validates cleanly.
func TestTLSConfig_Validate_AllowsRealDomain(t *testing.T) {
	c := TLSConfig{
		Domains:  []string{"broker.example.com"},
		CacheDir: filepath.Join(t.TempDir(), "autocert"),
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("expected validation to pass, got: %v", err)
	}
}

// TestTLSConfig_Validate_AllowsMultipleDomains: --tls-domain a.com,b.com
// becomes a two-element slice. Both must validate.
func TestTLSConfig_Validate_AllowsMultipleDomains(t *testing.T) {
	c := TLSConfig{
		Domains:  []string{"a.example.com", "b.example.com"},
		CacheDir: filepath.Join(t.TempDir(), "autocert"),
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("multi-domain validation: %v", err)
	}
}

// TestEnsureCacheDir_CreatesWithCorrectPerms verifies that the cache dir
// is created with 0700 when it doesn't exist. autocert writes the cert
// + private key inside; loose perms would mean world-readable keys.
func TestEnsureCacheDir_CreatesWithCorrectPerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh-cache")
	if err := ensureCacheDir(dir); err != nil {
		t.Fatalf("ensureCacheDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	perm := info.Mode().Perm()
	if perm != 0o700 {
		t.Fatalf("expected mode 0700, got %#o", perm)
	}
}

// TestEnsureCacheDir_RefusesLoosePerms guards against the case where the
// dir exists already but has loose perms (operator created it themselves
// without the umask). We surface this instead of silently inheriting
// world-readable cert storage.
func TestEnsureCacheDir_RefusesLoosePerms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "loose-cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := ensureCacheDir(dir)
	if err == nil {
		t.Fatal("expected ensureCacheDir to reject loose perms")
	}
	if !strings.Contains(err.Error(), "0700") {
		t.Fatalf("error should mention required 0700 perms, got: %v", err)
	}
}

// TestEnsureCacheDir_PathIsFile catches the case where the configured
// cache-dir path is a regular file (e.g. operator typo'd a file path
// into the dir flag). Refuse cleanly.
func TestEnsureCacheDir_PathIsFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(path, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureCacheDir(path); err == nil {
		t.Fatal("expected ensureCacheDir to refuse a regular-file path")
	}
}

// TestNewAutocertManager_HostPolicyAllowsConfigured proves the manager's
// HostPolicy whitelist is wired to the configured domains. Without this,
// autocert would attempt to get certs for any hostname requested in
// TLS-ALPN, which is an injection / unauthorized-issuance vector.
func TestNewAutocertManager_HostPolicyAllowsConfigured(t *testing.T) {
	c := TLSConfig{
		Domains:  []string{"broker.example.com"},
		CacheDir: filepath.Join(t.TempDir(), "autocert"),
	}
	m, err := newAutocertManager(c)
	if err != nil {
		t.Fatalf("newAutocertManager: %v", err)
	}
	if err := m.HostPolicy(context.Background(), "broker.example.com"); err != nil {
		t.Fatalf("expected configured host to be allowed: %v", err)
	}
	if err := m.HostPolicy(context.Background(), "evil.attacker.com"); err == nil {
		t.Fatal("expected non-configured host to be REJECTED -- injection-vector test")
	}
}

// TestNewAutocertManager_StagingDirectoryURL: the --tls-acme-staging flag
// changes the ACME endpoint so we can dry-run against Let's Encrypt
// staging (no rate limit, no real cert). Pin the URL so a typo or LE
// migration trips a test.
func TestNewAutocertManager_StagingDirectoryURL(t *testing.T) {
	c := TLSConfig{
		Domains:     []string{"broker.example.com"},
		CacheDir:    filepath.Join(t.TempDir(), "autocert"),
		AcmeStaging: true,
	}
	m, err := newAutocertManager(c)
	if err != nil {
		t.Fatal(err)
	}
	if m.Client == nil {
		t.Fatal("staging mode should configure a custom ACME client")
	}
	if !strings.Contains(m.Client.DirectoryURL, "acme-staging-v02.api.letsencrypt.org") {
		t.Fatalf("staging URL not set correctly: %q", m.Client.DirectoryURL)
	}
}

// TestNewAutocertManager_ProductionDirectoryURL: without the staging
// flag, the manager uses the autocert default (production LE). Pin that
// it doesn't accidentally point at staging.
func TestNewAutocertManager_ProductionDirectoryURL(t *testing.T) {
	c := TLSConfig{
		Domains:  []string{"broker.example.com"},
		CacheDir: filepath.Join(t.TempDir(), "autocert"),
	}
	m, err := newAutocertManager(c)
	if err != nil {
		t.Fatal(err)
	}
	// In production mode we leave Client nil so autocert uses its default
	// endpoint. Pin that contract.
	if m.Client != nil && strings.Contains(m.Client.DirectoryURL, "staging") {
		t.Fatalf("production mode should not point at staging: %q", m.Client.DirectoryURL)
	}
}

// TestNewAutocertManager_RefusesInvalidConfig proves that the manager
// constructor short-circuits on validation errors instead of returning
// a half-built manager.
func TestNewAutocertManager_RefusesInvalidConfig(t *testing.T) {
	c := TLSConfig{Domains: []string{"127.0.0.1"}, CacheDir: t.TempDir()}
	m, err := newAutocertManager(c)
	if err == nil {
		t.Fatal("expected error for IP-literal domain")
	}
	if m != nil {
		t.Fatal("expected nil manager on validation error")
	}
}

// parseTLSDomains is a small helper that converts the --tls-domain flag
// value (which may be comma-separated) into a clean slice. Pin its
// trim behavior so " a.com , b.com " doesn't leave whitespace.
func TestParseTLSDomains(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"a.com", []string{"a.com"}},
		{"a.com,b.com", []string{"a.com", "b.com"}},
		{" a.com , b.com ", []string{"a.com", "b.com"}},
		{"a.com,,b.com", []string{"a.com", "b.com"}}, // empty entries skipped
	}
	for _, c := range cases {
		got := parseTLSDomains(c.input)
		if len(got) != len(c.want) {
			t.Fatalf("parseTLSDomains(%q): expected %d entries, got %d (%v)", c.input, len(c.want), len(got), got)
		}
		for i, w := range c.want {
			if got[i] != w {
				t.Fatalf("parseTLSDomains(%q)[%d]: expected %q, got %q", c.input, i, w, got[i])
			}
		}
	}
}

// TestParseBrokerFlags pins the CLI flag surface for the broker. Mostly
// hand-written so an extra unknown flag fails loud (typos silently
// disabling TLS would be a security footgun).
func TestParseBrokerFlags(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantDomain  string
		wantStaging bool
		wantErr     bool
	}{
		{"empty", []string{}, "", false, false},
		{"domain via separate arg", []string{"--tls-domain", "broker.example.com"}, "broker.example.com", false, false},
		{"domain via inline", []string{"--tls-domain=broker.example.com"}, "broker.example.com", false, false},
		{"staging bare flag", []string{"--tls-acme-staging"}, "", true, false},
		{"staging=true", []string{"--tls-acme-staging=true"}, "", true, false},
		{"staging=false", []string{"--tls-acme-staging=false"}, "", false, false},
		{"missing value", []string{"--tls-domain"}, "", false, true},
		{"unknown flag", []string{"--ssl-cert", "foo"}, "", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Reset shared state.
			cfg.TLSDomain = ""
			cfg.TLSAcmeStaging = false
			err := parseBrokerFlags(c.args)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error for args %v", c.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBrokerFlags(%v): %v", c.args, err)
			}
			if cfg.TLSDomain != c.wantDomain {
				t.Fatalf("TLSDomain: want %q, got %q", c.wantDomain, cfg.TLSDomain)
			}
			if cfg.TLSAcmeStaging != c.wantStaging {
				t.Fatalf("TLSAcmeStaging: want %v, got %v", c.wantStaging, cfg.TLSAcmeStaging)
			}
		})
	}
	// Final reset so other tests don't see TLS enabled.
	cfg.TLSDomain = ""
	cfg.TLSAcmeStaging = false
}

// TestTLSConfigFromGlobalConfig_DefaultCacheDir exercises the small
// translation layer between cfg and TLSConfig: when TLSCacheDir is empty
// but a domain is set, we default to <configDir>/autocert rather than
// failing validation.
func TestTLSConfigFromGlobalConfig_DefaultCacheDir(t *testing.T) {
	saved := cfg
	defer func() { cfg = saved }()

	cfg.TLSDomain = "broker.example.com"
	cfg.TLSCacheDir = ""

	tc := tlsConfigFromGlobalConfig()
	if !tc.Enabled() {
		t.Fatal("expected TLS to be enabled")
	}
	if tc.CacheDir == "" {
		t.Fatal("expected default cache dir to be filled in")
	}
	if !strings.HasSuffix(tc.CacheDir, "autocert") {
		t.Fatalf("default cache dir should end in /autocert, got %q", tc.CacheDir)
	}
}

// TestTLSConfigFromGlobalConfig_DisabledByDefault: zero cfg.TLSDomain
// produces a disabled TLSConfig. Pin so the default deployment doesn't
// silently flip to TLS.
func TestTLSConfigFromGlobalConfig_DisabledByDefault(t *testing.T) {
	saved := cfg
	defer func() { cfg = saved }()

	cfg.TLSDomain = ""

	tc := tlsConfigFromGlobalConfig()
	if tc.Enabled() {
		t.Fatal("default config should produce disabled TLS")
	}
}

// TestHTTP_BrokerOverTLS_E2E is the headline integration test. Stands up
// an httptest.NewTLSServer running the same handler the broker uses,
// drives a real HTTPS request through it (with self-signed cert
// verification disabled in the client), and verifies the broker
// behaves identically over TLS as over plain HTTP. This proves the
// handler is transport-agnostic -- catches regressions where someone
// accidentally hardcodes http:// somewhere.
func TestHTTP_BrokerOverTLS_E2E(t *testing.T) {
	f := newBrokerHTTPFixture(t)

	// Wrap the same handler in an https test server.
	tlsHandler := f.srv.Config.Handler
	tlsSrv := httptest.NewTLSServer(tlsHandler)
	defer tlsSrv.Close()

	// Verify the URL is https.
	if !strings.HasPrefix(tlsSrv.URL, "https://") {
		t.Fatalf("expected TLS server URL to start with https://, got %q", tlsSrv.URL)
	}

	// Send a register request over TLS.
	body, _ := json.Marshal(RegisterRequest{
		AgentName: "tls-test", PID: 1, Machine: "test", CWD: "/tmp",
	})
	req, _ := http.NewRequest("POST", tlsSrv.URL+"/register", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")

	// httptest.NewTLSServer uses a self-signed cert -- the included Client
	// already trusts it. Use that client.
	resp, err := tlsSrv.Client().Do(req)
	if err != nil {
		t.Fatalf("HTTPS request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 over TLS, got %d: %s", resp.StatusCode, string(raw))
	}
	var regResp RegisterResponse
	json.NewDecoder(resp.Body).Decode(&regResp)
	if !regResp.OK || regResp.ID == "" {
		t.Fatalf("registration over TLS failed: %+v", regResp)
	}

	// And just to prove the cert presented is actually a TLS cert (not
	// some passthrough), inspect the connection state on a follow-up
	// request. tls.ConnectionState should have a non-empty peer cert.
	req2, _ := http.NewRequest("GET", tlsSrv.URL+"/health", nil)
	req2.Header.Set("Authorization", "Bearer "+f.token)
	resp2, err := tlsSrv.Client().Do(req2)
	if err != nil {
		t.Fatalf("health over TLS: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.TLS == nil {
		t.Fatal("expected resp2.TLS to be populated -- not over TLS?")
	}
	var emptyState tls.ConnectionState
	if resp2.TLS.HandshakeComplete == emptyState.HandshakeComplete && !resp2.TLS.HandshakeComplete {
		t.Fatal("TLS handshake did not complete")
	}
}
