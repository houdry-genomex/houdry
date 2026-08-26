package server

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"houdry/internal/gpu"
)

func TestAgentHeartbeatAndJobSmoke(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	inv := gpu.Inventory{
		NodeID:     "agent-1",
		DetectedAt: time.Now().UTC(),
		Host:       gpu.Host{Hostname: "box", OS: "linux", Arch: "amd64"},
		GPUs: []gpu.GPU{{
			Index:            0,
			Vendor:           gpu.VendorNVIDIA,
			Name:             "RTX 2050",
			MemoryTotalBytes: 4 << 30,
			Source:           "nvidia-smi",
		}},
	}
	n, err := JoinAgent(context.Background(), ts.URL, "", JoinRequest{
		Inventory:    inv,
		AgentVersion: "test",
		Status:       StatusReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.Status != StatusReady {
		t.Fatalf("status=%s", n.Status)
	}

	hb, err := Heartbeat(context.Background(), ts.URL, "", JoinRequest{
		Inventory:    inv,
		AgentVersion: "test",
		Status:       StatusReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hb.Status != StatusReady {
		t.Fatalf("heartbeat status=%s", hb.Status)
	}

	job, err := SubmitJob(context.Background(), ts.URL, "", JobTypeGPUSmoke, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobPending {
		t.Fatalf("job status=%s (want pending/assigned)", job.Status)
	}
	if job.NodeID != "agent-1" {
		t.Fatalf("node_id=%s", job.NodeID)
	}

	claimed, ok, err := ClaimJob(context.Background(), ts.URL, "", "agent-1")
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if claimed.ID != job.ID || claimed.Status != JobRunning {
		t.Fatalf("%+v", claimed)
	}

	nodes, err := ListNodes(context.Background(), ts.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if nodes[0].Status != StatusBusy {
		t.Fatalf("expected BUSY, got %s", nodes[0].Status)
	}

	done, err := ReportJobResult(context.Background(), ts.URL, "", claimed.ID, "agent-1", true, map[string]any{
		"gpu_count": 1,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != JobSucceeded {
		t.Fatalf("%+v", done)
	}

	nodes, err = ListNodes(context.Background(), ts.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if nodes[0].Status != StatusReady {
		t.Fatalf("expected READY after result, got %s", nodes[0].Status)
	}
}

func TestInventoryJoinIsJoinedNotReady(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	got, err := Join(context.Background(), ts.URL, "", gpu.Inventory{NodeID: "snap"})
	if err != nil {
		t.Fatal(err)
	}
	if got["status"] != StatusJoined {
		t.Fatalf("status=%v", got["status"])
	}
}

func TestMarkStaleOffline(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st.offline = time.Millisecond
	n := st.Upsert(Node{
		Inventory:    gpu.Inventory{NodeID: "a"},
		AgentVersion: "1",
		Status:       StatusReady,
	})
	time.Sleep(5 * time.Millisecond)
	_ = n
	changed := st.MarkStaleOffline()
	if len(changed) != 1 {
		t.Fatalf("changed=%v", changed)
	}
	got, ok := st.Get("a")
	if !ok || got.Status != StatusOffline {
		t.Fatalf("%v %+v", ok, got)
	}
}
