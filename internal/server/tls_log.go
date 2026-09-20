package server

import (
	"io"
	"log"
	"os"
	"strings"
)

// abortedTLSHandshake is a peer that closed or sent a truncated TLS record
// before Finished. Agent/Python/Electron open extra sockets and cancel some;
// Go then logs "tls: bad record MAC" even though a later connection succeeds
// (chat and gpu register work). That is not a cert/auth failure.
func abortedTLSHandshake(msg string) bool {
	if !strings.Contains(msg, "TLS handshake error") {
		return false
	}
	return strings.Contains(msg, "bad record MAC") ||
		strings.Contains(msg, ": EOF") ||
		strings.Contains(msg, "connection reset by peer")
}

type tlsHandshakeWriter struct {
	out io.Writer
}

func (w tlsHandshakeWriter) Write(p []byte) (int, error) {
	if abortedTLSHandshake(string(p)) {
		return len(p), nil
	}
	return w.out.Write(p)
}

func tlsHandshakeLogger() *log.Logger {
	return log.New(tlsHandshakeWriter{out: os.Stderr}, "", log.LstdFlags)
}
