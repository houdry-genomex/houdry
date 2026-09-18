# Enrollment

GPU hosts join with a one-time token, not the optional admin `--token`.

```bash
houdry node enroll create
# prints HDRY_<64 hex chars>  TTL 10m  uses 1
```

Tokens are stored hashed in `$HOUDRY_HOME/server/enrollment.json`. Plaintext exists only in the admin's terminal. Default TTL is 10 minutes and one use (`security.yaml`).

`houdry node join` / `houdry gpu register` on an empty `~/.houdry/node/` directory:

1. Generate ECDSA P-256 `private.key` / `public.pem` / `node.csr` (CN = node id, DNS SAN = hostname).
2. `POST /v1/nodes/enroll` over HTTPS with `{token, csr, node_id}` and no client cert.
3. Persist `node.crt` + `ca.crt`.
4. Heartbeat, claim, and result use mTLS.

Rejected: bad PEM, bad signature, expired or reused token, CSR CN ≠ `node_id`, a second active cert for a different key.

In-memory per-IP rate limit on `/v1/nodes/enroll` (default 5 attempts). Token consume is replay protection.

Share the token with the GPU host over a side channel. Do not put it in `Options.Token` / `--token`.
