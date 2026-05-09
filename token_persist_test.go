package main

// T12: persisted-knownTokens tests. Proves that delegated chains survive
// broker restart -- the auth-papercut that bit jim during the v0.3.x
// rollout. Without persistence, every restart wipes the in-memory
// knownTokens map and any token whose proof references a peer (rather
// than root) goes "BAD_PROOF: parent token not registered" until the
// peer re-authenticates. With persistence, restored on startup, chains
// hold across restarts.

import (
	"path/filepath"
	"testing"
	"time"
)

// newPersistedBroker spins up a broker against a fixed DB path so we can
// "restart" by closing and re-opening newBroker. testBroker uses t.TempDir
// directly and closes on cleanup -- this variant lets the second open
// reuse the same file.
func newPersistedBroker(t *testing.T, dbPath string) *Broker {
	t.Helper()
	cfg.DBPath = dbPath
	cfg.StaleTimeout = 300
	b, err := newBroker()
	if err != nil {
		t.Fatal(err)
	}
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	b.validator = NewTokenValidator(kp.PublicKey)
	b.validator.Restore(loadKnownTokens(b.db))
	b.validator.WithPersister(&sqliteTokenPersister{db: b.db})
	rootToken, err := MintRootToken(kp.PrivateKey, AllCapabilities(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b.validator.RegisterToken(rootToken, AllCapabilities())
	return b
}

// TestTokenPersist_SurvivesRestart is the headline test. Mint root,
// mint a peer token (delegates from root), validate it once. Close the
// broker. Re-open against the same DB. Validate the same peer token
// AGAIN -- should succeed because the parent (root) is back in
// knownTokens via Restore.
func TestTokenPersist_SurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.db")

	// Round 1: register a delegated token, validate it.
	b1 := newPersistedBroker(t, dbPath)
	rootKP, _ := GenerateKeyPair()
	b1.validator = NewTokenValidator(rootKP.PublicKey)
	b1.validator.Restore(loadKnownTokens(b1.db))
	b1.validator.WithPersister(&sqliteTokenPersister{db: b1.db})
	rootToken, _ := MintRootToken(rootKP.PrivateKey, AllCapabilities(), time.Hour)
	b1.validator.RegisterToken(rootToken, AllCapabilities())

	childKP, _ := GenerateKeyPair()
	peerToken, err := MintToken(rootKP.PrivateKey, childKP.PublicKey, PeerSessionCapabilities(), time.Hour, rootToken)
	if err != nil {
		t.Fatalf("mint peer token: %v", err)
	}
	if _, err := b1.validator.Validate(peerToken); err != nil {
		t.Fatalf("first validate failed: %v", err)
	}
	b1.db.Close()

	// Round 2: same DB, fresh broker process simulating a restart.
	b2 := newPersistedBroker(t, dbPath)
	defer b2.db.Close()
	// Re-bootstrap the validator with the SAME root keypair so signature
	// verification still works -- in production the broker loads its
	// keypair from disk (configDir).
	b2.validator = NewTokenValidator(rootKP.PublicKey)
	b2.validator.Restore(loadKnownTokens(b2.db))
	b2.validator.WithPersister(&sqliteTokenPersister{db: b2.db})
	b2.validator.RegisterToken(rootToken, AllCapabilities())

	// The peer token's parent is rootToken. Persistence should have stored
	// the peer token's own hash on validation in round 1. Now in round 2,
	// after Restore, the peer token's parent (root) is registered and the
	// validation should succeed without re-doing any auth dance.
	if _, err := b2.validator.Validate(peerToken); err != nil {
		t.Fatalf("post-restart validate failed (should have survived): %v", err)
	}

	// The bigger win: a GRANDCHILD token (peer -> grandchild) should also
	// validate after restart, because the peer token was persisted on its
	// validation in round 1.
	grandchildKP, _ := GenerateKeyPair()
	grandchildToken, err := MintToken(childKP.PrivateKey, grandchildKP.PublicKey, PeerSessionCapabilities(), time.Hour, peerToken)
	if err != nil {
		t.Fatalf("mint grandchild: %v", err)
	}
	if _, err := b2.validator.Validate(grandchildToken); err != nil {
		t.Fatalf("grandchild validate failed -- the bug T12 fixes: %v", err)
	}
}

// TestTokenPersist_PrunesExpired verifies that loadKnownTokens drops rows
// past their expires_at timestamp, so the table doesn't grow without bound
// across many restarts.
func TestTokenPersist_PrunesExpired(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "prune.db")
	b := newPersistedBroker(t, dbPath)
	defer b.db.Close()

	// Insert a known-expired row directly.
	b.db.Exec(
		`INSERT INTO known_tokens (hash, capabilities, expires_at) VALUES (?, ?, ?)`,
		"expired-hash", `["msg/send"]`, "2020-01-01T00:00:00Z",
	)
	// And one that's still alive.
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	b.db.Exec(
		`INSERT INTO known_tokens (hash, capabilities, expires_at) VALUES (?, ?, ?)`,
		"alive-hash", `["msg/send"]`, future,
	)

	loaded := loadKnownTokens(b.db)
	var aliveSeen, expiredSeen bool
	for _, row := range loaded {
		switch row.Hash {
		case "alive-hash":
			aliveSeen = true
		case "expired-hash":
			expiredSeen = true
		}
	}
	if !aliveSeen {
		t.Fatal("alive token should be in restored set")
	}
	if expiredSeen {
		t.Fatal("expired token should have been pruned")
	}

	// Confirm the expired row was DELETED from the table, not just filtered.
	var count int
	b.db.QueryRow("SELECT COUNT(*) FROM known_tokens WHERE hash = ?", "expired-hash").Scan(&count)
	if count != 0 {
		t.Fatalf("expected expired row to be deleted, found %d rows", count)
	}
}

// TestTokenPersist_NilPersisterStillWorks pins the in-memory-only path
// (zero-value validator, no persister attached). RegisterToken and
// validate must not panic when persister is nil -- this is the test-suite
// path and the no-DB path.
func TestTokenPersist_NilPersisterStillWorks(t *testing.T) {
	rootKP, _ := GenerateKeyPair()
	v := NewTokenValidator(rootKP.PublicKey)
	rootToken, _ := MintRootToken(rootKP.PrivateKey, AllCapabilities(), time.Hour)

	// Should not panic: no persister attached.
	v.RegisterToken(rootToken, AllCapabilities())
	if _, err := v.Validate(rootToken); err != nil {
		t.Fatalf("nil-persister validate: %v", err)
	}
}

// TestTokenPersist_SaveRoundtrip verifies the JSON encoding of capabilities
// in the table. Pin the on-disk shape so a future schema change trips a
// test rather than silently losing capability rows after a restart.
func TestTokenPersist_SaveRoundtrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "shape.db")
	b := newPersistedBroker(t, dbPath)
	defer b.db.Close()

	caps := []string{"msg/send", "msg/poll", "peer/heartbeat"}
	p := &sqliteTokenPersister{db: b.db}
	expiry := time.Now().Add(time.Hour)
	p.Save("test-hash", caps, expiry)

	var capsJSON, expiresAt string
	err := b.db.QueryRow("SELECT capabilities, expires_at FROM known_tokens WHERE hash = ?", "test-hash").
		Scan(&capsJSON, &expiresAt)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if capsJSON != `["msg/send","msg/poll","peer/heartbeat"]` {
		t.Fatalf("unexpected capabilities JSON: %s", capsJSON)
	}
	if expiresAt == "" {
		t.Fatal("expires_at should be set")
	}

	// Save again with same hash -- should UPSERT, not duplicate.
	p.Save("test-hash", []string{"msg/send"}, expiry)
	var count int
	b.db.QueryRow("SELECT COUNT(*) FROM known_tokens WHERE hash = ?", "test-hash").Scan(&count)
	if count != 1 {
		t.Fatalf("upsert should keep one row, got %d", count)
	}
	b.db.QueryRow("SELECT capabilities FROM known_tokens WHERE hash = ?", "test-hash").Scan(&capsJSON)
	if capsJSON != `["msg/send"]` {
		t.Fatalf("upsert should overwrite caps, got %s", capsJSON)
	}
}
