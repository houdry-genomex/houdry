package agent

import (
	"context"
	"fmt"
	"os"
	"time"

	"houdry/internal/config"
	"houdry/internal/pki"
	"houdry/internal/server"
)

func dial(ctx context.Context, opts Options) (context.Context, Options, error) {
	ctx, url, err := server.DialNode(ctx, opts.ServerURL, opts.Token, opts.NodeID)
	if err != nil {
		return ctx, opts, err
	}
	opts.ServerURL = url
	opts.Token = "" // node identity is the client cert, not the one-time enroll token
	return ctx, opts, nil
}

func maybeRenew(ctx context.Context, opts Options) {
	mat, err := pki.LoadNode(pki.NodeDir(config.Dir()))
	if err != nil || !mat.HasCertificate() {
		return
	}
	cert, err := mat.Certificate()
	if err != nil {
		return
	}
	if time.Until(cert.NotAfter) > 7*24*time.Hour {
		return
	}
	out, err := server.RenewCert(ctx, opts.ServerURL, string(mat.CSRPEM))
	if err != nil {
		fmt.Fprintf(os.Stderr, "certificate renew: %v (keeping current cert)\n", err)
		return
	}
	if err := mat.SaveEnrollment([]byte(out.Certificate), []byte(out.CA)); err != nil {
		fmt.Fprintf(os.Stderr, "certificate renew save: %v\n", err)
	}
}
