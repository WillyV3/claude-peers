package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Capability struct {
	Resource string `json:"resource"`
}

type UCANClaims struct {
	jwt.RegisteredClaims
	Capabilities []Capability `json:"cap"`
	Proof        string       `json:"prf"`
	MachineName  string       `json:"machine_name,omitempty"`
}

type capabilitySet map[string]bool

type TokenValidator struct {
	rootPubKey  ed25519.PublicKey
	knownTokens map[string]capabilitySet
	mu          sync.RWMutex
}

type KeyPair struct {
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey
}

var b64 = base64.RawURLEncoding

func pubKeyToString(pub ed25519.PublicKey) string {
	return b64.EncodeToString(pub)
}

func pubKeyFromString(s string) (ed25519.PublicKey, error) {
	data, err := b64.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}
	if len(data) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key length: %d", len(data))
	}
	return ed25519.PublicKey(data), nil
}

func TokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return b64.EncodeToString(h[:])
}

func MintRootToken(rootKey ed25519.PrivateKey, caps []Capability, ttl time.Duration) (string, error) {
	pub := rootKey.Public().(ed25519.PublicKey)
	kid := pubKeyToString(pub)

	now := time.Now()
	claims := UCANClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    kid,
			Audience:  jwt.ClaimStrings{kid},
			Subject:   "claude-peers",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
		Capabilities: caps,
		Proof:        "",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	return token.SignedString(rootKey)
}

func MintToken(signerKey ed25519.PrivateKey, audience ed25519.PublicKey, caps []Capability, ttl time.Duration, parentToken string) (string, error) {
	// Parse parent to check attenuation.
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithoutClaimsValidation())
	parentClaims := &UCANClaims{}
	_, _, err := parser.ParseUnverified(parentToken, parentClaims)
	if err != nil {
		return "", fmt.Errorf("parse parent token: %w", err)
	}

	parentCaps := make(capabilitySet)
	for _, c := range parentClaims.Capabilities {
		parentCaps[c.Resource] = true
	}

	for _, c := range caps {
		if !parentCaps[c.Resource] {
			return "", fmt.Errorf("capability %q not in parent token (attenuation violation)", c.Resource)
		}
	}

	signerPub := signerKey.Public().(ed25519.PublicKey)
	now := time.Now()
	claims := UCANClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    pubKeyToString(signerPub),
			Audience:  jwt.ClaimStrings{pubKeyToString(audience)},
			Subject:   "claude-peers",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
		Capabilities: caps,
		Proof:        TokenHash(parentToken),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	return token.SignedString(signerKey)
}

func NewTokenValidator(rootPubKey ed25519.PublicKey) *TokenValidator {
	return &TokenValidator{
		rootPubKey:  rootPubKey,
		knownTokens: make(map[string]capabilitySet),
	}
}

func (v *TokenValidator) Validate(tokenStr string) (*UCANClaims, error) {
	return v.validate(tokenStr, false)
}

// ValidateWithGrace validates a token but allows tokens that expired within the
// grace period (used by /refresh-token so machines can renew before hard lockout).
func (v *TokenValidator) ValidateWithGrace(tokenStr string, grace time.Duration) (*UCANClaims, error) {
	claims := &UCANClaims{}

	// First parse without any validation to check expiry ourselves.
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithoutClaimsValidation())
	keyFunc := func(token *jwt.Token) (any, error) {
		iss, err := token.Claims.GetIssuer()
		if err != nil {
			return nil, fmt.Errorf("get issuer: %w", err)
		}
		pub, err := pubKeyFromString(iss)
		if err != nil {
			return nil, fmt.Errorf("decode issuer key: %w", err)
		}
		return pub, nil
	}
	if _, err := parser.ParseWithClaims(tokenStr, claims, keyFunc); err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}

	// Check expiry with grace window manually.
	if claims.ExpiresAt != nil {
		expiredBy := time.Since(claims.ExpiresAt.Time)
		if expiredBy > grace {
			return nil, fmt.Errorf("token expired %s ago (grace window is %s)", expiredBy.Round(time.Second), grace)
		}
	}

	if claims.Subject != "claude-peers" {
		return nil, fmt.Errorf("invalid subject: %q", claims.Subject)
	}

	if claims.Proof == "" {
		issPub, err := pubKeyFromString(claims.Issuer)
		if err != nil {
			return nil, fmt.Errorf("decode root issuer: %w", err)
		}
		if !issPub.Equal(v.rootPubKey) {
			return nil, fmt.Errorf("root token issuer does not match root public key")
		}
	} else {
		v.mu.RLock()
		parentCaps, ok := v.knownTokens[claims.Proof]
		v.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("unknown proof: parent token not registered")
		}
		for _, c := range claims.Capabilities {
			if !parentCaps[c.Resource] {
				return nil, fmt.Errorf("capability %q not in parent (chain violation)", c.Resource)
			}
		}
	}

	// Register the (possibly-expired) token so the new one can chain from it.
	cs := make(capabilitySet)
	for _, c := range claims.Capabilities {
		cs[c.Resource] = true
	}
	hash := TokenHash(tokenStr)
	v.mu.Lock()
	v.knownTokens[hash] = cs
	v.mu.Unlock()

	return claims, nil
}

func (v *TokenValidator) validate(tokenStr string, _ bool) (*UCANClaims, error) {
	claims := &UCANClaims{}

	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithLeeway(30*time.Second),
	)

	// Keyfunc extracts issuer's public key from the iss claim.
	keyFunc := func(token *jwt.Token) (any, error) {
		iss, err := token.Claims.GetIssuer()
		if err != nil {
			return nil, fmt.Errorf("get issuer: %w", err)
		}
		pub, err := pubKeyFromString(iss)
		if err != nil {
			return nil, fmt.Errorf("decode issuer key: %w", err)
		}
		return pub, nil
	}

	_, err := parser.ParseWithClaims(tokenStr, claims, keyFunc)
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}

	if claims.Subject != "claude-peers" {
		return nil, fmt.Errorf("invalid subject: %q", claims.Subject)
	}

	if claims.Proof == "" {
		// Root token: issuer must be the root public key.
		issPub, err := pubKeyFromString(claims.Issuer)
		if err != nil {
			return nil, fmt.Errorf("decode root issuer: %w", err)
		}
		if !issPub.Equal(v.rootPubKey) {
			return nil, fmt.Errorf("root token issuer does not match root public key")
		}
	} else {
		// Delegated token: proof must reference a known parent.
		v.mu.RLock()
		parentCaps, ok := v.knownTokens[claims.Proof]
		v.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("unknown proof: parent token not registered")
		}
		for _, c := range claims.Capabilities {
			if !parentCaps[c.Resource] {
				return nil, fmt.Errorf("capability %q not in parent (chain violation)", c.Resource)
			}
		}
	}

	// Register this token on success.
	cs := make(capabilitySet)
	for _, c := range claims.Capabilities {
		cs[c.Resource] = true
	}
	hash := TokenHash(tokenStr)
	v.mu.Lock()
	v.knownTokens[hash] = cs
	v.mu.Unlock()

	return claims, nil
}

func (v *TokenValidator) RegisterToken(tokenStr string, caps []Capability) {
	cs := make(capabilitySet)
	for _, c := range caps {
		cs[c.Resource] = true
	}
	hash := TokenHash(tokenStr)
	v.mu.Lock()
	v.knownTokens[hash] = cs
	v.mu.Unlock()
}

func HasCapability(claims *UCANClaims, resource string) bool {
	for _, c := range claims.Capabilities {
		if c.Resource == resource {
			return true
		}
	}
	return false
}

func AllCapabilities() []Capability {
	resources := []string{
		"peer/register", "peer/heartbeat", "peer/unregister", "peer/set-summary",
		"peer/list", "msg/send", "msg/poll", "msg/ack",
		"events/read", "memory/read", "memory/write", "nats/subscribe",
	}
	caps := make([]Capability, len(resources))
	for i, r := range resources {
		caps[i] = Capability{Resource: r}
	}
	return caps
}

func PeerSessionCapabilities() []Capability {
	resources := []string{
		"peer/register", "peer/heartbeat", "peer/unregister", "peer/set-summary",
		"peer/list", "msg/send", "msg/poll", "msg/ack",
		"events/read", "memory/read", "memory/write",
	}
	caps := make([]Capability, len(resources))
	for i, r := range resources {
		caps[i] = Capability{Resource: r}
	}
	return caps
}

func FleetReadCapabilities() []Capability {
	resources := []string{"peer/list", "events/read", "memory/read"}
	caps := make([]Capability, len(resources))
	for i, r := range resources {
		caps[i] = Capability{Resource: r}
	}
	return caps
}

func FleetWriteCapabilities() []Capability {
	resources := []string{"peer/list", "events/read", "memory/read", "memory/write"}
	caps := make([]Capability, len(resources))
	for i, r := range resources {
		caps[i] = Capability{Resource: r}
	}
	return caps
}

func CLICapabilities() []Capability {
	resources := []string{"peer/list", "msg/send", "events/read"}
	caps := make([]Capability, len(resources))
	for i, r := range resources {
		caps[i] = Capability{Resource: r}
	}
	return caps
}
