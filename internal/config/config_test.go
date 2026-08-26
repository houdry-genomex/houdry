package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirRespectsHoudryHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOODRY_HOME", dir)
	if Dir() != dir {
		t.Fatalf("got %s", Dir())
	}
}

func TestEnsureNodeIDPersists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOODRY_HOME", dir)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureNodeID(); err != nil {
		t.Fatal(err)
	}
	if c.NodeID == "" {
		t.Fatal("empty id")
	}
	c2, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c2.NodeID != c.NodeID {
		t.Fatalf("%s != %s", c2.NodeID, c.NodeID)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}
}
