package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"houdry/internal/gpu"
)

func TestJoinAndList(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Options{DataDir: dir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	defer ts.Close()

	used := 40
	inv := gpu.Inventory{
		NodeID:     "node-1",
		DetectedAt: time.Now().UTC(),
		Host:       gpu.Host{Hostname: "lab-box", OS: "linux", Arch: "amd64"},
		GPUs: []gpu.GPU{{
			Index:            0,
			Vendor:           gpu.VendorNVIDIA,
			Name:             "Tesla T4",
			MemoryTotalBytes: 16 << 30,
			UtilizationGPU:   &used,
			Source:           "nvidia-smi",
		}},
	}
	got, err := Join(context.Background(), ts.URL, "", inv)
	if err != nil {
		t.Fatal(err)
	}
	if got["node_id"] != "node-1" {
		t.Errorf("node_id=%v", got["node_id"])
	}

	nodes, err := ListNodes(context.Background(), ts.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || len(nodes[0].GPUs) != 1 {
		t.Fatalf("%+v", nodes)
	}

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}

	resp, err = http.Get(ts.URL + "/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	if !strings.Contains(body, ts.URL) {
		t.Fatalf("install.sh missing server url: %s", body[:min(200, len(body))])
	}
	if !strings.Contains(body, "houdry gpu register") {
		t.Fatal("install.sh missing gpu register instructions")
	}

	resp, err = http.Get(ts.URL + "/.well-known/houdry.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	var meta map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	if meta["houdry"] != "control-plane" {
		t.Fatalf("%v", meta)
	}
}

func TestJoinRequiresToken(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	defer ts.Close()

	_, err = Join(context.Background(), ts.URL, "", gpu.Inventory{NodeID: "x"})
	if err == nil {
		t.Fatal("expected unauthorized")
	}
	_, err = Join(context.Background(), ts.URL, "secret", gpu.Inventory{NodeID: "x"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStorePersists(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Upsert(Node{Inventory: gpu.Inventory{NodeID: "a", Host: gpu.Host{Hostname: "h"}}})

	st2, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	n, ok := st2.Get("a")
	if !ok || n.Host.Hostname != "h" {
		t.Fatalf("%v %+v", ok, n)
	}
	if _, err := os.Stat(filepath.Join(dir, "nodes.json")); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadSelf(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), BinariesDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	defer ts.Close()

	// This test binary is not GOOS/amd64 houdry; the handler falls back to
	// os.Executable() only when os/arch match. Just assert 404 for mismatch.
	resp, err := http.Get(ts.URL + "/download/plan9/amd64")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestJoinJSONRoundTrip(t *testing.T) {
	inv := gpu.Inventory{NodeID: "n", GPUs: []gpu.GPU{{Name: "x", Vendor: gpu.VendorIntel}}}
	b, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	var out gpu.Inventory
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.GPUs[0].Vendor != gpu.VendorIntel {
		t.Fatal(out)
	}
}

func TestGeneratedFilesAreServed(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := filepath.Join(dir, "generated", "model.step")
	if err := os.WriteFile(path, []byte("ISO-10303-21;"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/files/model.step")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "ISO-10303-21;" {
		t.Fatalf("got %q", b)
	}
}
