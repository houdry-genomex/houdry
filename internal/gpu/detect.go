package gpu

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Detect inspects the local machine and returns a normalized GPU inventory.
func Detect(ctx context.Context, nodeID string) Inventory {
	return DetectWith(ctx, nodeID, OSRunner{})
}

// DetectWith is Detect using a custom Runner (tests).
func DetectWith(ctx context.Context, nodeID string, r Runner) Inventory {
	if ctx == nil {
		ctx = context.Background()
	}
	inv := Inventory{
		NodeID:     nodeID,
		DetectedAt: time.Now().UTC(),
		Host:       currentHost(ctx, r),
		GPUs:       []GPU{},
		Sources:    []string{},
		Warnings:   []string{},
	}

	add := func(gpus []GPU, sources, warnings []string) {
		inv.GPUs = append(inv.GPUs, gpus...)
		inv.Sources = appendUnique(inv.Sources, sources...)
		inv.Warnings = append(inv.Warnings, warnings...)
	}

	add(detectNVIDIA(ctx, r))
	add(detectAMD(ctx, r))
	add(detectPlatform(ctx, r))

	inv.GPUs = mergeGPUs(inv.GPUs)
	for i := range inv.GPUs {
		inv.GPUs[i].Index = i
		if inv.GPUs[i].ID == "" {
			inv.GPUs[i].ID = fmt.Sprintf("gpu-%d", i)
		}
	}
	sort.SliceStable(inv.Sources, func(i, j int) bool { return inv.Sources[i] < inv.Sources[j] })
	return inv
}

func currentHost(ctx context.Context, r Runner) Host {
	h := Host{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	}
	h.Hostname, _ = os.Hostname()
	if out, _, err := r.Run(ctx, "uname", "-r"); err == nil {
		h.Kernel = strings.TrimSpace(out)
	}
	return h
}

func mergeGPUs(in []GPU) []GPU {
	if len(in) == 0 {
		return []GPU{}
	}
	out := make([]GPU, 0, len(in))
	byUUID := map[string]int{}
	byPCI := map[string]int{}
	byVendorName := map[string]int{}
	indexOf := func(g GPU) int {
		if g.UUID != "" {
			if i, ok := byUUID[strings.ToLower(g.UUID)]; ok {
				return i
			}
		}
		if g.PCIBusID != "" {
			if i, ok := byPCI[normalizePCI(g.PCIBusID)]; ok {
				return i
			}
		}
		// WMI/lspci often omit UUID and PCI. Fold those onto a richer record
		// (nvidia-smi) of the same vendor+name so one physical card is not
		// counted twice. Do not key two UUID-less copies together: two identical
		// cards reported only by WMI must stay two rows.
		if g.UUID == "" && g.PCIBusID == "" {
			if i, ok := byVendorName[vendorNameKey(g)]; ok {
				return i
			}
		}
		return -1
	}
	remember := func(g GPU, i int) {
		if g.UUID != "" {
			byUUID[strings.ToLower(g.UUID)] = i
		}
		if g.PCIBusID != "" {
			byPCI[normalizePCI(g.PCIBusID)] = i
		}
		if g.UUID != "" || g.PCIBusID != "" {
			if k := vendorNameKey(g); k != "" {
				byVendorName[k] = i
			}
		}
	}
	for _, g := range in {
		if i := indexOf(g); i >= 0 {
			out[i] = richer(out[i], g)
			remember(out[i], i)
			continue
		}
		remember(g, len(out))
		out = append(out, g)
	}
	return out
}

func vendorNameKey(g GPU) string {
	name := normalizeGPUName(g.Name)
	if g.Vendor == "" || name == "" || looksGeneric(g.Name) {
		return ""
	}
	return string(g.Vendor) + "|" + name
}

func normalizeGPUName(name string) string {
	n := strings.ToLower(name)
	n = strings.ReplaceAll(n, "(r)", "")
	n = strings.ReplaceAll(n, "(tm)", "")
	n = strings.Join(strings.Fields(n), " ")
	return n
}

func normalizePCI(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	s = strings.TrimPrefix(s, "00000000:")
	s = strings.TrimPrefix(s, "0000:")
	return s
}

func richer(a, b GPU) GPU {
	if a.Name == "" || (looksGeneric(a.Name) && !looksGeneric(b.Name) && b.Name != "") {
		a.Name = b.Name
	}
	if a.UUID == "" {
		a.UUID = b.UUID
	}
	if a.DriverVersion == "" {
		a.DriverVersion = b.DriverVersion
	}
	if a.CUDAVersion == "" {
		a.CUDAVersion = b.CUDAVersion
	}
	if a.ComputeCapability == "" {
		a.ComputeCapability = b.ComputeCapability
	}
	if a.PCIBusID == "" {
		a.PCIBusID = b.PCIBusID
	}
	if a.MemoryTotalBytes == 0 || b.MemoryTotalBytes > a.MemoryTotalBytes {
		if b.MemoryTotalBytes > 0 {
			a.MemoryTotalBytes = b.MemoryTotalBytes
		}
	}
	if a.MemoryUsedBytes == 0 {
		a.MemoryUsedBytes = b.MemoryUsedBytes
	}
	if a.UtilizationGPU == nil {
		a.UtilizationGPU = b.UtilizationGPU
	}
	if a.UtilizationMemory == nil {
		a.UtilizationMemory = b.UtilizationMemory
	}
	if a.TemperatureC == nil {
		a.TemperatureC = b.TemperatureC
	}
	if a.Source != b.Source && b.Source != "" {
		a.Source = a.Source + "+" + b.Source
	}
	if a.Vendor == VendorOther && b.Vendor != VendorOther && b.Vendor != "" {
		a.Vendor = b.Vendor
	}
	return a
}

func looksGeneric(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "pci ") || n == "unknown" || n == "" ||
		strings.Contains(n, "vga compatible") || strings.Contains(n, "3d controller")
}

func appendUnique(dst []string, extra ...string) []string {
	have := map[string]bool{}
	for _, s := range dst {
		have[s] = true
	}
	for _, s := range extra {
		if s == "" || have[s] {
			continue
		}
		have[s] = true
		dst = append(dst, s)
	}
	return dst
}

func vendorFromPCI(id string) Vendor {
	id = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(id), "0x"))
	switch id {
	case "10de":
		return VendorNVIDIA
	case "1002", "1022":
		return VendorAMD
	case "8086":
		return VendorIntel
	case "106b":
		return VendorApple
	default:
		return VendorOther
	}
}

func vendorFromName(s string) Vendor {
	n := strings.ToLower(s)
	switch {
	case strings.Contains(n, "apple"):
		return VendorApple
	case strings.Contains(n, "nvidia") || strings.Contains(n, "geforce") || strings.Contains(n, "quadro") || strings.Contains(n, "tesla") || strings.Contains(n, "rtx ") || strings.Contains(n, "gtx "):
		return VendorNVIDIA
	case strings.Contains(n, "amd") || strings.Contains(n, "radeon") || strings.Contains(n, "ati ") || strings.Contains(n, "instinct"):
		return VendorAMD
	case strings.Contains(n, "intel") || strings.Contains(n, "arc "):
		return VendorIntel
	default:
		return VendorOther
	}
}

func skipDummyAdapter(name, vendorID string) bool {
	n := strings.ToLower(name)
	v := strings.ToLower(strings.TrimPrefix(vendorID, "0x"))
	if v == "1414" || v == "15ad" || v == "1234" || v == "1af4" {
		return true
	}
	dummies := []string{
		"microsoft basic display",
		"microsoft hyper-v",
		"virtualbox",
		"vmware svga",
		"qxl",
		"aspeed",
		"cirrus",
	}
	for _, d := range dummies {
		if strings.Contains(n, d) {
			return true
		}
	}
	return false
}
