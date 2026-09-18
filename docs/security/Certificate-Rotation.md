# Certificate rotation

Node certificates last 30 days by default and renew 7 days before expiry (`security.certificates` in `$HOUDRY_HOME/server/security.yaml`).

The agent calls `POST /v1/nodes/renew` over mTLS with a CSR for the **same** key. The plane issues a new cert; the node replaces `node.crt` and keeps the key.

On renew failure the current cert is kept and an audit warning is written. Heartbeats continue until expiry; after expiry the handshake is rejected and the node must enroll again with a fresh token.

```bash
houdry node cert info
```

prints this machine's certificate subject, serial, and not-after.
