# Threat model

Houdry assumes a private LAN (home/lab/office WiFi or Tailscale), not the public internet. The plane still requires HTTPS so a neighbor on the same SSID cannot silently impersonate `houdry serve`.

## In scope

- Stolen or replayed join URL without a token
- Reused enrollment token
- Forged node cert (unknown CA, wrong CN, expired, revoked)
- Scheduler assigning work to a suspended or revoked identity
- Chat clients talking to a fake OpenAI `/v1` on the LAN (TOFU / local CA pin)

## Out of scope

- A fully compromised control-plane host (it holds the Root CA key)
- Physical theft of `root_ca.key` or `private.key` with 0600 perms on a logged-in admin session
- Availability attacks beyond the enroll rate limit

## Mitigations

| Threat | Control |
| --- | --- |
| Anyone can join | Hashed single-use `HDRY_` token, 10m TTL, per-IP enroll limit |
| HTTP scrape of jobs | HTTPS everywhere, no HTTP fallback |
| Fake node | mTLS; `Fits` requires `identity == ACTIVE` |
| Stale cert | 30d lifetime, renew at 7d, revoke list |
| Agent pinning a WiFi IP | Discovery rewrite to `https://127.0.0.1` when the plane is local |
| Audit gaps | Append-only `$HOUDRY_HOME/server/audit.log` (JSONL, non-blocking) |

Ollama, GPU detectors, job claim/result JSON, and scheduler locality scoring are unchanged. Identity is only an eligibility gate in `Fits`.
