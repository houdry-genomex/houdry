package gpu

import "time"

// Vendor is a normalized GPU vendor identifier.
type Vendor string

const (
	VendorNVIDIA Vendor = "nvidia"
	VendorAMD    Vendor = "amd"
	VendorIntel  Vendor = "intel"
	VendorApple  Vendor = "apple"
	VendorOther  Vendor = "other"
)

// GPU is the normalized GPU information model used by the rest of Houdry.
// Downstream components should depend on this struct, not on nvidia-smi,
// sysfs, WMI, or system_profiler.
type GPU struct {
	Index             int    `json:"index"`
	ID                string `json:"id"`
	UUID              string `json:"uuid,omitempty"`
	Vendor            Vendor `json:"vendor"`
	Name              string `json:"name"`
	MemoryTotalBytes  uint64 `json:"memory_total_bytes"`
	MemoryUsedBytes   uint64 `json:"memory_used_bytes,omitempty"`
	DriverVersion     string `json:"driver_version,omitempty"`
	CUDAVersion       string `json:"cuda_version,omitempty"`
	ComputeCapability string `json:"compute_capability,omitempty"`
	UtilizationGPU    *int   `json:"utilization_gpu_percent,omitempty"`
	UtilizationMemory *int   `json:"utilization_memory_percent,omitempty"`
	TemperatureC      *int   `json:"temperature_c,omitempty"`
	PCIBusID          string `json:"pci_bus_id,omitempty"`
	Source            string `json:"source"`
}

// Host describes the machine the GPUs were discovered on.
type Host struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Kernel   string `json:"kernel,omitempty"`
}

// Inventory is the complete result of local GPU discovery.
type Inventory struct {
	NodeID     string    `json:"node_id"`
	DetectedAt time.Time `json:"detected_at"`
	Host       Host      `json:"host"`
	GPUs       []GPU     `json:"gpus"`
	Sources    []string  `json:"sources,omitempty"`
	Warnings   []string  `json:"warnings,omitempty"`
}
