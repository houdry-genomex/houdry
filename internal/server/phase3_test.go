package server

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"houdry/internal/gpu"
)

func joinReadyNode(t *testing.T, url, id, name string, vram uint64) Node {
	t.Helper()
	inv := gpu.Inventory{
		NodeID:     id,
		DetectedAt: time.Now().UTC(),
		Host:       gpu.Host{Hostname: id, OS: "linux", Arch: "amd64"},
		GPUs: []gpu.GPU{{
			Index:            0,
			ID:               "gpu-" + id,
			Vendor:           gpu.VendorNVIDIA,
			Name:             name,
			MemoryTotalBytes: vram,
			Source:           "nvidia-smi",
		}},
	}
	n, err := JoinAgent(context.Background(), url, "", JoinRequest{
		Inventory:    inv,
		AgentVersion: "test",
		Status:       StatusReady,
		Runtimes:     []string{"nvidia", "inventory"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Resources.Static.GPUs) != 1 {
		t.Fatalf("expected 1 physical GPU in profile, got %+v", n.Resources)
	}
	return n
}

func TestSchedulerSelectsByVRAM(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	joinReadyNode(t, ts.URL, "node-01", "RTX 2050", 4<<30)
	joinReadyNode(t, ts.URL, "node-02", "RTX 4060", 8<<30)

	job, err := SubmitJobWithRequirements(context.Background(), ts.URL, "", JobTypeGPUSmoke, "", Requirements{
		GPURequired:  true,
		MinVRAMBytes: 6 << 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobPending {
		t.Fatalf("expected pending (assigned), got %s", job.Status)
	}
	if job.NodeID != "node-02" {
		t.Fatalf("expected node-02 (8GB), got %s", job.NodeID)
	}
}

func TestSchedulerQueuesWhenNoFit(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	joinReadyNode(t, ts.URL, "node-01", "RTX 2050", 4<<30)

	job, err := SubmitJobWithRequirements(context.Background(), ts.URL, "", JobTypeGPUSmoke, "", Requirements{
		GPURequired:  true,
		MinVRAMBytes: 6 << 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobQueued {
		t.Fatalf("expected queued, got %s node=%s", job.Status, job.NodeID)
	}

	// Larger node joins → queue drains onto it.
	joinReadyNode(t, ts.URL, "node-02", "RTX 4060", 8<<30)
	got, ok := s.jobs.Get(job.ID)
	if !ok || got.Status != JobPending || got.NodeID != "node-02" {
		t.Fatalf("expected assigned to node-02, got %+v", got)
	}
}

func TestOfflineExcludedFromScheduling(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	joinReadyNode(t, ts.URL, "node-01", "RTX 2050", 4<<30)
	joinReadyNode(t, ts.URL, "node-02", "RTX 4060", 8<<30)
	s.store.SetStatus("node-02", StatusOffline, "")

	job, err := SubmitJobWithRequirements(context.Background(), ts.URL, "", JobTypeGPUSmoke, "", Requirements{
		GPURequired:  true,
		MinVRAMBytes: 6 << 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobQueued || job.NodeID != "" {
		t.Fatalf("offline 8GB node must not be selected: %+v", job)
	}
}

func TestDrainRejectsNewClaims(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	joinReadyNode(t, ts.URL, "node-01", "RTX 2050", 4<<30)
	if _, err := DrainNode(context.Background(), ts.URL, "", "node-01"); err != nil {
		t.Fatal(err)
	}
	job, err := SubmitJob(context.Background(), ts.URL, "", JobTypeGPUSmoke, "")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobQueued {
		t.Fatalf("draining node must not receive jobs: %+v", job)
	}
	_, ok, err := ClaimJob(context.Background(), ts.URL, "", "node-01")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("claim should fail while draining")
	}
}

func TestRuntimeProbesNotCountedAsGPUs(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	n := joinReadyNode(t, ts.URL, "node-01", "RTX 2050", 4<<30)
	if TotalPhysicalGPUs([]Node{n}) != 1 {
		t.Fatalf("gpus=%d", TotalPhysicalGPUs([]Node{n}))
	}
	if len(n.Runtimes) != 2 {
		t.Fatalf("runtimes=%v", n.Runtimes)
	}
}

func TestFitsUsesAvailableVRAM(t *testing.T) {
	n := Node{
		Status: StatusReady,
		Resources: ResourceProfile{
			Static: StaticResources{
				GPUs: []StaticGPU{{ID: "g1", MemoryTotalBytes: 8 << 30}},
			},
			Dynamic: DynamicResources{
				GPUs: []DynamicGPU{{ID: "g1", MemoryUsedBytes: 5 << 30, MemoryAvailableBytes: 3 << 30}},
			},
		},
	}
	if Fits(n, Requirements{GPURequired: true, MinVRAMBytes: 6 << 30}) {
		t.Fatal("should reject when available VRAM is only 3GB")
	}
	if !Fits(n, Requirements{GPURequired: true, MinVRAMBytes: 2 << 30}) {
		t.Fatal("should accept 2GB requirement")
	}
}
