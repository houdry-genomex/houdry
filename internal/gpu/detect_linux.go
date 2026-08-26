//go:build linux

package gpu

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var drmCardRe = regexp.MustCompile(`^card[0-9]+$`)

func detectPlatform(ctx context.Context, r Runner) ([]GPU, []string, []string) {
	return detectLinux(ctx, r)
}

func detectLinux(ctx context.Context, r Runner) ([]GPU, []string, []string) {
	const drmDir = "/sys/class/drm"
	entries, err := r.ReadDir(drmDir)
	if err != nil {
		gpus, src, warn := detectLSPCI(ctx, r)
		return gpus, src, warn
	}

	gpus := make([]GPU, 0)
	seenPCI := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if !drmCardRe.MatchString(name) {
			continue
		}
		base := drmDir + "/" + name + "/device"
		vendorHex := readSysHex(r, base+"/vendor")
		deviceHex := readSysHex(r, base+"/device")
		if vendorHex == "" {
			continue
		}
		pci := pciFromDevicePath(r, base)
		if pci != "" && seenPCI[normalizePCI(pci)] {
			continue
		}
		if pci != "" {
			seenPCI[normalizePCI(pci)] = true
		}

		driver := driverFromSysfs(r, base)
		g := GPU{
			Vendor:           vendorFromPCI(vendorHex),
			PCIBusID:         pci,
			DriverVersion:    driver,
			MemoryTotalBytes: readSysUint(r, base+"/mem_info_vram_total"),
			MemoryUsedBytes:  readSysUint(r, base+"/mem_info_vram_used"),
			Source:           "sysfs",
		}
		if used := readSysUint(r, base+"/mem_info_vram_used"); used > 0 && g.MemoryTotalBytes > 0 {
			pct := int((used * 100) / g.MemoryTotalBytes)
			g.UtilizationMemory = &pct
		}
		g.Name = gpuNameFromSysfs(r, base, vendorHex, deviceHex, driver)
		if skipDummyAdapter(g.Name, vendorHex) {
			continue
		}
		g.ID = linuxID(g)
		gpus = append(gpus, g)
	}

	sources := []string{"sysfs"}
	warnings := []string{}
	if lspciGPUs, src, warn := detectLSPCI(ctx, r); len(lspciGPUs) > 0 || len(warn) > 0 {
		gpus = append(gpus, lspciGPUs...)
		sources = appendUnique(sources, src...)
		warnings = append(warnings, warn...)
	}
	return gpus, sources, warnings
}

func linuxID(g GPU) string {
	if g.PCIBusID != "" {
		return strings.ToLower(string(g.Vendor)) + "-" + normalizePCI(g.PCIBusID)
	}
	return strings.ToLower(string(g.Vendor)) + "-" + strings.ToLower(g.Name)
}

func readSysHex(r Runner, p string) string {
	b, err := r.ReadFile(p)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	s = strings.TrimPrefix(strings.ToLower(s), "0x")
	return s
}

func readSysUint(r Runner, p string) uint64 {
	b, err := r.ReadFile(p)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	return n
}

func driverFromSysfs(r Runner, deviceDir string) string {
	link, err := r.ReadLink(deviceDir + "/driver")
	if err != nil {
		return ""
	}
	return path.Base(link)
}

func pciFromDevicePath(r Runner, deviceDir string) string {
	link, err := r.ReadLink(deviceDir)
	if err != nil {
		return ""
	}
	base := path.Base(link)
	if strings.Contains(base, ":") {
		return base
	}
	return ""
}

func gpuNameFromSysfs(r Runner, deviceDir, vendor, device, driver string) string {
	if b, err := r.ReadFile(deviceDir + "/label"); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	vendorName := string(vendorFromPCI(vendor))
	if vendorName == "other" {
		vendorName = "PCI"
	}
	if device != "" {
		return fmt.Sprintf("%s %s (%s:%s)", strings.ToUpper(vendorName[:1])+vendorName[1:], driver, vendor, device)
	}
	return fmt.Sprintf("%s device", vendorName)
}

func detectLSPCI(ctx context.Context, r Runner) ([]GPU, []string, []string) {
	bin := firstExisting(r, "lspci")
	if bin == "" {
		return nil, nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	stdout, stderr, err := r.Run(ctx, bin, "-nn", "-D")
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = err.Error()
		}
		return nil, []string{"lspci"}, []string{"lspci: " + msg}
	}
	return parseLSPCI(stdout), []string{"lspci"}, nil
}

var lspciRe = regexp.MustCompile(`(?i)^(\S+)\s+(VGA compatible controller|3D controller|Display controller)\s+\[([0-9a-f]{4})\]:\s+(.+?)\s+\[([0-9a-f]{4}):([0-9a-f]{4})\]`)

func parseLSPCI(raw string) []GPU {
	gpus := make([]GPU, 0)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		m := lspciRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := strings.TrimSpace(m[4])
		vendorID := m[5]
		if skipDummyAdapter(name, vendorID) {
			continue
		}
		g := GPU{
			Vendor:   vendorFromPCI(vendorID),
			Name:     name,
			PCIBusID: m[1],
			Source:   "lspci",
		}
		g.ID = linuxID(g)
		gpus = append(gpus, g)
	}
	return gpus
}
