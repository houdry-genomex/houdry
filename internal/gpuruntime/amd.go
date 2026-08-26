package gpuruntime

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// AMD probes the ROCm tool path when present.
type AMD struct{}

func (AMD) Name() string   { return "amd-rocm" }
func (AMD) Vendor() string { return "amd" }

func (AMD) Available(ctx context.Context) bool {
	if _, err := exec.LookPath("rocm-smi"); err == nil {
		return true
	}
	_, err := exec.LookPath("rocm_smi.py")
	return err == nil
}

func (a AMD) Smoke(ctx context.Context) (SmokeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	bin := "rocm-smi"
	if _, err := exec.LookPath(bin); err != nil {
		bin = "rocm_smi.py"
	}
	out, err := exec.CommandContext(ctx, bin, "--showproductname", "--showmeminfo", "vram", "--json").CombinedOutput()
	res := SmokeResult{
		Runtime: a.Name(),
		Vendor:  a.Vendor(),
		Details: map[string]any{"tool": bin},
	}
	if err != nil {
		res.Message = strings.TrimSpace(string(out))
		if res.Message == "" {
			res.Message = err.Error()
		}
		return res, err
	}

	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		res.Message = "rocm json parse failed"
		return res, err
	}
	for key, val := range root {
		card, ok := val.(map[string]any)
		if !ok {
			continue
		}
		name := firstString(card, "Device Name", "Card Series", "Card Model")
		if name == "" && !strings.Contains(strings.ToLower(key), "card") {
			continue
		}
		if name == "" {
			name = key
		}
		res.Devices = append(res.Devices, Device{
			ID:               key,
			Name:             name,
			MemoryTotalBytes: parseBytes(firstString(card, "VRAM Total Memory (B)", "VRAM Total Memory")),
		})
	}
	if len(res.Devices) == 0 {
		res.Message = "amd-rocm runtime returned no devices"
		return res, nil
	}
	res.OK = true
	res.Message = "amd-rocm runtime smoke ok"
	return res, nil
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				if s := strings.TrimSpace(t); s != "" {
					return s
				}
			default:
				s := strings.TrimSpace(stringify(t))
				if s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func stringify(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return strings.Trim(string(b), `"`)
}

func parseBytes(s string) uint64 {
	s = strings.TrimSpace(strings.TrimSuffix(strings.ToUpper(s), "B"))
	s = strings.TrimSpace(s)
	var n uint64
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			break
		}
		n = n*10 + uint64(s[i]-'0')
	}
	return n
}
