package securitycfg

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaultsWhenMissing(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Security.MTLS || c.TokenTTL() != 10*time.Minute || c.CertLifetime() != 30*24*time.Hour {
		t.Fatalf("%+v", c)
	}
}

func TestLoadParsesDurations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "security.yaml")
	if err := os.WriteFile(path, []byte("security:\n  enrollment:\n    token_ttl: 2m\n    max_attempts: 3\n  certificates:\n    lifetime: 48h\n    renew_before: 1h\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.TokenTTL() != 2*time.Minute || c.MaxAttempts() != 3 || c.CertLifetime() != 48*time.Hour || c.RenewBefore() != time.Hour {
		t.Fatalf("%+v", c)
	}
}
