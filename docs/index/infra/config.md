---
room: infra/config
subdomain: infra
source_paths: config, helpers
architectural_health: normal
security_tier: normal
see_also:
  - broker/core.md
hot_paths:
  - config.go
---

# config.go

DOES: Config file loader. Reads ~/.config/claude-peers/config.json into a typed Config struct. Schema includes role (broker/client), broker_url, listen, machine_name, db_path, stale_timeout, nats_url, llm_base_url, llm_model, llm_api_key, nats_token.

SYMBOLS:
- LoadConfig(path) → (*Config, error)
- (c *Config) SaveConfig(path) → error
- DefaultConfig(role) → *Config

CONFIG: ~/.config/claude-peers/config.json

# helpers.go

DOES: Misc utilities used across the codebase — base64url encode/decode, time formatting, error wrapping, HTTP client helpers.
