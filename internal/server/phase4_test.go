package server

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"houdry/internal/gpu"
	"houdry/internal/modelruntime"
)

func TestSchedulerPrefersNodeWithModel(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	// Node A: GPU ok, runtime ok, no model
	joinModelNode(t, ts.URL, "node-a", "RTX 2050", 4<<30, []string{"ollama"}, nil)
	// Node B: has the model
	joinModelNode(t, ts.URL, "node-b", "RTX 4060", 8<<30, []string{"ollama"}, []modelruntime.Model{
		{Name: "tinyllama", Tag: "latest", Runtime: "ollama", State: modelruntime.StateAvailable},
	})

	job, err := SubmitInference(context.Background(), ts.URL, "", "tinyllama", "Say hello", "", Requirements{
		GPURequired: true,
		Model:       "tinyllama",
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobPending || job.NodeID != "node-b" {
		t.Fatalf("expected node-b (has model), got status=%s node=%s", job.Status, job.NodeID)
	}
	if job.Requirements.ModelName != "tinyllama" {
		t.Fatalf("expected normalized model_name, got %+v", job.Requirements)
	}
}

func TestSchedulerRespectsRuntimePreference(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	joinModelNode(t, ts.URL, "node-ollama", "RTX 2050", 4<<30, []string{"ollama"}, []modelruntime.Model{
		{Name: "qwen", Tag: "7b", Runtime: "ollama", State: modelruntime.StateAvailable},
	})
	joinModelNode(t, ts.URL, "node-vllm", "RTX 4060", 8<<30, []string{"vllm"}, []modelruntime.Model{
		{Name: "qwen", Tag: "7b", Runtime: "vllm", State: modelruntime.StateAvailable},
	})

	job, err := SubmitInference(context.Background(), ts.URL, "", "qwen:7b", "hi", "", Requirements{
		GPURequired:  true,
		Model:        "qwen:7b",
		ModelRuntime: "vllm",
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.NodeID != "node-vllm" {
		t.Fatalf("expected vllm node, got %s", job.NodeID)
	}
}

func TestSchedulerRequireModelPresent(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	joinModelNode(t, ts.URL, "node-a", "RTX 2050", 4<<30, []string{"ollama"}, nil)

	job, err := SubmitInference(context.Background(), ts.URL, "", "missing-model", "hi", "", Requirements{
		GPURequired:         true,
		Model:               "missing-model",
		RequireModelPresent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobQueued {
		t.Fatalf("expected queued when model absent, got %s", job.Status)
	}
}

func TestInferenceJobRequiresPrompt(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	_, err = SubmitJobFull(context.Background(), ts.URL, "", JobTypeInference, "", Requirements{
		GPURequired: true,
		Model:       "tinyllama",
	}, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing prompt")
	}
}

func joinModelNode(t *testing.T, url, id, gpuName string, vram uint64, modelRTs []string, models []modelruntime.Model) {
	t.Helper()
	inv := gpu.Inventory{
		NodeID:     id,
		DetectedAt: time.Now().UTC(),
		Host:       gpu.Host{Hostname: id, OS: "linux", Arch: "amd64"},
		GPUs: []gpu.GPU{{
			Index: 0, ID: "gpu-" + id, Vendor: gpu.VendorNVIDIA,
			Name: gpuName, MemoryTotalBytes: vram, Source: "test",
		}},
	}
	_, err := JoinAgent(context.Background(), url, "", JoinRequest{
		Inventory:     inv,
		AgentVersion:  "test",
		Status:        StatusReady,
		Runtimes:      []string{"nvidia"},
		ModelRuntimes: modelRTs,
		Models:        models,
	})
	if err != nil {
		t.Fatal(err)
	}
}
