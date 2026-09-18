// Package pki is Houdry's built-in certificate authority.
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	caKeyName      = "root_ca.key"
	caCertName     = "root_ca.crt"
	serverKeyName  = "server.key"
	serverCertName = "server.crt"

	caValidity = 10 * 365 * 24 * time.Hour
	clockSkew  = 5 * time.Minute
	keyPerm    = 0o600
	certPerm   = 0o644
)

// Bundle is the control-plane PKI material loaded from disk.
type Bundle struct {
	Dir        string
	CACert     *x509.Certificate
	CAKey      crypto.Signer
	ServerCert *x509.Certificate
	ServerKey  crypto.Signer
}

// Ensure loads an existing PKI directory or creates Root CA + server cert.
// Server cert is reissued when SANs no longer cover the current host.
// Ed25519 material from 0.6.8 is rotated to ECDSA P-256 so Windows / Electron
// / Python TLS clients can handshake (they omit ed25519 in signature_algorithms).
func Ensure(dir string, serverLifetime time.Duration) (*Bundle, error) {
	if serverLifetime <= 0 {
		serverLifetime = 30 * 24 * time.Hour
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	b := &Bundle{Dir: dir}
	if err := b.loadOrCreateCA(); err != nil {
		return nil, err
	}
	if err := b.loadOrCreateServer(serverLifetime); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Bundle) CACertPEM() []byte { return encodeCertPEM(b.CACert.Raw) }
func (b *Bundle) CAKeyPath() string { return filepath.Join(b.Dir, caKeyName) }
func (b *Bundle) CACertPath() string { return filepath.Join(b.Dir, caCertName) }

func generateP256() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

func isP256(s crypto.Signer) bool {
	k, ok := s.(*ecdsa.PrivateKey)
	return ok && k.Curve == elliptic.P256()
}

func (b *Bundle) loadOrCreateCA() error {
	certPath := filepath.Join(b.Dir, caCertName)
	keyPath := filepath.Join(b.Dir, caKeyName)
	if fileExists(certPath) && fileExists(keyPath) {
		cert, err := loadCert(certPath)
		if err != nil {
			return fmt.Errorf("load CA cert: %w", err)
		}
		key, err := loadKey(keyPath)
		if err != nil {
			return fmt.Errorf("load CA key: %w", err)
		}
		if isP256(key) && cert.PublicKeyAlgorithm == x509.ECDSA {
			b.CACert, b.CAKey = cert, key
			return nil
		}
	}
	priv, err := generateP256()
	if err != nil {
		return err
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Houdry Root CA", Organization: []string{"Houdry"}},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
		MaxPathLenZero:        false,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	if err := os.WriteFile(certPath, encodeCertPEM(der), certPerm); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, encodeKeyPEM(priv), keyPerm); err != nil {
		return err
	}
	b.CACert, b.CAKey = cert, priv
	return nil
}

func (b *Bundle) loadOrCreateServer(lifetime time.Duration) error {
	certPath := filepath.Join(b.Dir, serverCertName)
	keyPath := filepath.Join(b.Dir, serverKeyName)
	sans := CurrentSANs()
	if fileExists(certPath) && fileExists(keyPath) {
		cert, err := loadCert(certPath)
		if err != nil {
			return fmt.Errorf("load server cert: %w", err)
		}
		key, err := loadKey(keyPath)
		if err != nil {
			return fmt.Errorf("load server key: %w", err)
		}
		if isP256(key) && cert.PublicKeyAlgorithm == x509.ECDSA && sansCover(cert, sans) && time.Until(cert.NotAfter) > time.Hour {
			b.ServerCert, b.ServerKey = cert, key
			return nil
		}
	}
	priv, err := generateP256()
	if err != nil {
		return err
	}
	cert, err := b.issue(IssueOpts{
		Pub:        &priv.PublicKey,
		CN:         "houdry-server",
		DNS:        sans.DNS,
		IPs:        sans.IPs,
		Lifetime:   lifetime,
		ServerAuth: true,
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(certPath, encodeCertPEM(cert.Raw), certPerm); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, encodeKeyPEM(priv), keyPerm); err != nil {
		return err
	}
	b.ServerCert, b.ServerKey = cert, priv
	return nil
}

// IssueOpts describes a leaf certificate signed by the Root CA.
type IssueOpts struct {
	Pub        crypto.PublicKey
	CN         string
	DNS        []string
	IPs        []net.IP
	OU         []string
	Lifetime   time.Duration
	ServerAuth bool
	ClientAuth bool
}

func (b *Bundle) issue(opts IssueOpts) (*x509.Certificate, error) {
	if opts.Lifetime <= 0 {
		opts.Lifetime = 30 * 24 * time.Hour
	}
	if !opts.ServerAuth && !opts.ClientAuth {
		opts.ClientAuth = true
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	ku := x509.KeyUsageDigitalSignature
	var eku []x509.ExtKeyUsage
	if opts.ServerAuth {
		eku = append(eku, x509.ExtKeyUsageServerAuth)
	}
	if opts.ClientAuth {
		eku = append(eku, x509.ExtKeyUsageClientAuth)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:         opts.CN,
			Organization:       []string{"Houdry"},
			OrganizationalUnit: opts.OU,
		},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(opts.Lifetime),
		KeyUsage:              ku,
		ExtKeyUsage:           eku,
		BasicConstraintsValid: true,
		DNSNames:              opts.DNS,
		IPAddresses:           opts.IPs,
		URIs:                  nil,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, b.CACert, opts.Pub, b.CAKey)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

// SignCSR parses a PEM CSR, checks CN == nodeID, and issues a client cert.
func (b *Bundle) SignCSR(csrPEM []byte, nodeID string, lifetime time.Duration) (*x509.Certificate, error) {
	csr, err := ParseCSR(csrPEM)
	if err != nil {
		return nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("invalid CSR signature: %w", err)
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, fmt.Errorf("CSR public key must be ECDSA P-256")
	}
	if csr.Subject.CommonName != nodeID {
		return nil, fmt.Errorf("CSR CN %q does not match node_id %q", csr.Subject.CommonName, nodeID)
	}
	return b.issue(IssueOpts{
		Pub:        pub,
		CN:         nodeID,
		DNS:        csr.DNSNames,
		IPs:        csr.IPAddresses,
		OU:         csr.Subject.OrganizationalUnit,
		Lifetime:   lifetime,
		ClientAuth: true,
	})
}

// TLSConfig is the control-plane listener config: server cert + optional client certs.
func (b *Bundle) TLSConfig(verify func(raw [][]byte, verified [][]*x509.Certificate) error) *tls.Config {
	cert, err := tls.X509KeyPair(encodeCertPEM(b.ServerCert.Raw), encodeKeyPEM(b.ServerKey))
	if err != nil {
		// X509KeyPair only fails on malformed PEM; we just wrote valid material.
		panic(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(b.CACert)
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		// Request, don't verify, at the handshake. Agent / Electron / Windows
		// Schannel often auto-send a store cert when we advertise optional
		// client-auth; VerifyClientCertIfGiven then fails those probes with
		// "bad record MAC" / "bad certificate" even though /v1 does not need
		// a node cert. Node APIs still check the cert in requireNodeCert.
		ClientAuth: tls.RequestClientCert,
		// HTTP/1.1 only: h2 + CertificateRequest trips Schannel/Electron on
		// LAN private-CA URLs (bursts of tls: bad record MAC in serve logs).
		NextProtos: []string{"http/1.1"},
	}
	if verify != nil {
		cfg.VerifyPeerCertificate = verify
	}
	return cfg
}

// ClientTLSConfig builds an mTLS client that trusts this CA and presents certPEM/keyPEM.
func (b *Bundle) ClientTLSConfig(certPEM, keyPEM []byte) (*tls.Config, error) {
	return ClientTLS(b.CACertPEM(), certPEM, keyPEM)
}

// ClientTLS trusts caPEM and optionally presents a client certificate.
func ClientTLS(caPEM, certPEM, keyPEM []byte) (*tls.Config, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("invalid CA PEM")
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
	}
	if len(certPEM) > 0 && len(keyPEM) > 0 {
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, err
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

// FingerprintSHA256 returns the SHA-256 fingerprint of a cert's DER.
func FingerprintSHA256(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return "SHA256:" + hex.EncodeToString(sum[:])
}

func SerialHex(cert *x509.Certificate) string {
	return hex.EncodeToString(cert.SerialNumber.Bytes())
}

func ParseCSR(pemBytes []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("malformed CSR PEM")
	}
	return x509.ParseCertificateRequest(block.Bytes)
}

func ParseCertPEM(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("malformed certificate PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

func EncodeCertPEM(cert *x509.Certificate) []byte {
	return encodeCertPEM(cert.Raw)
}

func EncodeKeyPEM(key any) []byte {
	return encodeKeyPEM(key)
}

func encodeCertPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func encodeKeyPEM(key any) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func loadCert(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseCertPEM(b)
}

func loadKey(path string) (crypto.Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("malformed key PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	s, ok := k.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("key is not a signer")
	}
	return s, nil
}

func randomSerial() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	if n.Sign() == 0 {
		n = big.NewInt(1)
	}
	return n, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// SANs is the set of names bound to the server certificate.
type SANs struct {
	DNS []string
	IPs []net.IP
}

func CurrentSANs() SANs {
	dns := []string{"localhost"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	if h, err := os.Hostname(); err == nil && h != "" && h != "localhost" {
		dns = append(dns, h)
	}
	ifaces, _ := net.InterfaceAddrs()
	for _, a := range ifaces {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP == nil || ipn.IP.IsLoopback() {
			continue
		}
		ips = append(ips, ipn.IP)
	}
	return SANs{DNS: uniqueStrings(dns), IPs: uniqueIPs(ips)}
}

func sansCover(cert *x509.Certificate, want SANs) bool {
	haveDNS := map[string]bool{}
	for _, d := range cert.DNSNames {
		haveDNS[d] = true
	}
	for _, d := range want.DNS {
		if !haveDNS[d] {
			return false
		}
	}
	haveIP := map[string]bool{}
	for _, ip := range cert.IPAddresses {
		haveIP[ip.String()] = true
	}
	for _, ip := range want.IPs {
		if ip == nil {
			continue
		}
		if !haveIP[ip.String()] {
			return false
		}
	}
	return true
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func uniqueIPs(in []net.IP) []net.IP {
	seen := map[string]bool{}
	var out []net.IP
	for _, ip := range in {
		if ip == nil {
			continue
		}
		k := ip.String()
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, ip)
	}
	return out
}
