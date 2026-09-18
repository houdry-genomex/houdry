package pki

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsurePersistsAndReusesCA(t *testing.T) {
	dir := t.TempDir()
	b1, err := Ensure(dir, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if b1.CACert.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("CA alg=%s", b1.CACert.PublicKeyAlgorithm)
	}
	b2, err := Ensure(dir, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if FingerprintSHA256(b1.CACert) != FingerprintSHA256(b2.CACert) {
		t.Fatal("CA regenerated on restart")
	}
	if FingerprintSHA256(b1.ServerCert) != FingerprintSHA256(b2.ServerCert) {
		t.Fatal("server cert regenerated without SAN change")
	}
	for _, name := range []string{caKeyName, caCertName, serverKeyName, serverCertName} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnsureRotatesEd25519ToP256(t *testing.T) {
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Houdry Root CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, caCertName), encodeCertPEM(der), certPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, caKeyName), encodeKeyPEM(priv), keyPerm); err != nil {
		t.Fatal(err)
	}
	b, err := Ensure(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if b.CACert.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("CA still %s", b.CACert.PublicKeyAlgorithm)
	}
	if b.ServerCert.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("server still %s", b.ServerCert.PublicKeyAlgorithm)
	}
}

func TestSignCSRIssuesClientCert(t *testing.T) {
	b, err := Ensure(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := generateP256()
	if err != nil {
		t.Fatal(err)
	}
	csr, err := CreateCSR(priv, "node-abc", "box")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := b.SignCSR(csr, "node-abc", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "node-abc" {
		t.Fatalf("cn=%s", cert.Subject.CommonName)
	}
	if err := cert.CheckSignatureFrom(b.CACert); err != nil {
		t.Fatal(err)
	}
	foundClient := false
	for _, u := range cert.ExtKeyUsage {
		if u == x509.ExtKeyUsageClientAuth {
			foundClient = true
		}
	}
	if !foundClient {
		t.Fatal("missing clientAuth EKU")
	}
}

func TestSignCSRRejectsCNMismatch(t *testing.T) {
	b, err := Ensure(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	priv, _ := generateP256()
	csr, _ := CreateCSR(priv, "other", "box")
	if _, err := b.SignCSR(csr, "node-abc", time.Hour); err == nil {
		t.Fatal("expected CN mismatch")
	}
}

func TestEnsureNodePersists(t *testing.T) {
	dir := t.TempDir()
	m1, err := EnsureNode(dir, "n1", "host")
	if err != nil {
		t.Fatal(err)
	}
	m2, err := EnsureNode(dir, "n1", "host")
	if err != nil {
		t.Fatal(err)
	}
	if !publicKeysEqual(m1.Pub, m2.Pub) {
		t.Fatal("key rotated unexpectedly")
	}
	if len(m2.CSRPEM) == 0 {
		t.Fatal("missing CSR")
	}
	if _, ok := m1.Key.(*ecdsa.PrivateKey); !ok {
		t.Fatal("node key is not ECDSA")
	}
}

func TestServerCertRegeneratedWhenSANsStale(t *testing.T) {
	dir := t.TempDir()
	b1, err := Ensure(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := b1.issue(IssueOpts{
		Pub: b1.ServerKey.Public(), CN: "houdry-server", DNS: []string{"stale.invalid"},
		Lifetime: time.Hour, ServerAuth: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, serverCertName), EncodeCertPEM(stale), certPerm); err != nil {
		t.Fatal(err)
	}
	b2, err := Ensure(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if FingerprintSHA256(stale) == FingerprintSHA256(b2.ServerCert) {
		t.Fatal("expected server cert refresh when SANs are stale")
	}
}

func TestFingerprintStable(t *testing.T) {
	b, err := Ensure(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	a := FingerprintSHA256(b.CACert)
	if a != FingerprintSHA256(b.CACert) || len(a) < 20 {
		t.Fatalf("%s", a)
	}
}

func publicKeysEqual(a, b crypto.PublicKey) bool {
	da, err := x509.MarshalPKIXPublicKey(a)
	if err != nil {
		return false
	}
	db, err := x509.MarshalPKIXPublicKey(b)
	if err != nil {
		return false
	}
	return bytes.Equal(da, db)
}

func TestTLSConfigSkipsClientCertForBrowserHello(t *testing.T) {
	b, err := Ensure(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cfg := b.TLSConfig(nil)
	if cfg.ClientAuth != tls.NoClientCert {
		t.Fatalf("default ClientAuth=%v", cfg.ClientAuth)
	}
	if cfg.GetConfigForClient == nil {
		t.Fatal("missing GetConfigForClient")
	}

	agent, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{
		SupportedProtos: []string{"h2", "http/1.1"},
		CipherSuites:    []uint16{0x0a0a, 0x1301}, // GREASE + TLS_AES_128_GCM_SHA256
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent.ClientAuth != tls.NoClientCert {
		t.Fatalf("browser ClientAuth=%v", agent.ClientAuth)
	}

	gpu, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{
		SupportedProtos: []string{ALPNHoudry, "http/1.1"},
		CipherSuites:    []uint16{0x1301},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gpu.ClientAuth != tls.RequestClientCert {
		t.Fatalf("houdry ALPN ClientAuth=%v", gpu.ClientAuth)
	}

	legacy, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{
		SupportedProtos: []string{"h2", "http/1.1"},
		CipherSuites:    []uint16{0x1301},
	})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.ClientAuth != tls.RequestClientCert {
		t.Fatalf("Go GPU without GREASE ClientAuth=%v", legacy.ClientAuth)
	}
}

func TestClientTLSAdvertisesHoudryALPNWithNodeCert(t *testing.T) {
	b, err := Ensure(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mat, err := EnsureNode(t.TempDir(), "gpu-1", "box")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := b.SignCSR(mat.CSRPEM, "gpu-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	withCert, err := ClientTLS(b.CACertPEM(), EncodeCertPEM(cert), mat.KeyPEM())
	if err != nil {
		t.Fatal(err)
	}
	if len(withCert.NextProtos) == 0 || withCert.NextProtos[0] != ALPNHoudry {
		t.Fatalf("node NextProtos=%v", withCert.NextProtos)
	}
	caOnly, err := ClientTLS(b.CACertPEM(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, proto := range caOnly.NextProtos {
		if proto == ALPNHoudry {
			t.Fatal("CA-only client must not advertise houdry ALPN")
		}
	}
}
