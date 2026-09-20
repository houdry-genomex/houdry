# mTLS

One HTTPS listener (`--listen`, default `0.0.0.0:8080`; desktop `18080`). There is no HTTP fallback on that port.

TLS 1.3, HTTP/1.1 only (HTTP/2 is disabled on the listener), session tickets disabled (`SessionTicketsDisabled: true`). The listener sends `CertificateRequest` **only** when the client advertises ALPN `houdry` (GPU `houdry gpu register` / node cert). Node APIs still require a CA-signed client cert on the HTTP request (`checkNodeCert`); skipping `CertificateRequest` for non-GPU clients does not open join/claim/result.

`tls: bad record MAC` logged by the control plane had two distinct causes, fixed separately:

1. **CertificateRequest sent to a non-GPU client** (0.6.13). Agent's TLS stacks (Electron `https.get`, Python httpx) don't handle the plane's optional client-cert request the way Go/GPU workers do.
2. **Session-ticket resumption across a `houdry serve` restart** (0.6.14). Go's TLS session-ticket keys are random per process. A client that cached a ticket from a *previous* serve process and tries to resume it against a newly-started one cannot have that ticket decrypted — the new process logs `bad record MAC` for what looks like an ordinary fresh connection from that client's IP, with no relation to the client's TLS stack or ALPN. Disabling session tickets forces a full handshake on every connection, so a `serve` restart can never trigger this.

A presented **Houdry-issued** cert must be unexpired and not revoked. Node APIs (join, heartbeat, claim, result) additionally require that cert to be signed by the Houdry Root CA and to match a node. OpenAI `/v1` does not require a client cert.

Agent and browsers complete HTTPS without a client cert. Those clients cannot claim jobs or report results.

## No client cert (HTTPS only)

`GET /healthz`, `/.well-known/houdry.json`, `/`, `/install.*`, `/download/`, `/files/`, `GET /v1/pki/ca`, OpenAI `POST /v1/chat/completions` + `GET /v1/models`, `POST /v1/nodes/enroll`. Cluster/node list GETs still accept optional `X-Houdry-Token`.

## Require node cert

Heartbeat, join-after-enroll, drain, leave, claim, result, inventory updates, `POST /v1/nodes/renew`.

Well-known JSON includes `"tls": true` and `"enroll": "/v1/nodes/enroll"`.

Houdry Agent trusts `$HOUDRY_HOME/server/pki/root_ca.crt` after a local spawn. A remote LAN plane is TOFU-pinned on first `GET /v1/pki/ca`. Python OpenAI clients should set `SSL_CERT_FILE` / `REQUESTS_CA_BUNDLE` to that CA — do not disable TLS verify on the production path.

`--token` remains extra auth on admin GETs. It is not node identity.
