package gpu

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func detectAMD(ctx context.Context, r Runner) ([]GPU, []string, []string) {
	bin := firstExisting(r, "rocm-smi", "rocm_smi.py")
	if bin == "" {
		return nil, nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	stdout, stderr, err := r.Run(ctx, bin, "--showproductname", "--showmeminfo", "vram", "--showuse", "--showtemp", "--showbus", "--json")
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return nil, []string{"rocm-smi"}, []string{"rocm-smi: " + msg}
	}

	gpus, parseErr := parseROCmJSON(stdout)
	warnings := []string{}
	if parseErr != nil {
		warnings = append(warnings, "rocm-smi: "+parseErr.Error())
	}
	return gpus, []string{"rocm-smi"}, warnings
}

func parseROCmJSON(raw string) ([]GPU, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}

	gpus := make([]GPU, 0)
	index := 0
	for key, val := range root {
		card, ok := val.(map[string]any)
		if !ok {
			continue
		}
		if !strings.Contains(strings.ToLower(key), "card") && lookupROCm(card, "Device Name") == "" && lookupROCm(card, "Card Series") == "" {
			continue
		}
		g := GPU{
			Index:    index,
			Vendor:   VendorAMD,
			Source:   "rocm-smi",
			Name:     firstNonEmpty(lookupROCm(card, "Device Name"), lookupROCm(card, "Card Series"), lookupROCm(card, "Card Model"), key),
			PCIBusID: firstNonEmpty(lookupROCm(card, "PCI Bus"), lookupROCm(card, "PCI Bus Number")),
		}
		g.DriverVersion = firstNonEmpty(lookupROCm(card, "Driver version"), lookupROCm(card, "ROCm Version"))
		if total := parseROCmBytes(firstNonEmpty(lookupROCm(card, "VRAM Total Memory (B)"), lookupROCm(card, "VRAM Total Memory"))); total > 0 {
			g.MemoryTotalBytes = total
		}
		if used := parseROCmBytes(firstNonEmpty(lookupROCm(card, "VRAM Total Used Memory (B)"), lookupROCm(card, "VRAM Total Used Memory"))); used > 0 {
			g.MemoryUsedBytes = used
		}
		g.UtilizationGPU = parsePct(lookupROCm(card, "GPU use (%)"))
		if t := parsePct(firstNonEmpty(
			lookupROCm(card, "Temperature (Sensor edge) (C)"),
			lookupROCm(card, "Temperature (Sensor junction) (C)"),
			lookupROCm(card, "Temperature (Sensor memory) (C)"),
		)); t != nil {
			g.TemperatureC = t
		}
		g.ID = amdID(g, key)
		gpus = append(gpus, g)
		index++
	}
	return gpus, nil
}

func amdID(g GPU, key string) string {
	if g.PCIBusID != "" {
		return "amd-" + strings.ToLower(g.PCIBusID)
	}
	return "amd-" + strings.ToLower(key)
}

func lookupROCm(card map[string]any, key string) string {
	if v, ok := card[key]; ok {
		return stringify(v)
	}
	lower := strings.ToLower(key)
	for k, v := range card {
		if strings.ToLower(k) == lower {
			return stringify(v)
		}
	}
	return ""
}

func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func parseROCmBytes(s string) uint64 {
	s = strings.TrimSpace(strings.TrimSuffix(strings.ToUpper(s), "B"))
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return 0
		}
		return uint64(f)
	}
	return n
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
