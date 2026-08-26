package gpu

import (
	"context"
	"encoding/csv"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var cudaVersionRe = regexp.MustCompile(`(?i)CUDA Version:\s*([0-9.]+)`)

func nvidiaSmiCandidates() []string {
	cands := []string{"nvidia-smi"}
	if runtime.GOOS == "windows" {
		cands = append(cands,
			`C:\Windows\System32\nvidia-smi.exe`,
			`C:\Program Files\NVIDIA Corporation\NVSMI\nvidia-smi.exe`,
		)
	}
	return cands
}

func detectNVIDIA(ctx context.Context, r Runner) ([]GPU, []string, []string) {
	bin := firstExisting(r, nvidiaSmiCandidates()...)
	if bin == "" {
		return nil, nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	fields := "index,uuid,name,driver_version,memory.total,memory.used,utilization.gpu,utilization.memory,temperature.gpu,pci.bus_id,compute_cap"
	stdout, stderr, err := r.Run(ctx, bin, "--query-gpu="+fields, "--format=csv,noheader,nounits")
	if err != nil && strings.Contains(stderr+err.Error(), "not a valid field") {
		fields = "index,uuid,name,driver_version,memory.total,memory.used,utilization.gpu,utilization.memory,temperature.gpu,pci.bus_id"
		stdout, stderr, err = r.Run(ctx, bin, "--query-gpu="+fields, "--format=csv,noheader,nounits")
	}
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return nil, []string{"nvidia-smi"}, []string{"nvidia-smi: " + msg}
	}

	gpus, parseErr := parseNvidiaCSV(stdout, strings.Contains(fields, "compute_cap"))
	warnings := []string{}
	if parseErr != nil {
		warnings = append(warnings, "nvidia-smi: "+parseErr.Error())
	}
	if len(gpus) == 0 {
		return gpus, []string{"nvidia-smi"}, warnings
	}

	if cuda, w := nvidiaCUDAVersion(ctx, r, bin); cuda != "" {
		for i := range gpus {
			gpus[i].CUDAVersion = cuda
		}
	} else if w != "" {
		warnings = append(warnings, w)
	}
	return gpus, []string{"nvidia-smi"}, warnings
}

func nvidiaCUDAVersion(ctx context.Context, r Runner, bin string) (string, string) {
	stdout, stderr, err := r.Run(ctx, bin)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return "", "nvidia-smi cuda version: " + msg
	}
	m := cudaVersionRe.FindStringSubmatch(stdout)
	if len(m) < 2 {
		return "", ""
	}
	return m[1], ""
}

func parseNvidiaCSV(raw string, hasComputeCap bool) ([]GPU, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	reader := csv.NewReader(strings.NewReader(raw))
	reader.TrimLeadingSpace = true
	reader.ReuseRecord = false
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse csv: %w", err)
	}

	want := 10
	if hasComputeCap {
		want = 11
	}
	gpus := make([]GPU, 0, len(rows))
	for _, row := range rows {
		if len(row) < want {
			return gpus, fmt.Errorf("unexpected nvidia-smi columns: got %d want %d", len(row), want)
		}
		g := GPU{
			Vendor:   VendorNVIDIA,
			Source:   "nvidia-smi",
			UUID:     cleanSMI(row[1]),
			Name:     cleanSMI(row[2]),
			PCIBusID: cleanSMI(row[9]),
		}
		g.Index = atoiDefault(cleanSMI(row[0]), len(gpus))
		g.DriverVersion = cleanSMI(row[3])
		g.MemoryTotalBytes = mibToBytes(cleanSMI(row[4]))
		g.MemoryUsedBytes = mibToBytes(cleanSMI(row[5]))
		g.UtilizationGPU = parsePct(cleanSMI(row[6]))
		g.UtilizationMemory = parsePct(cleanSMI(row[7]))
		g.TemperatureC = parsePct(cleanSMI(row[8])) // same int parser
		if hasComputeCap {
			g.ComputeCapability = cleanSMI(row[10])
		}
		g.ID = nvidiaID(g)
		gpus = append(gpus, g)
	}
	return gpus, nil
}

func nvidiaID(g GPU) string {
	if g.UUID != "" {
		return g.UUID
	}
	if g.PCIBusID != "" {
		return "nvidia-" + strings.ToLower(g.PCIBusID)
	}
	return fmt.Sprintf("nvidia-%d", g.Index)
}

func cleanSMI(s string) string {
	s = strings.TrimSpace(s)
	switch strings.ToUpper(s) {
	case "", "N/A", "[N/A]", "[NOT SUPPORTED]", "NOT SUPPORTED":
		return ""
	}
	return s
}

func atoiDefault(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

func parsePct(s string) *int {
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSuffix(s, "%"))
	if err != nil {
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return nil
		}
		n = int(f)
	}
	return &n
}

func mibToBytes(s string) uint64 {
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return uint64(f * 1024 * 1024)
}
