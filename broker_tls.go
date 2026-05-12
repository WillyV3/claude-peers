package main

// T13: autocert TLS support. Optional. When enabled via TLSConfig.Domains,
// the broker terminates TLS on port 443 using Let's Encrypt via the
// TLS-ALPN-01 challenge (same port, no port 80 needed -- friendlier to
// minimal firewalls). When disabled, the broker behaves exactly as it
// did pre-T13: plain HTTP on cfg.Listen.
//
// Deployment story:
//   - Tailscale fleet: leave TLS off, WireGuard provides transport security.
//   - Single-binary public: set --tls-domain broker.example.com, point DNS
//     at the broker, open port 443. Cert auto-issued, auto-renewed.
//   - Behind reverse proxy (caddy/nginx/cloudflared): leave TLS off, the
//     proxy terminates and forwards plaintext over localhost.
//
// Non-goals for v1: self-signed + pinning, manual --tls-cert/--tls-key,
// HTTP-01 challenge (TLS-ALPN-01 is strictly easier to deploy).

import (
	"fmt"
	"net"
	"os"
	"strings"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// acmeStagingDirectoryURL is Let's Encrypt's staging endpoint. Use when
// dry-running deployments -- no rate limits, no real cert. Pin it in a
// const so a typo or LE migration trips a test rather than a deploy.
const acmeStagingDirectoryURL = "https://acme-staging-v02.api.letsencrypt.org/directory"

// TLSConfig holds the bootstrap parameters for the broker's optional TLS
// listener. Zero value = TLS disabled (current default). All fields are
// derived from CLI flags or env vars in runBroker.
type TLSConfig struct {
	// Domains is the list of hostnames Let's Encrypt should issue certs
	// for. Empty = TLS disabled. Multi-entry slices use SAN certs.
	Domains []string

	// Email is the optional account contact Let's Encrypt uses for
	// renewal-failure notifications. Strongly recommended for production
	// deployments; nothing breaks if omitted.
	Email string

	// CacheDir is the directory autocert uses to persist issued certs
	// and the ACME account key. Must be 0700 to protect the private key.
	// Required when Enabled() is true.
	CacheDir string

	// AcmeStaging routes ACME calls to Let's Encrypt staging instead of
	// production. Use for testing -- staging certs aren't browser-trusted
	// but exercise the full issuance flow.
	AcmeStaging bool
}

// Enabled reports whether TLS termination should be wired up. Cheap
// predicate so callers don't need to know the zero-value shape.
func (c TLSConfig) Enabled() bool {
	return len(c.Domains) > 0
}

// Validate refuses the most common operator mistakes BEFORE the broker
// binds a port. Catches them at startup instead of letting them surface
// as TLS handshake failures on first connection.
func (c TLSConfig) Validate() error {
	if !c.Enabled() {
		return nil
	}
	for _, d := range c.Domains {
		if d == "" {
			return fmt.Errorf("tls-domain has an empty entry")
		}
		if d == "localhost" || strings.HasPrefix(d, "localhost.") {
			return fmt.Errorf("tls-domain %q: Let's Encrypt does not issue certs for localhost (use a real domain or leave TLS disabled)", d)
		}
		if net.ParseIP(d) != nil {
			return fmt.Errorf("tls-domain %q: Let's Encrypt does not issue certs for IP addresses (use a domain name pointed at this IP)", d)
		}
	}
	if c.CacheDir == "" {
		return fmt.Errorf("tls-cache-dir is required when TLS is enabled (autocert needs somewhere to persist the cert)")
	}
	return nil
}

// parseTLSDomains splits a comma-separated --tls-domain value into a
// clean slice, trimming whitespace and dropping empty entries. Empty
// input returns nil so the resulting TLSConfig is .Enabled()==false.
func parseTLSDomains(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ensureCacheDir prepares the autocert cache directory. Creates with
// 0700 if missing; refuses to start if it exists with looser perms,
// because autocert stores the private key as a regular file inside.
func ensureCacheDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create tls cache dir %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat tls cache dir %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("tls cache path %s is not a directory", dir)
	}
	perm := info.Mode().Perm()
	if perm&0o077 != 0 {
		return fmt.Errorf("tls cache dir %s has mode %#o; must be 0700 to protect the autocert private key (chmod 700 %s)", dir, perm, dir)
	}
	return nil
}

// newAutocertManager assembles the autocert.Manager that drives
// TLS-ALPN-01 cert issuance. Returns an error (and nil manager) if the
// config fails Validate or the cache dir can't be prepared -- callers
// should treat this as a refuse-to-start condition.
//
// Important: HostPolicy is set to a strict whitelist of the configured
// domains. Without this, autocert would attempt to get a cert for any
// hostname an attacker sends in TLS-ALPN, which is an unauthorized-
// issuance vector and burns Let's Encrypt rate limits.
func newAutocertManager(c TLSConfig) (*autocert.Manager, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if err := ensureCacheDir(c.CacheDir); err != nil {
		return nil, err
	}
	m := &autocert.Manager{
		Cache:      autocert.DirCache(c.CacheDir),
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(c.Domains...),
		Email:      c.Email,
	}
	if c.AcmeStaging {
		m.Client = &acme.Client{DirectoryURL: acmeStagingDirectoryURL}
	}
	return m, nil
}
