package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// setupRefreshBroker returns a broker, its root keypair, and a delegated token
// issued to a fresh client keypair — ready to hit /refresh-token.
func setupRefreshBroker(t *testing.T) (*Broker, KeyPair, KeyPair, string) {
	t.Helper()
	b := testBroker(t)

	brokerKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	rootToken, err := MintRootToken(brokerKP.PrivateKey, AllCapabilities(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b.validator = NewTokenValidator(brokerKP.PublicKey)
	b.validator.RegisterToken(rootToken, AllCapabilities())

	clientKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	delegated, err := MintToken(brokerKP.PrivateKey, clientKP.PublicKey, PeerSessionCapabilities(), time.Hour, rootToken)
	if err != nil {
		t.Fatal(err)
	}
	b.validator.RegisterToken(delegated, PeerSessionCapabilities())

	return b, brokerKP, clientKP, delegated
}

// refreshHandler returns the /refresh-token http.HandlerFunc as the broker wires it.
// We duplicate the handler inline so the test doesn't depend on broker network setup.
func refreshHandler(b *Broker, brokerKP KeyPair, rootToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			writeAuthError(w, http.StatusUnauthorized, "missing authorization header", "NO_AUTH")
			return
		}
		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenStr == authHeader {
			writeAuthError(w, http.StatusUnauthorized, "missing bearer token", "NO_AUTH")
			return
		}

		if b.validator == nil {
			http.Error(w, "broker has no validator", 500)
			return
		}

		claims, err := b.validator.Validate(tokenStr)
		if err != nil {
			claims, err = b.validator.ValidateWithGrace(tokenStr, time.Hour)
			if err != nil {
				writeAuthError(w, http.StatusUnauthorized, err.Error(), "TOKEN_EXPIRED")
				return
			}
		}

		if len(claims.Audience) == 0 {
			writeAuthError(w, http.StatusBadRequest, "token has no audience", "INVALID_TOKEN")
			return
		}
		audiencePub, err := pubKeyFromString(claims.Audience[0])
		if err != nil {
			writeAuthError(w, http.StatusBadRequest, "invalid audience key", "INVALID_TOKEN")
			return
		}
		issuerPub, err := pubKeyFromString(claims.Issuer)
		if err != nil {
			writeAuthError(w, http.StatusBadRequest, "invalid issuer key", "INVALID_TOKEN")
			return
		}
		if issuerPub.Equal(audiencePub) {
			writeAuthError(w, http.StatusForbidden, "root tokens cannot be refreshed via this endpoint", "ROOT_TOKEN")
			return
		}

		newToken, err := MintToken(brokerKP.PrivateKey, audiencePub, claims.Capabilities, 24*time.Hour, rootToken)
		if err != nil {
			http.Error(w, "mint token: "+err.Error(), 500)
			return
		}
		b.validator.RegisterToken(newToken, claims.Capabilities)

		writeJSON(w, map[string]string{"token": newToken})
	}
}

func TestRefreshTokenValid(t *testing.T) {
	b, brokerKP, _, delegated := setupRefreshBroker(t)

	rootToken, err := MintRootToken(brokerKP.PrivateKey, AllCapabilities(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b.validator.RegisterToken(rootToken, AllCapabilities())

	handler := refreshHandler(b, brokerKP, rootToken)

	body, _ := json.Marshal(map[string]string{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/refresh-token", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+delegated)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	newToken, ok := resp["token"]
	if !ok || newToken == "" {
		t.Fatal("expected non-empty token in response")
	}
	if newToken == delegated {
		t.Fatal("refreshed token should differ from the old token")
	}

	// New token must be valid.
	claims, err := b.validator.Validate(newToken)
	if err != nil {
		t.Fatalf("refreshed token failed validation: %v", err)
	}

	// Must carry same capabilities.
	capSet := make(map[string]bool)
	for _, c := range claims.Capabilities {
		capSet[c.Resource] = true
	}
	for _, c := range PeerSessionCapabilities() {
		if !capSet[c.Resource] {
			t.Fatalf("refreshed token missing capability %q", c.Resource)
		}
	}

	// Must have fresh 24h TTL.
	if claims.ExpiresAt == nil {
		t.Fatal("refreshed token has no expiry")
	}
	remaining := time.Until(claims.ExpiresAt.Time)
	if remaining < 23*time.Hour {
		t.Fatalf("expected ~24h TTL, got %s", remaining.Round(time.Minute))
	}
}

func TestRefreshTokenExpiredWithinGrace(t *testing.T) {
	brokerKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	rootToken, err := MintRootToken(brokerKP.PrivateKey, AllCapabilities(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	b := testBroker(t)
	b.validator = NewTokenValidator(brokerKP.PublicKey)
	b.validator.RegisterToken(rootToken, AllCapabilities())

	clientKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	// Mint a token that expired 30 minutes ago — within the 1h grace window.
	expiredToken, err := MintToken(brokerKP.PrivateKey, clientKP.PublicKey, PeerSessionCapabilities(), -30*time.Minute, rootToken)
	if err != nil {
		t.Fatal(err)
	}
	// Register it so ValidateWithGrace can resolve the proof chain.
	b.validator.RegisterToken(expiredToken, PeerSessionCapabilities())

	handler := refreshHandler(b, brokerKP, rootToken)

	body, _ := json.Marshal(map[string]string{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/refresh-token", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+expiredToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for recently-expired token, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["token"] == "" {
		t.Fatal("expected new token in response")
	}
}

func TestRefreshTokenExpiredBeyondGrace(t *testing.T) {
	brokerKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	rootToken, err := MintRootToken(brokerKP.PrivateKey, AllCapabilities(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	b := testBroker(t)
	b.validator = NewTokenValidator(brokerKP.PublicKey)
	b.validator.RegisterToken(rootToken, AllCapabilities())

	clientKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	// Expired 2 hours ago — outside the 1h grace window.
	veryExpired, err := MintToken(brokerKP.PrivateKey, clientKP.PublicKey, PeerSessionCapabilities(), -2*time.Hour, rootToken)
	if err != nil {
		t.Fatal(err)
	}
	b.validator.RegisterToken(veryExpired, PeerSessionCapabilities())

	handler := refreshHandler(b, brokerKP, rootToken)

	body, _ := json.Marshal(map[string]string{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/refresh-token", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+veryExpired)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for token beyond grace, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &errResp)
	if errResp["code"] != "TOKEN_EXPIRED" {
		t.Fatalf("expected TOKEN_EXPIRED code, got %q", errResp["code"])
	}
}

func TestRefreshTokenNoAuth(t *testing.T) {
	b := testBroker(t)
	brokerKP, _ := GenerateKeyPair()
	rootToken, _ := MintRootToken(brokerKP.PrivateKey, AllCapabilities(), time.Hour)
	handler := refreshHandler(b, brokerKP, rootToken)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/refresh-token", bytes.NewReader([]byte("{}")))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without auth header, got %d", rec.Code)
	}
}

func TestRefreshTokenRootTokenRefused(t *testing.T) {
	brokerKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	rootToken, err := MintRootToken(brokerKP.PrivateKey, AllCapabilities(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	b := testBroker(t)
	b.validator = NewTokenValidator(brokerKP.PublicKey)
	b.validator.RegisterToken(rootToken, AllCapabilities())

	handler := refreshHandler(b, brokerKP, rootToken)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/refresh-token", bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer "+rootToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for root token refresh, got %d: %s", rec.Code, rec.Body.String())
	}

	var errResp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &errResp)
	if errResp["code"] != "ROOT_TOKEN" {
		t.Fatalf("expected ROOT_TOKEN code, got %q", errResp["code"])
	}
}

func TestRefreshTokenPreservesCapabilities(t *testing.T) {
	brokerKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	rootToken, err := MintRootToken(brokerKP.PrivateKey, AllCapabilities(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	b := testBroker(t)
	b.validator = NewTokenValidator(brokerKP.PublicKey)
	b.validator.RegisterToken(rootToken, AllCapabilities())

	clientKP, _ := GenerateKeyPair()

	// Issue a fleet-read token (subset of caps).
	fleetToken, err := MintToken(brokerKP.PrivateKey, clientKP.PublicKey, FleetReadCapabilities(), time.Hour, rootToken)
	if err != nil {
		t.Fatal(err)
	}
	b.validator.RegisterToken(fleetToken, FleetReadCapabilities())

	handler := refreshHandler(b, brokerKP, rootToken)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/refresh-token", bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer "+fleetToken)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &resp)

	// Parse the new token without validation just to inspect caps.
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithoutClaimsValidation())
	claims := &UCANClaims{}
	keyFunc := func(tok *jwt.Token) (any, error) {
		iss, _ := tok.Claims.GetIssuer()
		pub, err := pubKeyFromString(iss)
		return pub, err
	}
	if _, err := parser.ParseWithClaims(resp["token"], claims, keyFunc); err != nil {
		t.Fatalf("parse new token: %v", err)
	}

	expectedCaps := make(map[string]bool)
	for _, c := range FleetReadCapabilities() {
		expectedCaps[c.Resource] = true
	}
	// Must have exactly fleet-read caps — not more.
	for _, c := range claims.Capabilities {
		if !expectedCaps[c.Resource] {
			t.Fatalf("refreshed token has unexpected capability %q", c.Resource)
		}
	}
	if len(claims.Capabilities) != len(FleetReadCapabilities()) {
		t.Fatalf("expected %d caps, got %d", len(FleetReadCapabilities()), len(claims.Capabilities))
	}
}

func TestValidateWithGraceExpiredJustInside(t *testing.T) {
	kp, _ := GenerateKeyPair()
	rootToken, _ := MintRootToken(kp.PrivateKey, AllCapabilities(), time.Hour)
	v := NewTokenValidator(kp.PublicKey)
	v.RegisterToken(rootToken, AllCapabilities())

	clientKP, _ := GenerateKeyPair()
	// Expired 59 minutes ago — just inside 1h grace.
	tok, _ := MintToken(kp.PrivateKey, clientKP.PublicKey, PeerSessionCapabilities(), -59*time.Minute, rootToken)
	v.RegisterToken(tok, PeerSessionCapabilities())

	claims, err := v.ValidateWithGrace(tok, time.Hour)
	if err != nil {
		t.Fatalf("expected success within grace, got: %v", err)
	}
	if claims == nil {
		t.Fatal("expected non-nil claims")
	}
}

func TestValidateWithGraceExpiredJustOutside(t *testing.T) {
	kp, _ := GenerateKeyPair()
	rootToken, _ := MintRootToken(kp.PrivateKey, AllCapabilities(), time.Hour)
	v := NewTokenValidator(kp.PublicKey)
	v.RegisterToken(rootToken, AllCapabilities())

	clientKP, _ := GenerateKeyPair()
	// Expired 61 minutes ago — just outside 1h grace.
	tok, _ := MintToken(kp.PrivateKey, clientKP.PublicKey, PeerSessionCapabilities(), -61*time.Minute, rootToken)
	v.RegisterToken(tok, PeerSessionCapabilities())

	_, err := v.ValidateWithGrace(tok, time.Hour)
	if err == nil {
		t.Fatal("expected error outside grace window")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expiry error, got: %v", err)
	}
}
