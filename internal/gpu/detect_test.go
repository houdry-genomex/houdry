package gpu

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"time"
)

type fakeFile struct {
	data    []byte
	entries []fs.DirEntry
	link    string
}

type fakeRunner struct {
	bins map[string]string
	cmds map[string]struct {
		stdout string
		stderr string
		err    error
	}
	files map[string]fakeFile
}

func (f *fakeRunner) LookPath(file string) (string, error) {
	if p, ok := f.bins[file]; ok {
		return p, nil
	}
	return "", errNotFound{file}
}

type errNotFound struct{ file string }

func (e errNotFound) Error() string { return e.file + " not found" }

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	key := name + " " + strings.Join(args, " ")
	if c, ok := f.cmds[key]; ok {
		return c.stdout, c.stderr, c.err
	}
	if c, ok := f.cmds[name]; ok {
		return c.stdout, c.stderr, c.err
	}
	return "", "not mocked: " + key, errNotFound{key}
}

func (f *fakeRunner) ReadFile(name string) ([]byte, error) {
	if file, ok := f.files[name]; ok {
		return file.data, nil
	}
	return nil, fs.ErrNotExist
}

func (f *fakeRunner) ReadDir(name string) ([]fs.DirEntry, error) {
	if file, ok := f.files[name]; ok {
		return file.entries, nil
	}
	return nil, fs.ErrNotExist
}

func (f *fakeRunner) ReadLink(name string) (string, error) {
	if file, ok := f.files[name]; ok && file.link != "" {
		return file.link, nil
	}
	return "", fs.ErrNotExist
}

func (f *fakeRunner) Exists(name string) bool {
	if _, ok := f.bins[name]; ok {
		return true
	}
	_, ok := f.files[name]
	return ok
}

func TestParseNvidiaCSV(t *testing.T) {
	raw := "0, GPU-abc-123, NVIDIA GeForce RTX 4090, 550.54.14, 24564, 1024, 12, 8, 41, 00000000:01:00.0, 8.9\n"
	gpus, err := parseNvidiaCSV(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(gpus) != 1 {
		t.Fatalf("got %d gpus", len(gpus))
	}
	g := gpus[0]
	if g.Name != "NVIDIA GeForce RTX 4090" {
		t.Errorf("name=%q", g.Name)
	}
	if g.Vendor != VendorNVIDIA {
		t.Errorf("vendor=%s", g.Vendor)
	}
	if g.ComputeCapability != "8.9" {
		t.Errorf("cc=%q", g.ComputeCapability)
	}
	if g.MemoryTotalBytes != 24564*1024*1024 {
		t.Errorf("mem=%d", g.MemoryTotalBytes)
	}
	if g.UtilizationGPU == nil || *g.UtilizationGPU != 12 {
		t.Errorf("util=%v", g.UtilizationGPU)
	}
}

func TestParseNvidiaCSVQuotedComma(t *testing.T) {
	raw := "0, GPU-1, \"NVIDIA, Inc. GPU\", 550.54.14, 8192, 0, 0, 0, 30, 0000:02:00.0, 7.5\n"
	gpus, err := parseNvidiaCSV(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if gpus[0].Name != "NVIDIA, Inc. GPU" {
		t.Errorf("name=%q", gpus[0].Name)
	}
}

func TestMergeDedupesByPCI(t *testing.T) {
	in := []GPU{
		{Vendor: VendorNVIDIA, Name: "NVIDIA GeForce RTX 4090", UUID: "GPU-1", PCIBusID: "00000000:01:00.0", MemoryTotalBytes: 24 << 30, Source: "nvidia-smi"},
		{Vendor: VendorNVIDIA, Name: "VGA compatible controller", PCIBusID: "0000:01:00.0", Source: "lspci"},
	}
	out := mergeGPUs(in)
	if len(out) != 1 {
		t.Fatalf("got %d, want 1", len(out))
	}
	if !strings.Contains(out[0].Name, "RTX 4090") {
		t.Errorf("kept generic name %q", out[0].Name)
	}
}

func TestMergeDedupesWMINVIDIAAlreadySeenBySMI(t *testing.T) {
	in := []GPU{
		{Vendor: VendorNVIDIA, Name: "NVIDIA GeForce RTX 2050", UUID: "GPU-5d0ce205", PCIBusID: "00000000:01:00.0", MemoryTotalBytes: 4 << 30, Source: "nvidia-smi"},
		{Vendor: VendorNVIDIA, Name: "NVIDIA GeForce RTX 2050", MemoryTotalBytes: 4 << 30, Source: "wmi"},
		{Vendor: VendorIntel, Name: "Intel(R) UHD Graphics", MemoryTotalBytes: 2 << 30, Source: "wmi"},
	}
	out := mergeGPUs(in)
	if len(out) != 2 {
		t.Fatalf("got %d GPUs, want 2 (one NVIDIA, one Intel): %+v", len(out), out)
	}
	if out[0].Vendor != VendorNVIDIA || !strings.Contains(out[0].Source, "wmi") {
		t.Errorf("nvidia row = %+v", out[0])
	}
	if out[1].Vendor != VendorIntel {
		t.Errorf("intel row = %+v", out[1])
	}
}

func TestMergeKeepsTwoWMIOnlyIdenticalCards(t *testing.T) {
	in := []GPU{
		{Vendor: VendorNVIDIA, Name: "NVIDIA GeForce RTX 4090", Source: "wmi"},
		{Vendor: VendorNVIDIA, Name: "NVIDIA GeForce RTX 4090", Source: "wmi"},
	}
	out := mergeGPUs(in)
	if len(out) != 2 {
		t.Fatalf("got %d, want 2 WMI-only copies", len(out))
	}
}

func TestDetectNVIDIA(t *testing.T) {
	r := &fakeRunner{
		bins: map[string]string{"nvidia-smi": "/usr/bin/nvidia-smi"},
		cmds: map[string]struct {
			stdout string
			stderr string
			err    error
		}{
			"/usr/bin/nvidia-smi --query-gpu=index,uuid,name,driver_version,memory.total,memory.used,utilization.gpu,utilization.memory,temperature.gpu,pci.bus_id,compute_cap --format=csv,noheader,nounits": {
				stdout: "0, GPU-x, Tesla T4, 535.104, 15360, 256, 3, 1, 35, 00000000:00:1e.0, 7.5\n",
			},
			"/usr/bin/nvidia-smi": {
				stdout: "CUDA Version: 12.2\n",
			},
		},
	}
	inv := DetectWith(context.Background(), "node-1", r)
	if len(inv.GPUs) != 1 {
		t.Fatalf("gpus=%d warnings=%v", len(inv.GPUs), inv.Warnings)
	}
	if inv.GPUs[0].CUDAVersion != "12.2" {
		t.Errorf("cuda=%q", inv.GPUs[0].CUDAVersion)
	}
	if inv.GPUs[0].Name != "Tesla T4" {
		t.Errorf("name=%q", inv.GPUs[0].Name)
	}
}

func TestSkipDummyAdapter(t *testing.T) {
	if !skipDummyAdapter("Microsoft Basic Display Adapter", "1414") {
		t.Fatal("expected skip")
	}
	if skipDummyAdapter("NVIDIA RTX 4090", "10de") {
		t.Fatal("should not skip")
	}
}

func TestParseROCmJSON(t *testing.T) {
	raw := `{
		"card0": {
			"Device Name": "AMD Instinct MI210",
			"PCI Bus": "0000:03:00.0",
			"VRAM Total Memory (B)": "68702699520",
			"GPU use (%)": "4",
			"Temperature (Sensor edge) (C)": "42"
		}
	}`
	gpus, err := parseROCmJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(gpus) != 1 || gpus[0].Vendor != VendorAMD {
		t.Fatalf("%+v", gpus)
	}
	if gpus[0].MemoryTotalBytes != 68702699520 {
		t.Errorf("mem=%d", gpus[0].MemoryTotalBytes)
	}
}

func TestDetectTimeoutDoesNotPanic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	r := &fakeRunner{bins: map[string]string{}, cmds: map[string]struct {
		stdout string
		stderr string
		err    error
	}{}}
	inv := DetectWith(ctx, "n", r)
	if inv.GPUs == nil {
		t.Fatal("gpus should be empty slice")
	}
}
