package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirRespectsHoudryHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOUDRY_HOME", dir)
	t.Setenv("HOODRY_HOME", "")
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

func TestLoadStripsUTF8BOM(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOODRY_HOME", dir)
	body := append([]byte{0xEF, 0xBB, 0xBF}, []byte("{\n  \"node_id\": \"n-from-bom\"\n}\n")...)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.NodeID != "n-from-bom" {
		t.Fatalf("node_id=%q", c.NodeID)
	}
}

func TestLoadEmptyFileIsBlankConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOODRY_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte{0xEF, 0xBB, 0xBF, '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.NodeID != "" || c.Server != "" {
		t.Fatalf("%+v", c)
	}
}
