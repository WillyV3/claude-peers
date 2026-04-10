---
room: infra/rate-limit
subdomain: infra
source_paths: rate_limiter
architectural_health: normal
security_tier: normal
see_also:
  - broker/core.md
---

# rate_limiter.go

DOES: Per-route rate limiting. Token bucket implementation. Each broker route can have its own rate config (e.g., /msg/send is more permissive than /memory/write).

SYMBOLS:
- NewRateLimiter(capacity, refillRate) → *RateLimiter
- (l *RateLimiter) Allow(key string) → bool
- middleware wrapper that returns 429 when bucket is empty

USE WHEN: Adding a new broker route that needs rate-limiting, debugging "429 Too Many Requests" errors, tuning capacity for a hot endpoint.
