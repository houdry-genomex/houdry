# PKI

Houdry ships a built-in certificate authority. There is no external CA and no HTTP listener on the control-plane port.

On first `houdry serve`, the plane writes:

```
$HOUDRY_HOME/server/pki/root_ca.key   # 0600
$HOUDRY_HOME/server/pki/root_ca.crt   # 0644
$HOUDRY_HOME/server/pki/server.key    # 0600
$HOUDRY_HOME/server/pki/server.crt    # 0644
```

All keys are ECDSA P-256 (`crypto/ecdsa`). Ed25519 from 0.6.8 is rotated on the next `houdry serve` / `gpu register` so Windows, Electron, and Python TLS clients can handshake. The Root CA signs the server certificate and every node certificate. Files are reused across restarts. The server certificate is reissued when its SANs no longer cover localhost, `127.0.0.1`, `::1`, the hostname, and current non-loopback IPs — so LAN Agent URLs such as `https://10.x:18080` verify.

`NotBefore` is set five minutes in the past so a mildly skewed clock still accepts a fresh cert.

OpenAI `/v1` and enrollment use the same HTTPS listener. Clients that only chat never present a client certificate. Node APIs do.

See also [Enrollment](Enrollment.md), [mTLS](mTLS.md), and [Threat-Model](Threat-Model.md).
