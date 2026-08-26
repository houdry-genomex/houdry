//go:build darwin

package gpu

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func detectPlatform(ctx context.Context, r Runner) ([]GPU, []string, []string) {
	bin := firstExisting(r, "system_profiler")
	if bin == "" {
		return nil, nil, []string{"system_profiler not found"}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stdout, stderr, err := r.Run(ctx, bin, "SPDisplaysDataType", "-json")
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return nil, []string{"system_profiler"}, []string{"system_profiler: " + msg}
	}
	gpus, parseErr := parseAppleDisplays(stdout)
	warnings := []string{}
	if parseErr != nil {
		warnings = append(warnings, "system_profiler: "+parseErr.Error())
	}
	return gpus, []string{"system_profiler"}, warnings
}

func parseAppleDisplays(raw string) ([]GPU, error) {
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	arr, _ := root["SPDisplaysDataType"].([]any)
	gpus := make([]GPU, 0, len(arr))
	for i, item := range arr {
		card, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := firstNonEmpty(asString(card["_name"]), asString(card["sppci_model"]), asString(card["spdisplays_device-id"]))
		vendorRaw := firstNonEmpty(asString(card["spdisplays_vendor"]), asString(card["sppci_vendor"]))
		g := GPU{
			Index:  i,
			Vendor: vendorFromName(vendorRaw + " " + name),
			Name:   name,
			Source: "system_profiler",
		}
		g.MemoryTotalBytes = parseAppleVRAM(firstNonEmpty(
			asString(card["spdisplays_vram"]),
			asString(card["spdisplays_vram_shared"]),
			asString(card["sppci_model"]),
		))
		if g.MemoryTotalBytes == 0 {
			g.MemoryTotalBytes = parseAppleVRAM(asString(card["spdisplays_vram_shared"]))
		}
		if cores := asString(card["sppci_cores"]); cores != "" {
			if g.Name != "" && !strings.Contains(g.Name, "core") {
				g.Name = fmt.Sprintf("%s (%s cores)", g.Name, cores)
			}
		}
		g.ID = appleID(g, i)
		if g.Name == "" {
			continue
		}
		gpus = append(gpus, g)
	}
	return gpus, nil
}

func appleID(g GPU, i int) string {
	if g.Name != "" {
		return fmt.Sprintf("apple-%s", strings.ToLower(strings.ReplaceAll(g.Name, " ", "-")))
	}
	return fmt.Sprintf("apple-gpu-%d", i)
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		if t == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func parseAppleVRAM(s string) uint64 {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(strings.ToLower(s), "dynamic") || strings.Contains(strings.ToLower(s), "shared") {
		return 0
	}
	s = strings.ReplaceAll(s, ",", "")
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	unit := ""
	if len(fields) > 1 {
		unit = strings.ToLower(fields[1])
	} else {
		unit = "mb"
	}
	switch {
	case strings.HasPrefix(unit, "g"):
		return uint64(n * 1024 * 1024 * 1024)
	case strings.HasPrefix(unit, "m"):
		return uint64(n * 1024 * 1024)
	default:
		return uint64(n)
	}
}
