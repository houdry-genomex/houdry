// Package gpuruntime defines vendor-agnostic GPU runtime adapters used by
// workloads such as gpu.smoke. Job types talk to Runtime, never to nvidia-smi
// or other vendor tools directly.
package gpuruntime

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Device is a runtime-visible GPU after a smoke probe.
type Device struct {
	ID               string `json:"id,omitempty"`
	Name             string `json:"name"`
	MemoryTotalBytes uint64 `json:"memory_total_bytes,omitempty"`
	DriverVersion    string `json:"driver_version,omitempty"`
	Extra            string `json:"extra,omitempty"`
}

// SmokeResult is the vendor-neutral output of a smoke probe.
type SmokeResult struct {
	Runtime  string         `json:"runtime"`
	Vendor   string         `json:"vendor,omitempty"`
	OK       bool           `json:"ok"`
	Message  string         `json:"message,omitempty"`
	Devices  []Device       `json:"devices"`
	Duration time.Duration  `json:"duration_ms"` // filled as milliseconds in JSON via custom? use ms int
	Details  map[string]any `json:"details,omitempty"`
}

// Runtime is a pluggable GPU backend (CUDA/NVIDIA, ROCm/AMD, Metal, etc.).
type Runtime interface {
	// Name is a stable runtime id, e.g. "nvidia-cuda", "amd-rocm", "inventory".
	Name() string
	// Vendor is a coarse vendor hint, e.g. "nvidia", "amd", "any".
	Vendor() string
	// Available reports whether this runtime can be used on this host.
	Available(ctx context.Context) bool
	// Smoke runs a lightweight liveness probe against the runtime/GPU path.
	Smoke(ctx context.Context) (SmokeResult, error)
}

// DefaultRuntimes returns the built-in runtime adapters in preference order.
func DefaultRuntimes() []Runtime {
	return []Runtime{
		NVIDIA{},
		AMD{},
		Inventory{},
	}
}

// SelectAvailable returns runtimes that report Available on this host.
func SelectAvailable(ctx context.Context, all []Runtime) []Runtime {
	out := make([]Runtime, 0, len(all))
	for _, r := range all {
		if r.Available(ctx) {
			out = append(out, r)
		}
	}
	return out
}

// SmokeAll runs Smoke on every available runtime and aggregates results.
// It succeeds if at least one runtime reports OK with one or more devices.
//
// Counts are separated so schedulers never confuse probes with GPUs:
//   - physical_device_count: unique devices across successful probes
//   - runtime_probe_count: number of runtime smoke attempts that ran
//   - ok_runtime_count: runtimes that reported OK
func SmokeAll(ctx context.Context, runtimes []Runtime) (map[string]any, error) {
	available := SelectAvailable(ctx, runtimes)
	probes := make([]SmokeResult, 0, len(available))
	okCount := 0
	seen := map[string]Device{}

	for _, r := range available {
		start := time.Now()
		res, err := r.Smoke(ctx)
		res.Runtime = r.Name()
		if res.Vendor == "" {
			res.Vendor = r.Vendor()
		}
		res.Duration = time.Since(start)
		if err != nil {
			res.OK = false
			if res.Message == "" {
				res.Message = err.Error()
			}
		}
		if res.OK {
			okCount++
			for _, d := range res.Devices {
				key := deviceKey(d)
				if _, exists := seen[key]; !exists {
					seen[key] = d
				}
			}
		}
		probes = append(probes, res)
	}

	physical := make([]Device, 0, len(seen))
	for _, d := range seen {
		physical = append(physical, d)
	}

	out := map[string]any{
		"workload":              "gpu.smoke",
		"ok":                    false,
		"physical_device_count": len(physical),
		"runtime_probe_count":   len(probes),
		"ok_runtime_count":      okCount,
		"physical_devices":      physical,
		"probes":                probesJSON(probes),
	}
	if len(available) == 0 {
		return out, fmt.Errorf("gpu.smoke: no GPU runtime available on this node")
	}
	if okCount == 0 || len(physical) == 0 {
		return out, fmt.Errorf("gpu.smoke: no runtime reported a live GPU")
	}
	out["ok"] = true
	return out, nil
}

func deviceKey(d Device) string {
	if id := strings.TrimSpace(strings.ToLower(d.ID)); id != "" {
		return "id:" + id
	}
	name := strings.TrimSpace(strings.ToLower(d.Name))
	return fmt.Sprintf("name:%s:%d", name, d.MemoryTotalBytes)
}

func probesJSON(probes []SmokeResult) []map[string]any {
	out := make([]map[string]any, 0, len(probes))
	for _, p := range probes {
		out = append(out, map[string]any{
			"runtime":     p.Runtime,
			"vendor":      p.Vendor,
			"ok":          p.OK,
			"message":     p.Message,
			"devices":     p.Devices,
			"duration_ms": p.Duration.Milliseconds(),
			"details":     p.Details,
		})
	}
	return out
}
