# mTLS

One HTTPS listener (`--listen`, default `0.0.0.0:8080`; desktop `18080`). There is no HTTP fallback on that port.

TLS 1.3. `ClientAuth: VerifyClientCertIfGiven`. Presented client certs must be signed by the Houdry Root CA, unexpired, not revoked, and match a registered node.

## No client cert (HTTPS only)

`GET /healthz`, `/.well-known/houdry.json`, `/`, `/install.*`, `/download/`, `/files/`, `GET /v1/pki/ca`, OpenAI `POST /v1/chat/completions` + `GET /v1/models`, `POST /v1/nodes/enroll`. Cluster/node list GETs still accept optional `X-Houdry-Token`.

## Require node cert

Heartbeat, join-after-enroll, drain, leave, claim, result, inventory updates, `POST /v1/nodes/renew`.

Well-known JSON includes `"tls": true` and `"enroll": "/v1/nodes/enroll"`.

Houdry Agent trusts `$HOUDRY_HOME/server/pki/root_ca.crt` after a local spawn. A remote LAN plane is TOFU-pinned on first `GET /v1/pki/ca`. Python OpenAI clients should set `SSL_CERT_FILE` / `REQUESTS_CA_BUNDLE` to that CA — do not disable TLS verify on the production path.

`--token` remains extra auth on admin GETs. It is not node identity.
