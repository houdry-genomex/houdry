package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"houdry/internal/pki"
)

type ctxClientKey struct{}

// WithHTTPClient attaches an HTTP client (typically mTLS) to ctx.
func WithHTTPClient(ctx context.Context, c *http.Client) context.Context {
	return context.WithValue(ctx, ctxClientKey{}, c)
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
		return fmt.Errorf("certificate not signed by Houdry CA")
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
	n, ok := s.store.Get(nodeID)
	if !ok {
		return fmt.Errorf("unknown node")
	}
	if n.Identity == IdentityRevoked {
		return fmt.Errorf("node revoked")
	}
	return nil
}

func (s *Server) requireNodeCert(w http.ResponseWriter, r *http.Request, allowSuspended bool) (string, bool) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		s.audit.Log("AUTHENTICATION_FAILURE", "", "", clientIP(r), "missing client certificate")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "client certificate required"})
		return "", false
	}
	cert := r.TLS.PeerCertificates[0]
	nodeID := cert.Subject.CommonName
	if nodeID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "certificate has no node id"})
		return "", false
	}
	n, ok := s.store.Get(nodeID)
	if !ok {
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
	if !mat.HasCertificate() {
		ca, err := loadOrFetchCA(ctx, serverURL)
		if err != nil {
			return ctx, serverURL, fmt.Errorf("trust control plane: %w", err)
		}
		pub, err := caOnlyClient(ca)
		if err != nil {
			return ctx, serverURL, err
		}
		out, err := Enroll(WithHTTPClient(ctx, pub), serverURL, enrollToken, nodeID, string(mat.CSRPEM))
		if err != nil {
			return ctx, serverURL, fmt.Errorf("enroll: %w", err)
		}
		if err := mat.SaveEnrollment([]byte(out.Certificate), []byte(out.CA)); err != nil {
			return ctx, serverURL, err
		}
	}
	c, err := mtlsClient(mat.CAPEM, mat.CertPEM, mat.KeyPEM())
	if err != nil {
		return ctx, serverURL, err
	}
	return WithHTTPClient(ctx, c), serverURL, nil
}

func houdryHome() string {
	if v := os.Getenv("HOODRY_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "."
	}
	return filepath.Join(home, ".houdry")
}

func loadOrFetchCA(ctx context.Context, serverURL string) ([]byte, error) {
	home := houdryHome()
	for _, p := range []string{
		filepath.Join(home, "server", "pki", "root_ca.crt"),
		filepath.Join(home, "node", "ca.crt"),
	} {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			return b, nil
		}
	}
	pem, err := FetchCA(ctx, serverURL)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, "node")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "ca.crt"), pem, 0o644)
	return pem, nil
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
