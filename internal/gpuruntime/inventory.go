package gpuruntime

import (
	"context"
	"fmt"

	"houdry/internal/gpu"
)

// Inventory is a fallback runtime that uses Houdry's normalized discovery
// layer. It keeps smoke workable when vendor tools are absent but detectors
// still found a GPU (for example sysfs / system_profiler / WMI).
type Inventory struct{}

func (Inventory) Name() string   { return "inventory" }
func (Inventory) Vendor() string { return "any" }

func (Inventory) Available(ctx context.Context) bool {
	return true
}

func (Inventory) Smoke(ctx context.Context) (SmokeResult, error) {
	inv := gpu.Detect(ctx, "")
	res := SmokeResult{
		Runtime: "inventory",
		Vendor:  "any",
		Details: map[string]any{
			"sources":  inv.Sources,
			"warnings": inv.Warnings,
			"host": map[string]any{
				"hostname": inv.Host.Hostname,
				"os":       inv.Host.OS,
				"arch":     inv.Host.Arch,
			},
		},
	}
	for _, g := range inv.GPUs {
		res.Devices = append(res.Devices, Device{
			ID:               g.ID,
			Name:             g.Name,
			MemoryTotalBytes: g.MemoryTotalBytes,
			DriverVersion:    g.DriverVersion,
			Extra:            string(g.Vendor),
		})
	}
	if len(res.Devices) == 0 {
		res.Message = "inventory runtime found no GPUs"
		return res, fmt.Errorf("no GPUs in inventory")
	}
	res.OK = true
	res.Message = "inventory runtime smoke ok"
	return res, nil
}
