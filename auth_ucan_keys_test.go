package main

import (
	"path/filepath"
	"testing"
)

func TestKeyPairRoundTrip(t *testing.T) {
	dir := t.TempDir()

	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	if err := SaveKeyPair(kp, dir); err != nil {
		t.Fatalf("SaveKeyPair: %v", err)
	}

	loaded, err := LoadKeyPair(dir)
	if err != nil {
		t.Fatalf("LoadKeyPair: %v", err)
	}

	if !kp.PublicKey.Equal(loaded.PublicKey) {
		t.Fatal("public keys do not match")
	}
	if !kp.PrivateKey.Equal(loaded.PrivateKey) {
		t.Fatal("private keys do not match")
	}
}

func TestLoadKeyPairMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadKeyPair(dir)
	if err == nil {
		t.Fatal("expected error loading missing keypair")
	}
}

func TestSaveAndLoadPublicKey(t *testing.T) {
	dir := t.TempDir()

	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "test.pub")
	if err := SavePublicKey(kp.PublicKey, path); err != nil {
		t.Fatalf("SavePublicKey: %v", err)
	}

	loaded, err := LoadPublicKey(path)
	if err != nil {
		t.Fatalf("LoadPublicKey: %v", err)
	}

	if !kp.PublicKey.Equal(loaded) {
		t.Fatal("public keys do not match")
	}
}

func TestLoadPublicKeyMissing(t *testing.T) {
	_, err := LoadPublicKey("/nonexistent/path.pub")
	if err == nil {
		t.Fatal("expected error loading missing public key")
	}
}

func TestSaveAndLoadToken(t *testing.T) {
	dir := t.TempDir()

	token := "eyJhbGciOiJFZDI1NTE5IiwidHlwIjoiSldUIn0.test.signature"
	if err := SaveToken(token, dir); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	loaded, err := LoadToken(dir)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}

	if loaded != token {
		t.Fatalf("expected %q, got %q", token, loaded)
	}
}

func TestLoadTokenMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadToken(dir)
	if err == nil {
		t.Fatal("expected error loading missing token")
	}
}

func TestPubKeyToString(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	s := pubKeyToString(kp.PublicKey)
	if s == "" {
		t.Fatal("expected non-empty string")
	}
}
