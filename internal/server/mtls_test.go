package server

import (
	"context"
	"crypto/tls"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

func TestHTTP1TLSTransportDisablesHTTP2(t *testing.T) {
	tr := http1TLSTransport(&tls.Config{NextProtos: []string{pki.ALPNHoudry, "http/1.1"}})
	if tr.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 must be false — Go otherwise prepends ALPN h2")
	}
	if tr.TLSNextProto == nil {
		t.Fatal("nil TLSNextProto auto-enables HTTP/2")
	}
	for _, p := range tr.TLSClientConfig.NextProtos {
		if p == "h2" {
			t.Fatalf("ALPN offers h2: %v", tr.TLSClientConfig.NextProtos)
		}
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
	m, err := pki.EnsureNode(t.TempDir(), "evil", "host")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := other.SignCSR(m.CSRPEM, "evil", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pki.ClientTLS(other.CACertPEM(), pki.EncodeCertPEM(cert), m.KeyPEM())
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

func TestLoadOrFetchCAIgnoresStaleLocalServeCA(t *testing.T) {
	env := startTLSServer(t, Options{})
	stale := filepath.Join(houdryHome(), "server", "pki")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "root_ca.crt"), []byte("-----BEGIN CERTIFICATE-----\nnot-the-plane\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nodeDir := filepath.Join(houdryHome(), "node")
	if err := os.MkdirAll(nodeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodeDir, "ca.crt"), []byte("-----BEGIN CERTIFICATE-----\nstale-pin\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := loadOrFetchCA(context.Background(), env.URL)
	if err != nil {
		t.Fatal(err)
	}
	want := env.S.bundle.CACertPEM()
	if string(got) != string(want) {
		t.Fatalf("used stale local CA instead of the live control plane")
	}
}

func TestIsLoopbackURL(t *testing.T) {
	if !isLoopbackURL("https://127.0.0.1:18080") || !isLoopbackURL("http://localhost:18080/v1") {
		t.Fatal("expected loopback")
	}
	if isLoopbackURL("https://192.168.29.48:18080") {
		t.Fatal("LAN IP must not use this machine's serve CA")
	}
}

func TestDetachedClientKeepsMTLSAfterCancel(t *testing.T) {
	c := &http.Client{Timeout: time.Second}
	parent, cancel := context.WithCancel(WithHTTPClient(context.Background(), c))
	cancel()
	detached := DetachedClient(parent)
	if detached.Err() != nil {
		t.Fatal("detached context must not be cancelled")
	}
	if clientFrom(detached) != c {
		t.Fatal("detached context dropped the mTLS HTTP client")
	}
}

func TestReportJobResultWithoutCertRejected(t *testing.T) {
	env := startTLSServer(t, Options{})
	_, err := ReportJobResult(env.PublicCtx(), env.URL, "", "job-1", "n1", true, nil, "")
	if err == nil || !strings.Contains(err.Error(), "client certificate required") {
		t.Fatalf("expected client certificate required, got %v", err)
	}
}

func TestJoinAfterLeaveReusesCert(t *testing.T) {
	env := startTLSServer(t, Options{})
	ctx := env.Enroll("n1")
	if _, err := JoinAgent(ctx, env.URL, "", JoinRequest{
		Inventory: testInv("n1"), AgentVersion: "t", Status: StatusReady,
	}); err != nil {
		t.Fatal(err)
	}
	if err := LeaveNode(ctx, env.URL, "", "n1"); err != nil {
		t.Fatal(err)
	}
	if _, err := JoinAgent(ctx, env.URL, "", JoinRequest{
		Inventory: testInv("n1"), AgentVersion: "t", Status: StatusReady,
	}); err != nil {
		t.Fatalf("rejoin after leave: %v", err)
	}
}

func TestDialNodeJoinTrustsFetchedCA(t *testing.T) {
	env := startTLSServer(t, Options{})
	iss, err := env.S.enroll.Create()
	if err != nil {
		t.Fatal(err)
	}
	ctx, url, err := DialNode(context.Background(), env.URL, iss.Token, "gpu-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Join(ctx, url, "", testInv("gpu-1")); err != nil {
		t.Fatal(err)
	}
}

func TestDialNodeIgnoresStaleNodeCAFile(t *testing.T) {
	env := startTLSServer(t, Options{})
	iss, err := env.S.enroll.Create()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DialNode(context.Background(), env.URL, iss.Token, "gpu-1"); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(houdryHome(), "node", "ca.crt")
	if err := os.WriteFile(stale, []byte("-----BEGIN CERTIFICATE-----\nstale-pin\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, url, err := DialNode(context.Background(), env.URL, "", "gpu-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Join(ctx, url, "", testInv("gpu-1")); err != nil {
		t.Fatal(err)
	}
}
