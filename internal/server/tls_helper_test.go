package server

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"houdry/internal/pki"
)

type tlsEnv struct {
	t       *testing.T
	S       *Server
	TS      *httptest.Server
	URL     string
	clients map[string]context.Context
}

func startTLSServer(t *testing.T, opts Options) *tlsEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOUDRY_HOME", home)
	t.Setenv("HOODRY_HOME", home)
	if opts.DataDir == "" {
		opts.DataDir = t.TempDir()
	}
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.initPKI(); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(s)
	ts.TLS = s.bundle.TLSConfig(s.verifyPeer)
	ts.Config.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return &tlsEnv{t: t, S: s, TS: ts, URL: ts.URL, clients: map[string]context.Context{}}
}

func (e *tlsEnv) PublicCtx() context.Context {
	return WithHTTPClient(context.Background(), e.TS.Client())
}

func (e *tlsEnv) Enroll(nodeID string) context.Context {
	e.t.Helper()
	if ctx, ok := e.clients[nodeID]; ok {
		return ctx
	}
	m, err := pki.EnsureNode(e.t.TempDir(), nodeID, "testhost")
	if err != nil {
		e.t.Fatal(err)
	}
	iss, err := e.S.enroll.Create()
	if err != nil {
		e.t.Fatal(err)
	}
	out, err := Enroll(e.PublicCtx(), e.URL, iss.Token, nodeID, string(m.CSRPEM))
	if err != nil {
		e.t.Fatal(err)
	}
	if err := m.SaveEnrollment([]byte(out.Certificate), []byte(out.CA)); err != nil {
		e.t.Fatal(err)
	}
	c, err := mtlsClient(m.CAPEM, m.CertPEM, m.KeyPEM())
	if err != nil {
		e.t.Fatal(err)
	}
	ctx := WithHTTPClient(context.Background(), c)
	e.clients[nodeID] = ctx
	return ctx
}

func (e *tlsEnv) Get(path string) (*http.Response, error) {
	return e.TS.Client().Get(e.URL + path)
}

func (e *tlsEnv) Post(path, contentType string, body io.Reader) (*http.Response, error) {
	return e.TS.Client().Post(e.URL+path, contentType, body)
}
