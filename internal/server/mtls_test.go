package server

import (
	"crypto/ed25519"
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	"houdry/internal/gpu"
	"houdry/internal/pki"
)

func testInv(id string) gpu.Inventory {
	return gpu.Inventory{
		NodeID: id,
		Host:   gpu.Host{Hostname: id, OS: "linux", Arch: "amd64"},
		GPUs: []gpu.GPU{{
			Index: 0, ID: "gpu-" + id, Vendor: gpu.VendorNVIDIA,
			Name: "RTX", MemoryTotalBytes: 4 << 30, Source: "test",
		}},
	}
}

func TestEnrollRejectsInvalidAndReusedToken(t *testing.T) {
	env := startTLSServer(t, Options{})
	m, err := pki.EnsureNode(t.TempDir(), "n1", "host")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Enroll(env.PublicCtx(), env.URL, "HDRY_dead", "n1", string(m.CSRPEM)); err == nil {
		t.Fatal("expected invalid token")
	}
	iss, err := env.S.enroll.Create()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Enroll(env.PublicCtx(), env.URL, iss.Token, "n1", string(m.CSRPEM)); err != nil {
		t.Fatal(err)
	}
	m2, err := pki.EnsureNode(t.TempDir(), "n2", "host")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Enroll(env.PublicCtx(), env.URL, iss.Token, "n2", string(m2.CSRPEM)); err == nil {
		t.Fatal("expected reused token rejection")
	}
}

func TestJoinWithoutClientCertRejected(t *testing.T) {
	env := startTLSServer(t, Options{})
	if _, err := Join(env.PublicCtx(), env.URL, "", testInv("x")); err == nil {
		t.Fatal("expected missing client cert")
	}
}

func TestUnknownCARejected(t *testing.T) {
	env := startTLSServer(t, Options{})
	other, err := pki.Ensure(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := pki.CreateCSR(priv, "evil", "host")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := other.SignCSR(csr, "evil", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pki.ClientTLS(other.CACertPEM(), pki.EncodeCertPEM(cert), encodeKeyForTest(priv))
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithHTTPClient(env.PublicCtx(), &http.Client{
		Transport: &http.Transport{TLSClientConfig: cfg},
	})
	if _, err := Join(ctx, env.URL, "", testInv("evil")); err == nil {
		t.Fatal("expected unknown CA rejection")
	}
}

func encodeKeyForTest(key ed25519.PrivateKey) []byte {
	m := &pki.NodeMaterial{Key: key}
	return m.KeyPEM()
}

func TestRevokedCertRejected(t *testing.T) {
	env := startTLSServer(t, Options{})
	ctx := env.Enroll("n1")
	if _, err := JoinAgent(ctx, env.URL, "", JoinRequest{
		Inventory: testInv("n1"), AgentVersion: "t", Status: StatusReady,
	}); err != nil {
		t.Fatal(err)
	}
	n, _ := env.S.store.Get("n1")
	if err := env.S.revoke.Revoke(n.CertSerial, "n1"); err != nil {
		t.Fatal(err)
	}
	if _, err := Heartbeat(ctx, env.URL, "", JoinRequest{
		Inventory: testInv("n1"), AgentVersion: "t", Status: StatusReady,
	}); err == nil {
		t.Fatal("expected revoked cert rejection")
	}
}

func TestSuspendedNodeKeepsHeartbeatLosesJobs(t *testing.T) {
	env := startTLSServer(t, Options{})
	ctx := env.Enroll("n1")
	if _, err := JoinAgent(ctx, env.URL, "", JoinRequest{
		Inventory: testInv("n1"), AgentVersion: "t", Status: StatusReady,
		Runtimes: []string{"nvidia"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := env.S.store.SetIdentity("n1", IdentitySuspended); !ok {
		t.Fatal("set identity")
	}
	if _, err := Heartbeat(ctx, env.URL, "", JoinRequest{
		Inventory: testInv("n1"), AgentVersion: "t", Status: StatusReady,
	}); err != nil {
		t.Fatal(err)
	}
	n, _ := env.S.store.Get("n1")
	if Fits(n, Requirements{GPURequired: true}) {
		t.Fatal("suspended node must not fit")
	}
}

func TestExpiredNodeCertRejected(t *testing.T) {
	env := startTLSServer(t, Options{})
	ctx := env.Enroll("n1")
	if _, err := JoinAgent(ctx, env.URL, "", JoinRequest{
		Inventory: testInv("n1"), AgentVersion: "t", Status: StatusReady,
	}); err != nil {
		t.Fatal(err)
	}
	other, err := pki.Ensure(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	csr, _ := pki.CreateCSR(priv, "n1", "host")
	cert, err := other.SignCSR(csr, "n1", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_ = cert
	_ = tls.VersionTLS13
	time.Sleep(5 * time.Millisecond)
	// Handshake against our plane with a cert from another CA already covered.
	// Expired cert signed by the plane CA:
	env.S.certLifetime = time.Millisecond
	m, err := pki.EnsureNode(t.TempDir(), "exp", "host")
	if err != nil {
		t.Fatal(err)
	}
	iss, err := env.S.enroll.Create()
	if err != nil {
		t.Fatal(err)
	}
	out, err := Enroll(env.PublicCtx(), env.URL, iss.Token, "exp", string(m.CSRPEM))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SaveEnrollment([]byte(out.Certificate), []byte(out.CA)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	c, err := mtlsClient(m.CAPEM, m.CertPEM, m.KeyPEM())
	if err != nil {
		t.Fatal(err)
	}
	expCtx := WithHTTPClient(env.PublicCtx(), c)
	if _, err := Join(expCtx, env.URL, "", testInv("exp")); err == nil {
		t.Fatal("expected expired cert rejection")
	}
	_ = ctx
}
