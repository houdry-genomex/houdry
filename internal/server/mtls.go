package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"houdry/internal/config"
	"houdry/internal/pki"
)

type ctxClientKey struct{}

// WithHTTPClient attaches an HTTP client (typically mTLS) to ctx.
func WithHTTPClient(ctx context.Context, c *http.Client) context.Context {
	return context.WithValue(ctx, ctxClientKey{}, c)
}

// DetachedClient copies the mTLS client from parent onto a fresh context that
// is not cancelled when parent is. Job reports after Execute must use this:
// context.Background() alone drops the client certificate and the plane
// answers 401 "client certificate required".
func DetachedClient(parent context.Context) context.Context {
	if parent == nil {
		return context.Background()
	}
	return WithHTTPClient(context.Background(), clientFrom(parent))
}

func clientFrom(ctx context.Context) *http.Client {
	if ctx != nil {
		if c, ok := ctx.Value(ctxClientKey{}).(*http.Client); ok && c != nil {
			return c
		}
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func clientFor(ctx context.Context, serverURL string) *http.Client {
	if ctx != nil {
		if c, ok := ctx.Value(ctxClientKey{}).(*http.Client); ok && c != nil {
			return c
		}
	}
	if strings.HasPrefix(strings.ToLower(HTTPSURL(serverURL)), "https://") {
		if ctx == nil {
			ctx = context.Background()
		}
		if trusted, _, err := Trust(ctx, serverURL); err == nil {
			return clientFrom(trusted)
		}
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// HTTPSURL rewrites http:// control-plane URLs to https://.
func HTTPSURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if strings.HasPrefix(raw, "http://") {
		return "https://" + strings.TrimPrefix(raw, "http://")
	}
	return raw
}

func mtlsClient(caPEM, certPEM, keyPEM []byte) (*http.Client, error) {
	cfg, err := pki.ClientTLS(caPEM, certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: cfg,
		},
	}, nil
}

func caOnlyClient(caPEM []byte) (*http.Client, error) {
	return mtlsClient(caPEM, nil, nil)
}

func (s *Server) verifyPeer(raw [][]byte, _ [][]*x509.Certificate) error {
	if len(raw) == 0 {
		return nil
	}
	cert, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return err
	}
	if s.bundle == nil {
		return fmt.Errorf("pki not ready")
	}
	if err := cert.CheckSignatureFrom(s.bundle.CACert); err != nil {
		// Ignore non-Houdry certs at the handshake. Node APIs still require a
		// CA-signed cert in checkNodeCert; /v1 does not.
		return nil
	}
	now := time.Now().UTC()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return fmt.Errorf("certificate not currently valid")
	}
	if s.revoke != nil && s.revoke.IsRevoked(pki.SerialHex(cert)) {
		return fmt.Errorf("certificate revoked")
	}
	nodeID := cert.Subject.CommonName
	if nodeID == "" {
		return fmt.Errorf("certificate has no node id")
	}
	// Do not require the node to already be in the registry. Ctrl+C on
	// gpu register calls leave, which removes the row; the cert is still
	// valid and join must be able to complete the TLS handshake.
	if n, ok := s.store.Get(nodeID); ok && n.Identity == IdentityRevoked {
		return fmt.Errorf("node revoked")
	}
	return nil
}

func (s *Server) requireNodeCert(w http.ResponseWriter, r *http.Request, allowSuspended bool) (string, bool) {
	return s.checkNodeCert(w, r, allowSuspended, false)
}

func (s *Server) requireJoinCert(w http.ResponseWriter, r *http.Request) (string, bool) {
	return s.checkNodeCert(w, r, false, true)
}

func (s *Server) checkNodeCert(w http.ResponseWriter, r *http.Request, allowSuspended, allowUnknown bool) (string, bool) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		s.audit.Log("AUTHENTICATION_FAILURE", "", "", clientIP(r), "missing client certificate")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "client certificate required"})
		return "", false
	}
	cert := r.TLS.PeerCertificates[0]
	if s.bundle != nil {
		if err := cert.CheckSignatureFrom(s.bundle.CACert); err != nil {
			s.audit.Log("AUTHENTICATION_FAILURE", cert.Subject.CommonName, pki.FingerprintSHA256(cert), clientIP(r), "not a Houdry node certificate")
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "client certificate required"})
			return "", false
		}
	}
	nodeID := cert.Subject.CommonName
	if nodeID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "certificate has no node id"})
		return "", false
	}
	n, ok := s.store.Get(nodeID)
	if !ok {
		if allowUnknown {
			return nodeID, true
		}
		s.audit.Log("AUTHENTICATION_FAILURE", nodeID, pki.FingerprintSHA256(cert), clientIP(r), "unknown node")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "node not enrolled"})
		return "", false
	}
	if n.Identity == IdentityRevoked || (s.revoke != nil && s.revoke.IsRevoked(pki.SerialHex(cert))) {
		s.audit.Log("AUTHENTICATION_FAILURE", nodeID, n.CertFingerprint, clientIP(r), "revoked")
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "node revoked"})
		return "", false
	}
	if n.Identity == IdentitySuspended && !allowSuspended {
		s.audit.Log("HEARTBEAT_REJECTED", nodeID, n.CertFingerprint, clientIP(r), "suspended")
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "node suspended"})
		return "", false
	}
	if n.CertSerial != "" && n.CertSerial != pki.SerialHex(cert) {
		// Allow a just-rotated cert: serial mismatch is OK if CA-signed and not revoked.
	}
	return nodeID, true
}

func tlsHTTPClient(cfg *tls.Config) *http.Client {
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{TLSClientConfig: cfg},
	}
}

// NewTLSClient trusts caPEM and optionally presents a node certificate.
func NewTLSClient(caPEM, certPEM, keyPEM []byte) (*http.Client, error) {
	return mtlsClient(caPEM, certPEM, keyPEM)
}

// Trust attaches a CA-only HTTPS client after loading or TOFU-fetching the Houdry CA.
func Trust(ctx context.Context, serverURL string) (context.Context, string, error) {
	serverURL = HTTPSURL(serverURL)
	ca, err := loadOrFetchCA(ctx, serverURL)
	if err != nil {
		return ctx, serverURL, err
	}
	c, err := caOnlyClient(ca)
	if err != nil {
		return ctx, serverURL, err
	}
	return WithHTTPClient(ctx, c), serverURL, nil
}

// DialNode enrolls this machine if needed and returns an mTLS client context.
func DialNode(ctx context.Context, serverURL, enrollToken, nodeID string) (context.Context, string, error) {
	if nodeID == "" {
		return ctx, serverURL, fmt.Errorf("node ID is required")
	}
	serverURL = HTTPSURL(serverURL)
	host, _ := os.Hostname()
	mat, err := pki.EnsureNode(pki.NodeDir(houdryHome()), nodeID, host)
	if err != nil {
		return ctx, serverURL, err
	}
	ca, err := loadOrFetchCA(ctx, serverURL)
	if err != nil {
		return ctx, serverURL, fmt.Errorf("trust control plane: %w", err)
	}
	needEnroll := !mat.HasCertificate() || !nodeCertIssuedBy(mat.CertPEM, ca) || enrollToken != ""
	if needEnroll {
		if enrollToken == "" {
			if !mat.HasCertificate() {
				return ctx, serverURL, fmt.Errorf("enroll: a one-time token is required (houdry node enroll create on the control plane)")
			}
			// Keep the existing cert and try join. After leave the plane
			// must still accept a CA-signed client cert (see verifyPeer).
		} else {
			pub, err := caOnlyClient(ca)
			if err != nil {
				return ctx, serverURL, err
			}
			out, err := Enroll(WithHTTPClient(ctx, pub), serverURL, enrollToken, nodeID, string(mat.CSRPEM))
			if err != nil {
				if !(mat.HasCertificate() && nodeCertIssuedBy(mat.CertPEM, ca) && strings.Contains(err.Error(), "already enrolled")) {
					return ctx, serverURL, fmt.Errorf("enroll: %w", err)
				}
			} else if err := mat.SaveEnrollment([]byte(out.Certificate), []byte(out.CA)); err != nil {
				return ctx, serverURL, err
			} else {
				ca = mat.CAPEM
			}
		}
	}
	c, err := mtlsClient(ca, mat.CertPEM, mat.KeyPEM())
	if err != nil {
		return ctx, serverURL, err
	}
	return WithHTTPClient(ctx, c), serverURL, nil
}

func nodeCertIssuedBy(certPEM, caPEM []byte) bool {
	cert, err := pki.ParseCertPEM(certPEM)
	if err != nil {
		return false
	}
	ca, err := pki.ParseCertPEM(caPEM)
	if err != nil {
		return false
	}
	return cert.CheckSignatureFrom(ca) == nil
}

func houdryHome() string {
	return config.Dir()
}

func isLoopbackURL(raw string) bool {
	u, err := url.Parse(HTTPSURL(raw))
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func caPinPath(serverURL string) string {
	host := "default"
	if u, err := url.Parse(HTTPSURL(serverURL)); err == nil && u.Host != "" {
		host = strings.NewReplacer(":", "_", "[", "", "]", "").Replace(u.Host)
	}
	return filepath.Join(houdryHome(), "node", "ca-"+host+".crt")
}

func loadOrFetchCA(ctx context.Context, serverURL string) ([]byte, error) {
	serverURL = HTTPSURL(serverURL)
	home := houdryHome()

	// A local `houdry serve` CA is only valid for this machine. Using it to
	// join a WiFi control plane is what produced:
	//   signature algorithm specifies an ECDSA public key, but have public
	//   key of type ed25519.PublicKey
	// after 0.6.8 (Ed25519) → 0.6.9 (P-256) on the other laptop.
	if isLoopbackURL(serverURL) {
		if b, err := os.ReadFile(filepath.Join(home, "server", "pki", "root_ca.crt")); err == nil && len(b) > 0 {
			if caVerifiesServer(ctx, serverURL, b) {
				return b, nil
			}
		}
	}

	for _, p := range []string{caPinPath(serverURL), filepath.Join(home, "node", "ca.crt")} {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 && caVerifiesServer(ctx, serverURL, b) {
			return b, nil
		}
	}

	pem, err := FetchCA(ctx, serverURL)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, "node")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(caPinPath(serverURL), pem, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "ca.crt"), pem, 0o644)
	return pem, nil
}

func caVerifiesServer(ctx context.Context, serverURL string, caPEM []byte) bool {
	c, err := caOnlyClient(caPEM)
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(serverURL, "/")+"/.well-known/houdry.json", nil)
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode < 500
}

// FetchCA retrieves the control-plane CA. The first contact uses TOFU
// (skip-verify) and the caller is expected to persist the PEM.
func FetchCA(ctx context.Context, serverURL string) ([]byte, error) {
	c := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // TOFU: pin the PEM after this fetch
				MinVersion:         tls.VersionTLS13,
			},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(HTTPSURL(serverURL), "/")+"/v1/pki/ca", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch CA: server returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if _, err := pki.ParseCertPEM(data); err != nil {
		return nil, fmt.Errorf("fetch CA: %w", err)
	}
	return data, nil
}

func AdminSetIdentity(ctx context.Context, serverURL, token, path, nodeID string) (Node, error) {
	var out Node
	if err := postJSONInto(ctx, serverURL, token, path, map[string]string{"node_id": nodeID}, &out); err != nil {
		return Node{}, err
	}
	return out, nil
}
