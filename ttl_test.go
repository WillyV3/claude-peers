package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseFlexibleDuration_GoNative(t *testing.T) {
	cases := map[string]time.Duration{
		"1h":      time.Hour,
		"24h":     24 * time.Hour,
		"72h30m":  72*time.Hour + 30*time.Minute,
		"168h":    7 * 24 * time.Hour,
		"1m":      time.Minute,
		"8760h":   365 * 24 * time.Hour, // exactly the max
		"2h45m3s": 2*time.Hour + 45*time.Minute + 3*time.Second,
	}
	for in, want := range cases {
		got, err := ParseFlexibleDuration(in)
		if err != nil {
			t.Errorf("ParseFlexibleDuration(%q): unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseFlexibleDuration(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestParseFlexibleDuration_DayShorthand(t *testing.T) {
	cases := map[string]time.Duration{
		"1d":   24 * time.Hour,
		"7d":   7 * 24 * time.Hour,
		"30d":  30 * 24 * time.Hour,
		"365d": 365 * 24 * time.Hour,
	}
	for in, want := range cases {
		got, err := ParseFlexibleDuration(in)
		if err != nil {
			t.Errorf("ParseFlexibleDuration(%q): unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseFlexibleDuration(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestParseFlexibleDuration_Mixed(t *testing.T) {
	// Day shorthand can appear alongside Go-native units in the same string.
	// The substitution rewrites "Nd" to "(N*24)h" and time.ParseDuration
	// handles the rest, so "1d12h" must equal 36h.
	got, err := ParseFlexibleDuration("1d12h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := 36 * time.Hour; got != want {
		t.Errorf("ParseFlexibleDuration(\"1d12h\") = %s, want %s", got, want)
	}
}

func TestParseFlexibleDuration_RejectsOutOfBounds(t *testing.T) {
	tooShort := []string{"30s", "59s", "0s", "500ms"}
	for _, in := range tooShort {
		if _, err := ParseFlexibleDuration(in); err == nil {
			t.Errorf("ParseFlexibleDuration(%q): expected error below minimum, got nil", in)
		} else if !strings.Contains(err.Error(), "below minimum") {
			t.Errorf("ParseFlexibleDuration(%q): wrong error: %v", in, err)
		}
	}

	tooLong := []string{"366d", "9000h", "2000d"}
	for _, in := range tooLong {
		if _, err := ParseFlexibleDuration(in); err == nil {
			t.Errorf("ParseFlexibleDuration(%q): expected error above maximum, got nil", in)
		} else if !strings.Contains(err.Error(), "exceeds maximum") {
			t.Errorf("ParseFlexibleDuration(%q): wrong error: %v", in, err)
		}
	}
}

func TestParseFlexibleDuration_RejectsGarbage(t *testing.T) {
	cases := []string{
		"",
		"abc",
		"30",         // missing unit
		"d30",        // backwards
		"--ttl 30d",  // whole flag accidentally passed
		"30d-extra",  // trailing junk (ParseDuration will reject)
	}
	for _, in := range cases {
		if _, err := ParseFlexibleDuration(in); err == nil {
			t.Errorf("ParseFlexibleDuration(%q): expected error, got nil", in)
		}
	}
}

func TestParseIssueTokenArgs_Default(t *testing.T) {
	orig := cfg.DefaultChildTTL
	defer func() { cfg.DefaultChildTTL = orig }()
	cfg.DefaultChildTTL = "24h"

	got, err := parseIssueTokenArgs([]string{"/tmp/client.pub", "peer-session"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ttlFromFlag {
		t.Error("ttlFromFlag should be false when no --ttl passed")
	}
	if got.ttl != 24*time.Hour {
		t.Errorf("default ttl = %s, want 24h", got.ttl)
	}
	if len(got.positional) != 2 || got.positional[0] != "/tmp/client.pub" || got.positional[1] != "peer-session" {
		t.Errorf("positional = %#v, want [/tmp/client.pub peer-session]", got.positional)
	}
}

func TestParseIssueTokenArgs_BrokerDefaultTTL(t *testing.T) {
	// If the broker is configured with a longer DefaultChildTTL (e.g.
	// Willy sets 30d on his always-on host), issue-token with no --ttl
	// must inherit that, not the historical 24h.
	orig := cfg.DefaultChildTTL
	defer func() { cfg.DefaultChildTTL = orig }()
	cfg.DefaultChildTTL = "30d"

	got, err := parseIssueTokenArgs([]string{"/tmp/client.pub", "peer-session"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ttl != 30*24*time.Hour {
		t.Errorf("inherited ttl = %s, want 720h", got.ttl)
	}
	if got.ttlFromFlag {
		t.Error("ttlFromFlag should be false (came from config, not flag)")
	}
}

func TestParseIssueTokenArgs_FlagVariants(t *testing.T) {
	cases := [][]string{
		{"--ttl", "30d", "/tmp/client.pub", "peer-session"},
		{"/tmp/client.pub", "--ttl", "30d", "peer-session"},
		{"/tmp/client.pub", "peer-session", "--ttl", "30d"},
		{"--ttl=30d", "/tmp/client.pub", "peer-session"},
		{"/tmp/client.pub", "--ttl=30d", "peer-session"},
	}
	for _, args := range cases {
		got, err := parseIssueTokenArgs(args)
		if err != nil {
			t.Errorf("args=%v: unexpected error: %v", args, err)
			continue
		}
		if !got.ttlFromFlag {
			t.Errorf("args=%v: ttlFromFlag should be true", args)
		}
		if got.ttl != 30*24*time.Hour {
			t.Errorf("args=%v: ttl = %s, want 720h", args, got.ttl)
		}
		if len(got.positional) != 2 || got.positional[0] != "/tmp/client.pub" || got.positional[1] != "peer-session" {
			t.Errorf("args=%v: positional = %#v", args, got.positional)
		}
	}
}

func TestParseIssueTokenArgs_Errors(t *testing.T) {
	cases := [][]string{
		{"--ttl"},                                    // missing value
		{"--ttl", "bogus", "/tmp/p", "role"},         // unparseable duration
		{"--ttl", "30s", "/tmp/p", "role"},           // below minimum
		{"--ttl", "2000d", "/tmp/p", "role"},         // above maximum
	}
	for _, args := range cases {
		if _, err := parseIssueTokenArgs(args); err == nil {
			t.Errorf("args=%v: expected error, got nil", args)
		}
	}
}

// TestIssueTokenTTL_EndToEnd mints a child token with an explicit --ttl
// via the same flag-parsing path the CLI uses, then decodes the resulting
// JWT and verifies the exp claim landed where we asked. This is the
// regression test that catches "I wired the flag but forgot to pass it
// to MintToken" — the exact bug that would revive the treadmill.
func TestIssueTokenTTL_EndToEnd(t *testing.T) {
	orig := cfg.DefaultChildTTL
	defer func() { cfg.DefaultChildTTL = orig }()
	cfg.DefaultChildTTL = "24h"

	rootKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate root: %v", err)
	}
	childKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("generate child: %v", err)
	}
	rootToken, err := MintRootToken(rootKP.PrivateKey, AllCapabilities(), 365*24*time.Hour)
	if err != nil {
		t.Fatalf("mint root: %v", err)
	}

	parsed, err := parseIssueTokenArgs([]string{"--ttl", "30d", "/ignored.pub", "peer-session"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.ttl != 30*24*time.Hour {
		t.Fatalf("parsed ttl wrong: %s", parsed.ttl)
	}

	childToken, err := MintToken(rootKP.PrivateKey, childKP.PublicKey, PeerSessionCapabilities(), parsed.ttl, rootToken)
	if err != nil {
		t.Fatalf("mint child: %v", err)
	}

	validator := NewTokenValidator(rootKP.PublicKey)
	// The validator walks the proof chain bottom-up, so the parent must
	// be known before a child will validate.
	validator.RegisterToken(rootToken, AllCapabilities())
	claims, err := validator.Validate(childToken)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if claims.ExpiresAt == nil {
		t.Fatal("child token has no ExpiresAt")
	}
	wantExp := time.Now().Add(30 * 24 * time.Hour)
	delta := claims.ExpiresAt.Time.Sub(wantExp)
	if delta < -10*time.Second || delta > 10*time.Second {
		t.Errorf("child exp off by %s (got %s, want ~%s)", delta, claims.ExpiresAt.Time, wantExp)
	}
}

func TestDefaultChildTTL_FallsBackOnMissing(t *testing.T) {
	// Reset cfg to default to simulate a fresh install; defaultChildTTL
	// should return 24h when the config is empty or unparseable.
	orig := cfg.DefaultChildTTL
	defer func() { cfg.DefaultChildTTL = orig }()

	cfg.DefaultChildTTL = ""
	if got := defaultChildTTL(); got != 24*time.Hour {
		t.Errorf("empty config: got %s, want 24h", got)
	}

	cfg.DefaultChildTTL = "bogus"
	if got := defaultChildTTL(); got != 24*time.Hour {
		t.Errorf("unparseable config: got %s, want 24h fallback", got)
	}

	cfg.DefaultChildTTL = "30d"
	if got, want := defaultChildTTL(), 30*24*time.Hour; got != want {
		t.Errorf("30d config: got %s, want %s", got, want)
	}
}
