package server

import (
	"houdry/internal/gpu"
	"houdry/internal/host"
	"houdry/internal/modelruntime"
)

// StaticGPU is capacity that does not change between heartbeats (identity/model).
type StaticGPU struct {
	ID                string     `json:"id"`
	UUID              string     `json:"uuid,omitempty"`
	Vendor            gpu.Vendor `json:"vendor"`
	Name              string     `json:"name"`
	MemoryTotalBytes  uint64     `json:"memory_total_bytes"`
	ComputeCapability string     `json:"compute_capability,omitempty"`
	PCIBusID          string     `json:"pci_bus_id,omitempty"`
}

// DynamicGPU is live utilization / free capacity.
type DynamicGPU struct {
	ID                   string `json:"id"`
	MemoryUsedBytes      uint64 `json:"memory_used_bytes,omitempty"`
	MemoryAvailableBytes uint64 `json:"memory_available_bytes"`
	UtilizationGPU       *int   `json:"utilization_gpu_percent,omitempty"`
	UtilizationMemory    *int   `json:"utilization_memory_percent,omitempty"`
}

// StaticResources are relatively fixed node attributes.
type StaticResources struct {
	CPUCores         int         `json:"cpu_cores"`
	MemoryTotalBytes uint64      `json:"memory_total_bytes"`
	Arch             string      `json:"arch"`
	GPUs             []StaticGPU `json:"gpus"`
}

// DynamicResources change over time.
type DynamicResources struct {
	MemoryUsedBytes uint64       `json:"memory_used_bytes,omitempty"`
	ActiveJobs      int          `json:"active_jobs"`
	GPUs            []DynamicGPU `json:"gpus"`
}

// ResourceProfile is the normalized node resource view used by the scheduler.
type ResourceProfile struct {
	Static  StaticResources  `json:"static"`
	Dynamic DynamicResources `json:"dynamic"`
}

// Requirements is a framework-agnostic workload request.
// The scheduler never cares whether the job came from CLI, UI, or an agent app,
// and never depends on a specific model runtime vendor.
//
// Model identity:
//
//	ModelName + ModelTag  → logical model (e.g. qwen2 + 0.5b)
//	ModelRuntime          → optional preferred backend (ollama | vllm | …)
//	Model                 → convenience "name" or "name:tag" (normalized on create)
type Requirements struct {
	GPURequired         bool   `json:"gpu_required"`
	MinVRAMBytes        uint64 `json:"min_vram_bytes,omitempty"`
	ModelName           string `json:"model_name,omitempty"`
	ModelTag            string `json:"model_tag,omitempty"`
	ModelRuntime        string `json:"model_runtime,omitempty"`
	RequireModelPresent bool   `json:"require_model_present,omitempty"`
	// Model is optional shorthand ("tinyllama" or "qwen2:0.5b"). Prefer ModelName/ModelTag.
	Model string `json:"model,omitempty"`
}

// ModelIdentity returns the runtime-agnostic model key for scheduling.
func (r Requirements) ModelIdentity() modelruntime.Identity {
	id := modelruntime.Identity{
		Name:    r.ModelName,
		Tag:     r.ModelTag,
		Runtime: r.ModelRuntime,
	}
	if id.Name == "" && r.Model != "" {
		parsed := modelruntime.ParseRef(r.Model)
		id.Name = parsed.Name
		if id.Tag == "" {
			id.Tag = parsed.Tag
		}
	}
	return id
}

// NormalizeModelFields fills ModelName/ModelTag from Model shorthand when needed.
func (r *Requirements) NormalizeModelFields() {
	if r.ModelName == "" && r.Model != "" {
		parsed := modelruntime.ParseRef(r.Model)
		r.ModelName = parsed.Name
		if r.ModelTag == "" {
			r.ModelTag = parsed.Tag
		}
	}
	if r.Model == "" && r.ModelName != "" {
		r.Model = modelruntime.Identity{Name: r.ModelName, Tag: r.ModelTag}.Ref()
	}
}

// BuildProfile constructs a resource profile from discovery + host metrics.
// Physical GPUs come from inventory only — never from runtime probe counts.
func BuildProfile(inv gpu.Inventory, hostRes host.Resources, activeJobs int) ResourceProfile {
	staticGPUs := make([]StaticGPU, 0, len(inv.GPUs))
	dynGPUs := make([]DynamicGPU, 0, len(inv.GPUs))
	for _, g := range inv.GPUs {
		id := g.ID
		if id == "" {
			id = g.UUID
		}
		staticGPUs = append(staticGPUs, StaticGPU{
			ID:                id,
			UUID:              g.UUID,
			Vendor:            g.Vendor,
			Name:              g.Name,
			MemoryTotalBytes:  g.MemoryTotalBytes,
			ComputeCapability: g.ComputeCapability,
			PCIBusID:          g.PCIBusID,
		})
		avail := uint64(0)
		if g.MemoryTotalBytes >= g.MemoryUsedBytes {
			avail = g.MemoryTotalBytes - g.MemoryUsedBytes
		}
		dynGPUs = append(dynGPUs, DynamicGPU{
			ID:                   id,
			MemoryUsedBytes:      g.MemoryUsedBytes,
			MemoryAvailableBytes: avail,
			UtilizationGPU:       g.UtilizationGPU,
			UtilizationMemory:    g.UtilizationMemory,
		})
	}
	return ResourceProfile{
		Static: StaticResources{
			CPUCores:         hostRes.CPUCores,
			MemoryTotalBytes: hostRes.MemoryTotalBytes,
			Arch:             firstNonEmpty(hostRes.Arch, inv.Host.Arch),
			GPUs:             staticGPUs,
		},
		Dynamic: DynamicResources{
			MemoryUsedBytes: hostRes.MemoryUsedBytes,
			ActiveJobs:      activeJobs,
			GPUs:            dynGPUs,
		},
	}
}

// Fits reports whether a READY node's resources satisfy workload requirements.
func Fits(n Node, req Requirements) bool {
	if n.Status != StatusReady {
		return false
	}

	needsGPU := req.GPURequired || req.MinVRAMBytes > 0 || req.ModelIdentity().Name != ""
	gpus := n.Resources.Static.GPUs
	if len(gpus) == 0 {
		for _, g := range n.GPUs {
			gpus = append(gpus, StaticGPU{
				ID:               g.ID,
				MemoryTotalBytes: g.MemoryTotalBytes,
			})
		}
	}
	if needsGPU && req.GPURequired && len(gpus) == 0 {
		return false
	}
	if req.MinVRAMBytes > 0 {
		dyn := map[string]DynamicGPU{}
		for _, d := range n.Resources.Dynamic.GPUs {
			dyn[d.ID] = d
		}
		okVRAM := false
		for _, g := range gpus {
			avail := g.MemoryTotalBytes
			if d, ok := dyn[g.ID]; ok {
				switch {
				case d.MemoryAvailableBytes > 0:
					avail = d.MemoryAvailableBytes
				case g.MemoryTotalBytes >= d.MemoryUsedBytes:
					avail = g.MemoryTotalBytes - d.MemoryUsedBytes
				}
			}
			if avail >= req.MinVRAMBytes {
				okVRAM = true
				break
			}
		}
		if !okVRAM {
			return false
		}
	}

	id := req.ModelIdentity()
	if id.Name == "" {
		return true
	}

	// Model workloads need a model runtime on the node (any, or the preferred one).
	if id.Runtime != "" {
		if !hasRuntime(n.ModelRuntimes, id.Runtime) {
			return false
		}
	} else if len(n.ModelRuntimes) == 0 {
		return false
	}

	has := modelruntime.HasModel(n.Models, id)
	if req.RequireModelPresent {
		return has
	}
	// Default: allow assignment if a runtime can pull, or model already present.
	return has || len(n.ModelRuntimes) > 0
}

func hasRuntime(runtimes []string, want string) bool {
	for _, r := range runtimes {
		if r == want {
			return true
		}
	}
	return false
}

// ModelScore ranks nodes for a model job. Higher is better.
// Prefer LOADED > AVAILABLE > pull-capable runtime. Runtime-agnostic.
func ModelScore(n Node, req Requirements) int {
	id := req.ModelIdentity()
	if id.Name == "" {
		return 0
	}
	score := 0
	if modelruntime.IsLoaded(n.Models, id) {
		score += 100
	} else if modelruntime.HasModel(n.Models, id) {
		score += 50
	}
	if id.Runtime != "" {
		if hasRuntime(n.ModelRuntimes, id.Runtime) {
			score += 10
		}
	} else if len(n.ModelRuntimes) > 0 {
		score += 10
	}
	return score
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// TotalPhysicalGPUs counts unique physical GPUs across nodes.
func TotalPhysicalGPUs(nodes []Node) int {
	n := 0
	for _, node := range nodes {
		if len(node.Resources.Static.GPUs) > 0 {
			n += len(node.Resources.Static.GPUs)
		} else {
			n += len(node.GPUs)
		}
	}
	return n
}

// AvailableVRAMBytes sums free/total VRAM on READY nodes.
func AvailableVRAMBytes(nodes []Node) uint64 {
	var sum uint64
	for _, node := range nodes {
		if node.Status != StatusReady {
			continue
		}
		if len(node.Resources.Dynamic.GPUs) > 0 {
			for _, g := range node.Resources.Dynamic.GPUs {
				sum += g.MemoryAvailableBytes
			}
			continue
		}
		for _, g := range node.GPUs {
			if g.MemoryTotalBytes >= g.MemoryUsedBytes {
				sum += g.MemoryTotalBytes - g.MemoryUsedBytes
			} else {
				sum += g.MemoryTotalBytes
			}
		}
	}
	return sum
}
