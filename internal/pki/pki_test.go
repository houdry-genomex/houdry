package pki

import (
	"crypto/ed25519"
	"crypto/x509"
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

func TestSignCSRIssuesClientCert(t *testing.T) {
	b, err := Ensure(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(nil)
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
	_, priv, _ := ed25519.GenerateKey(nil)
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
	if !m1.Pub.Equal(m2.Pub) {
		t.Fatal("key rotated unexpectedly")
	}
	if len(m2.CSRPEM) == 0 {
		t.Fatal("missing CSR")
	}
}

func TestServerCertRegeneratedWhenSANsStale(t *testing.T) {
	dir := t.TempDir()
	b1, err := Ensure(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pub := b1.ServerKey.Public().(ed25519.PublicKey)
	stale, err := b1.issue(IssueOpts{
		Pub: pub, CN: "houdry-server", DNS: []string{"stale.invalid"},
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
