# Revocation and suspend

Serials live in `$HOUDRY_HOME/server/pki/revoked.json`. Handshake and handlers reject revoked certs.

```bash
houdry node revoke NODE_ID
houdry node suspend NODE_ID
houdry node resume NODE_ID
```

Run these on the control-plane host (local data dir) or against an authenticated admin HTTPS URL.

- **Revoked:** cert is dead. Connects fail. The node cannot receive jobs.
- **Suspended:** cert is still valid. Heartbeats are accepted. `Fits` is false, so the scheduler never assigns work. `houdry node resume` returns the node to `ACTIVE`.

Operational status (`READY` / `BUSY` / …) is unchanged. Identity is a separate field (`UNKNOWN|PENDING|ENROLLING|CERTIFIED|ACTIVE|SUSPENDED|REVOKED`). `houdry node list` prints both, e.g. `ACTIVE/READY`.
