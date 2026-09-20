package server

import "testing"

func TestAbortedTLSHandshake(t *testing.T) {
	mac := "http: TLS handshake error from 192.168.29.179:55595: local error: tls: bad record MAC"
	if !abortedTLSHandshake(mac) {
		t.Fatal("bad record MAC from a canceled Agent socket must be treated as aborted")
	}
	if !abortedTLSHandshake("http: TLS handshake error from 10.0.0.1:1: EOF") {
		t.Fatal("EOF mid-handshake is aborted")
	}
	if abortedTLSHandshake("http: TLS handshake error from 10.0.0.1:1: tls: bad certificate") {
		t.Fatal("must still print real certificate failures")
	}
	if abortedTLSHandshake("node revoked") {
		t.Fatal("non-handshake lines must not be filtered")
	}
}
