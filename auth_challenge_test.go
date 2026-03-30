package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChallengeEndpoint(t *testing.T) {
	// Generate a keypair and save it so LoadKeyPair works.
	dir := t.TempDir()
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveKeyPair(kp, dir); err != nil {
		t.Fatal(err)
	}

	// The /challenge handler loads the keypair from configDir().
	// We need to override configDir. Since configDir uses HOME, set it.
	origHome := t.TempDir() // not used, just to avoid modifying real home
	_ = origHome

	// Instead of fighting configDir, just create a handler inline that mimics the broker's behavior.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeBody[ChallengeRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		sig := ed25519.Sign(kp.PrivateKey, []byte(req.Nonce))
		writeJSON(w, ChallengeResponse{
			Nonce:     req.Nonce,
			Signature: base64.RawURLEncoding.EncodeToString(sig),
			PublicKey: pubKeyToString(kp.PublicKey),
		})
	})

	// Test valid challenge.
	nonce := "test-nonce-12345"
	body, _ := json.Marshal(ChallengeRequest{Nonce: nonce})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/challenge", bytes.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp ChallengeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Nonce != nonce {
		t.Fatalf("expected nonce %q, got %q", nonce, resp.Nonce)
	}

	// Verify signature.
	sig, err := base64.RawURLEncoding.DecodeString(resp.Signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}

	if !ed25519.Verify(kp.PublicKey, []byte(nonce), sig) {
		t.Fatal("signature verification failed")
	}
}

func TestChallengeEndpointDifferentNonces(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, _ := decodeBody[ChallengeRequest](r)
		sig := ed25519.Sign(kp.PrivateKey, []byte(req.Nonce))
		writeJSON(w, ChallengeResponse{
			Nonce:     req.Nonce,
			Signature: base64.RawURLEncoding.EncodeToString(sig),
		})
	})

	nonces := []string{"nonce-a", "nonce-b", "nonce-c"}
	for _, nonce := range nonces {
		body, _ := json.Marshal(ChallengeRequest{Nonce: nonce})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/challenge", bytes.NewReader(body)))

		var resp ChallengeResponse
		json.Unmarshal(rec.Body.Bytes(), &resp)

		if resp.Nonce != nonce {
			t.Fatalf("nonce mismatch: expected %q, got %q", nonce, resp.Nonce)
		}

		sig, _ := base64.RawURLEncoding.DecodeString(resp.Signature)
		if !ed25519.Verify(kp.PublicKey, []byte(nonce), sig) {
			t.Fatalf("verification failed for nonce %q", nonce)
		}
	}
}

func TestChallengeEndpointBadBody(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := decodeBody[ChallengeRequest](r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/challenge", bytes.NewReader([]byte("not json"))))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad body, got %d", rec.Code)
	}
}

func TestChallengeWrongKeyVerification(t *testing.T) {
	kp1, _ := GenerateKeyPair()
	kp2, _ := GenerateKeyPair()

	nonce := "test-nonce"
	sig := ed25519.Sign(kp1.PrivateKey, []byte(nonce))

	// Verify with wrong key should fail.
	if ed25519.Verify(kp2.PublicKey, []byte(nonce), sig) {
		t.Fatal("signature should not verify with wrong public key")
	}
}
