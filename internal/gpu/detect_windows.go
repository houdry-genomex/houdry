//go:build windows

package gpu

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var pnpVenRe = regexp.MustCompile(`(?i)VEN_([0-9A-F]{4})`)
var pnpDevRe = regexp.MustCompile(`(?i)DEV_([0-9A-F]{4})`)

func detectPlatform(ctx context.Context, r Runner) ([]GPU, []string, []string) {
	shell := firstExisting(r, "powershell", "pwsh", `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`)
	if shell == "" {
		return nil, nil, []string{"powershell not found; cannot query Win32_VideoController"}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	script := `Get-CimInstance -ClassName Win32_VideoController | Select-Object Name, AdapterRAM, DriverVersion, PNPDeviceID, VideoProcessor, AdapterCompatibility, Status | ConvertTo-Json -Compress`
	stdout, stderr, err := r.Run(ctx, shell, "-NoProfile", "-NonInteractive", "-Command", script)
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return nil, []string{"wmi"}, []string{"wmi: " + msg}
	}
	gpus, parseErr := parseWindowsVideoJSON(stdout)
	warnings := []string{}
	if parseErr != nil {
		warnings = append(warnings, "wmi: "+parseErr.Error())
	}
	return gpus, []string{"wmi"}, warnings
}

func parseWindowsVideoJSON(raw string) ([]GPU, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var items []map[string]any
	if strings.HasPrefix(raw, "{") {
		var one map[string]any
		if err := json.Unmarshal([]byte(raw), &one); err != nil {
			return nil, fmt.Errorf("parse json: %w", err)
		}
		items = []map[string]any{one}
	} else {
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			return nil, fmt.Errorf("parse json: %w", err)
		}
	}

	gpus := make([]GPU, 0, len(items))
	for i, item := range items {
		name := stringify(item["Name"])
		pnp := stringify(item["PNPDeviceID"])
		vendorID := ""
		if m := pnpVenRe.FindStringSubmatch(pnp); len(m) == 2 {
			vendorID = m[1]
		}
		if skipDummyAdapter(name, vendorID) {
			continue
		}
		g := GPU{
			Index:         i,
			Vendor:        vendorFromPCI(vendorID),
			Name:          name,
			DriverVersion: stringify(item["DriverVersion"]),
			Source:        "wmi",
		}
		if g.Name == "" {
			g.Name = stringify(item["VideoProcessor"])
		}
		if compat := stringify(item["AdapterCompatibility"]); g.Vendor == VendorOther && compat != "" {
			g.Vendor = vendorFromName(compat + " " + g.Name)
		}
		if ram := windowsAdapterRAM(item["AdapterRAM"]); ram > 0 {
			g.MemoryTotalBytes = ram
		}
		g.ID = windowsID(g, pnp, i)
		if g.Name == "" {
			continue
		}
		gpus = append(gpus, g)
	}
	return gpus, nil
}

func windowsAdapterRAM(v any) uint64 {
	switch t := v.(type) {
	case float64:
		if t < 0 {
			return 0
		}
		return uint64(t)
	case string:
		n, _ := strconv.ParseUint(strings.TrimSpace(t), 10, 64)
		return n
	default:
		s := stringify(v)
		n, _ := strconv.ParseUint(s, 10, 64)
		return n
	}
}

func windowsID(g GPU, pnp string, i int) string {
	if m := pnpDevRe.FindStringSubmatch(pnp); len(m) == 2 {
		return strings.ToLower(string(g.Vendor)) + "-" + strings.ToLower(m[1])
	}
	if g.Name != "" {
		return strings.ToLower(string(g.Vendor)) + "-" + strings.ToLower(strings.ReplaceAll(g.Name, " ", "-"))
	}
	return fmt.Sprintf("windows-gpu-%d", i)
}
