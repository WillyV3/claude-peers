---
room: auth/middleware
subdomain: auth
source_paths: auth_middleware
architectural_health: normal
security_tier: sensitive
see_also:
  - broker/core.md
  - auth/ucan.md
hot_paths:
  - auth_middleware.go
---

# auth_middleware.go

DOES: HTTP middleware that wraps every broker route. Reads the Authorization header, validates the JWT via auth_ucan.VerifyToken, checks that the token's `cap` field includes the resource being accessed (e.g., "peer/register"), and either passes the request through or returns 401/403.

SYMBOLS:
- AuthMiddleware(handler, requiredCap) → http.Handler — wraps a handler with cap-checking
- extractToken(r) → (string, error) — pulls JWT from Authorization header

PATTERNS:
- **Capability gating per route** — each broker handler is wrapped with the specific capability it requires (peer/register, msg/send, memory/write, etc). A token issued without that cap can't hit the route even if the signature is valid.
- **401 on bad auth, 403 on insufficient cap** — distinct error codes so the client can tell "your token is wrong" from "your token is right but you can't do this"

USE WHEN: Adding a new broker route (must wire it through this middleware) or debugging "BAD_PROOF" / "INSUFFICIENT_CAPS" errors.
