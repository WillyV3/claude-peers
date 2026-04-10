---
subdomain: auth
source_paths: auth_ucan.go, auth_ucan_keys.go, auth_middleware.go, auth_challenge_test.go
---

# auth — UCAN tokens and middleware

Source paths: auth_ucan, auth_ucan_keys, auth_middleware

## TASK → LOAD

| Task | Load |
|------|------|
| Issue / verify / refresh a UCAN token | ucan.md |
| Add an auth-protected route | middleware.md |
| Modify the delegation chain | ucan.md |
| Generate a new identity keypair | ucan.md |

## Rooms

| Room | Covers | Files |
|------|--------|-------|
| ucan.md | auth_ucan, auth_ucan_keys | 2 |
| middleware.md | auth_middleware | 1 |
