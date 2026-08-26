package gpuruntime

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// NVIDIA probes the NVIDIA/CUDA-facing tool path when present.
// It is one Runtime implementation — jobs must not call nvidia-smi directly.
type NVIDIA struct{}

func (NVIDIA) Name() string   { return "nvidia" }
func (NVIDIA) Vendor() string { return "nvidia" }

func (NVIDIA) Available(ctx context.Context) bool {
	_, err := exec.LookPath("nvidia-smi")
	return err == nil
}

func (n NVIDIA) Smoke(ctx context.Context) (SmokeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return SmokeResult{OK: false, Message: "nvidia-smi not found"}, err
	}
	out, err := exec.CommandContext(ctx, path,
		"--query-gpu=index,uuid,name,memory.total,driver_version",
		"--format=csv,noheader,nounits",
	).CombinedOutput()
	res := SmokeResult{
		Runtime: n.Name(),
		Vendor:  n.Vendor(),
		Details: map[string]any{"tool": "nvidia-smi"},
	}
	if err != nil {
		res.Message = strings.TrimSpace(string(out))
		if res.Message == "" {
			res.Message = err.Error()
		}
		return res, err
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := splitCSV(line)
		if len(parts) < 5 {
			continue
		}
		res.Devices = append(res.Devices, Device{
			ID:               strings.TrimSpace(parts[1]),
			Name:             strings.TrimSpace(parts[2]),
			MemoryTotalBytes: mibToBytes(strings.TrimSpace(parts[3])),
			DriverVersion:    strings.TrimSpace(parts[4]),
		})
	}
	if len(res.Devices) == 0 {
		res.Message = "nvidia runtime returned no devices"
		return res, nil
	}
	res.OK = true
	res.Message = "nvidia runtime smoke ok"
	return res, nil
}

func splitCSV(line string) []string {
	parts := strings.Split(line, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func mibToBytes(s string) uint64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f <= 0 {
		return 0
	}
	return uint64(f * 1024 * 1024)
}
