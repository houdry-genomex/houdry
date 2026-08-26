package gpuruntime

import (
	"context"
	"strings"
	"testing"
)

type fakeRuntime struct {
	name   string
	vendor string
	avail  bool
	result SmokeResult
	err    error
}

func (f fakeRuntime) Name() string   { return f.name }
func (f fakeRuntime) Vendor() string { return f.vendor }
func (f fakeRuntime) Available(ctx context.Context) bool {
	return f.avail
}
func (f fakeRuntime) Smoke(ctx context.Context) (SmokeResult, error) {
	return f.result, f.err
}

func TestSmokeAllDedupesPhysicalDevicesAcrossRuntimes(t *testing.T) {
	same := Device{ID: "GPU-abc", Name: "RTX 2050", MemoryTotalBytes: 4 << 30}
	runtimes := []Runtime{
		fakeRuntime{
			name:   "nvidia",
			vendor: "nvidia",
			avail:  true,
			result: SmokeResult{OK: true, Message: "ok", Devices: []Device{same}},
		},
		fakeRuntime{
			name:   "inventory",
			vendor: "any",
			avail:  true,
			result: SmokeResult{OK: true, Message: "ok", Devices: []Device{same}},
		},
	}
	out, err := SmokeAll(context.Background(), runtimes)
	if err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true {
		t.Fatalf("%v", out)
	}
	if out["physical_device_count"] != 1 {
		t.Fatalf("physical_device_count=%v want 1", out["physical_device_count"])
	}
	if out["runtime_probe_count"] != 2 {
		t.Fatalf("runtime_probe_count=%v want 2", out["runtime_probe_count"])
	}
	if out["ok_runtime_count"] != 2 {
		t.Fatalf("ok_runtime_count=%v want 2", out["ok_runtime_count"])
	}
	if _, ok := out["device_count"]; ok {
		t.Fatal("device_count should not be present; it is misleading")
	}
}

func TestSmokeAllCountsDistinctPhysicalDevices(t *testing.T) {
	out, err := SmokeAll(context.Background(), []Runtime{
		fakeRuntime{
			name:  "nvidia",
			avail: true,
			result: SmokeResult{OK: true, Devices: []Device{
				{ID: "gpu-1", Name: "A"},
				{ID: "gpu-2", Name: "B"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out["physical_device_count"] != 2 {
		t.Fatalf("physical_device_count=%v", out["physical_device_count"])
	}
	if out["runtime_probe_count"] != 1 {
		t.Fatalf("runtime_probe_count=%v", out["runtime_probe_count"])
	}
}

func TestSmokeAllFailsWhenNoRuntimeOK(t *testing.T) {
	_, err := SmokeAll(context.Background(), []Runtime{
		fakeRuntime{name: "dead", avail: true, result: SmokeResult{OK: false, Message: "down"}},
	})
	if err == nil || !strings.Contains(err.Error(), "no runtime reported") {
		t.Fatalf("err=%v", err)
	}
}

func TestSelectAvailable(t *testing.T) {
	got := SelectAvailable(context.Background(), []Runtime{
		fakeRuntime{name: "a", avail: false},
		fakeRuntime{name: "b", avail: true},
	})
	if len(got) != 1 || got[0].Name() != "b" {
		t.Fatalf("%v", got)
	}
}
