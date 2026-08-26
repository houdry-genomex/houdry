package server

import (
	"houdry/internal/gpu"
	"houdry/internal/host"
)

// enrichNode fills Resources and Runtimes from inventory + host metrics.
func enrichNode(n *Node, hostRes host.Resources, runtimes []string) {
	active := 0
	if n.CurrentJobID != "" || n.Status == StatusBusy {
		active = 1
	}
	n.Resources = BuildProfile(n.Inventory, hostRes, active)
	if len(runtimes) > 0 {
		n.Runtimes = append([]string(nil), runtimes...)
	}
}

// ClusterSummary is a compact multi-node overview.
type ClusterSummary struct {
	Nodes         int    `json:"nodes"`
	GPUs          int    `json:"gpus"`
	AvailableVRAM uint64 `json:"available_vram_bytes"`
	ReadyNodes    int    `json:"ready_nodes"`
	QueuedJobs    int    `json:"queued_jobs"`
}

func SummarizeCluster(nodes []Node, queuedJobs int) ClusterSummary {
	ready := 0
	for _, n := range nodes {
		if n.Status == StatusReady {
			ready++
		}
	}
	return ClusterSummary{
		Nodes:         len(nodes),
		GPUs:          TotalPhysicalGPUs(nodes),
		AvailableVRAM: AvailableVRAMBytes(nodes),
		ReadyNodes:    ready,
		QueuedJobs:    queuedJobs,
	}
}

// Ensure inventory GPUs are used when building from JoinRequest without host.
func defaultHostFromInventory(inv gpu.Inventory) host.Resources {
	h := host.Collect()
	if h.Arch == "" {
		h.Arch = inv.Host.Arch
	}
	return h
}
