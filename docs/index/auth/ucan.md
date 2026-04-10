---
room: auth/ucan
subdomain: auth
source_paths: auth_ucan, auth_ucan_keys
architectural_health: normal
security_tier: sensitive
see_also:
  - broker/core.md
  - auth/middleware.md
  - fleet/state.md
hot_paths:
  - auth_ucan.go
  - auth_ucan_keys.go
committee_notes: "UCAN signing logic. Affects entire fleet trust model. ANY change must run refresh_token_test.go AND auth_ucan_test.go AND auth_challenge_test.go before merging."
---

# auth_ucan.go

DOES: UCAN (User-Controlled Authorization Network) token issuance and verification. Implements the JWT envelope with EdDSA signing. Each token has `iss` (issuer DID), `sub` (subject), `aud` (audience), `exp`/`iat` (validity window), `cap` (capabilities/resources), and `prf` (proof — reference to parent delegation). Tokens form a delegation chain rooted at the broker's root key.

SYMBOLS:
- IssueToken(parentToken, audPub, role, ttl) → (jwt, error) — creates a new token signed by parentToken's key, delegating to audPub
- VerifyToken(jwt, expectedAud) → (*Claims, error) — full chain validation: signature, exp, prf trace back to a known root
- RefreshToken(currentJwt) → (newJwt, error) — issues a fresh token with extended exp, same delegation chain
- ParseToken(jwt) → (*Claims, error) — parse without verification (used for inspection)

PATTERNS:
- **Delegation chain validation** — every token's `prf` field references its parent. VerifyToken walks the chain from leaf to a known root. Caretaker's failure mode: if you issue a token from omarchy's admin token but the broker's root key doesn't recognize that admin token, you get BAD_PROOF (invalid delegation chain). Issue tokens from the machine that holds the root key (ubuntu-homelab) to avoid this.
- **24h default TTL** — current default expiry is 24 hours from iat. T3 in willybrain TODO is to add a `--ttl` flag.

# auth_ucan_keys.go

DOES: Ed25519 keypair generation and storage. Each machine has its own identity keypair under $HOME/.config/claude-peers/identity.{pem,pub}. The broker has a root keypair that signs the root token.

SYMBOLS:
- GenerateIdentity() → (privKey, pubKey, error) — fresh Ed25519 keypair
- LoadIdentity(path) → (privKey, error)
- SaveIdentity(path, privKey)
- LoadRootToken(path) → (string, error) — reads the broker's root token from disk

USE WHEN: Setting up a new machine in the fleet, rotating an identity, debugging signature verification failures.

ANTI-PATTERN: Don't share identity keys across machines. Each peer has its own. Cross-machine signing means delegation chain mismatches.
