package host

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Resources captures host-level CPU/RAM for node profiles.
type Resources struct {
	CPUCores         int    `json:"cpu_cores"`
	MemoryTotalBytes uint64 `json:"memory_total_bytes"`
	MemoryUsedBytes  uint64 `json:"memory_used_bytes,omitempty"`
	Arch             string `json:"arch"`
}

// Collect returns best-effort host CPU/RAM information.
func Collect() Resources {
	r := Resources{
		CPUCores: runtime.NumCPU(),
		Arch:     runtime.GOARCH,
	}
	total, used := memoryBytes()
	r.MemoryTotalBytes = total
	r.MemoryUsedBytes = used
	return r
}

func memoryBytes() (total, used uint64) {
	// Linux /proc/meminfo
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	var memTotal, memAvailable uint64
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		n, _ := strconv.ParseUint(fields[1], 10, 64)
		n *= 1024 // kB → bytes
		switch fields[0] {
		case "MemTotal:":
			memTotal = n
		case "MemAvailable:":
			memAvailable = n
		}
	}
	if memTotal == 0 {
		return 0, 0
	}
	if memAvailable > memTotal {
		memAvailable = memTotal
	}
	return memTotal, memTotal - memAvailable
}
