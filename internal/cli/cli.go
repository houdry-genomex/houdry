package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"houdry/internal/config"
	"houdry/internal/gpu"
	"houdry/internal/server"
	"houdry/internal/version"
)

func Run(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return errors.New("missing command")
	}
	switch args[0] {
	case "gpu":
		return runGPU(args[1:])
	case "serve":
		return runServe(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("houdry %s\n", version.Version)
		return nil
	case "help", "--help", "-h":
		printUsage(os.Stdout)
		return nil
	default:
		printUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, `Houdry — private GPU fabric (phase 1)

Usage:
  houdry gpu detect [--json]
  houdry gpu join [--server URL] [--token TOKEN] [--json]
  houdry gpu list [--server URL] [--token TOKEN] [--json]
  houdry serve [--listen ADDR] [--data DIR] [--binaries DIR] [--token TOKEN]
  houdry version

After installing from a Houdry server, detect and join:

  houdry gpu detect
  houdry gpu join

The same gpu commands work on Linux, macOS, and Windows.
`)
}

func runGPU(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: houdry gpu detect|join|list")
	}
	switch args[0] {
	case "detect":
		return runDetect(args[1:])
	case "join":
		return runJoin(args[1:])
	case "list":
		return runList(args[1:])
	default:
		return fmt.Errorf("unknown gpu command %q", args[0])
	}
}

func runDetect(args []string) error {
	fs := flag.NewFlagSet("gpu detect", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print machine-readable JSON")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.EnsureNodeID(); err != nil {
		return err
	}
	inv := gpu.Detect(context.Background(), cfg.NodeID)
	if *asJSON {
		return encodeJSON(os.Stdout, inv)
	}
	printInventory(os.Stdout, inv)
	return nil
}

func runJoin(args []string) error {
	fs := flag.NewFlagSet("gpu join", flag.ContinueOnError)
	serverURL := fs.String("server", "", "Houdry server URL")
	token := fs.String("token", "", "join token")
	asJSON := fs.Bool("json", false, "print machine-readable JSON")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadJoinConfig(*serverURL, *token)
	if err != nil {
		return err
	}
	inv := gpu.Detect(context.Background(), cfg.NodeID)
	result, err := server.Join(context.Background(), cfg.Server, cfg.Token, inv)
	if err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(os.Stdout, map[string]any{
			"status":    "joined",
			"server":    cfg.Server,
			"node_id":   inv.NodeID,
			"gpu_count": len(inv.GPUs),
			"warnings":  inv.Warnings,
			"response":  result,
		})
	}
	fmt.Printf("Joined %d GPU(s) to %s\n", len(inv.GPUs), cfg.Server)
	fmt.Printf("Node ID: %s\n", inv.NodeID)
	fmt.Printf("Host:    %s (%s/%s)\n", inv.Host.Hostname, inv.Host.OS, inv.Host.Arch)
	fmt.Printf("View:    %s\n", strings.TrimRight(cfg.Server, "/")+"/")
	if len(inv.Warnings) > 0 {
		fmt.Fprintln(os.Stderr, "Warnings:")
		for _, w := range inv.Warnings {
			fmt.Fprintf(os.Stderr, "  - %s\n", w)
		}
	}
	if len(inv.GPUs) == 0 {
		fmt.Fprintln(os.Stderr, "Note: no GPUs were detected; the node is still registered.")
	}
	return nil
}

func runList(args []string) error {
	fs := flag.NewFlagSet("gpu list", flag.ContinueOnError)
	serverURL := fs.String("server", "", "Houdry server URL")
	token := fs.String("token", "", "join token")
	asJSON := fs.Bool("json", false, "print machine-readable JSON")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadJoinConfig(*serverURL, *token)
	if err != nil {
		return err
	}
	nodes, err := server.ListNodes(context.Background(), cfg.Server, cfg.Token)
	if err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(os.Stdout, nodes)
	}
	if len(nodes) == 0 {
		fmt.Println("No nodes have joined yet.")
		return nil
	}
	fmt.Printf("Joined nodes (%d) at %s\n\n", len(nodes), cfg.Server)
	for _, n := range nodes {
		fmt.Printf("%s  %s (%s/%s)  %d GPU(s)  last seen %s\n",
			shortID(n.NodeID), n.Host.Hostname, n.Host.OS, n.Host.Arch, len(n.GPUs), n.LastSeen.Local().Format(time.RFC3339))
		for _, g := range n.GPUs {
			fmt.Printf("    [%d] %s  %s  %s\n", g.Index, g.Vendor, g.Name, formatBytes(g.MemoryTotalBytes))
		}
	}
	return nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", "0.0.0.0:8080", "listen address")
	dataDir := fs.String("data", "", "directory for joined-node state (default: ~/.houdry/server)")
	binaries := fs.String("binaries", "dist", "directory of cross-compiled houdry binaries")
	token := fs.String("token", "", "optional join token")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dataDir == "" {
		*dataDir = filepath.Join(config.Dir(), "server")
	}
	if env := os.Getenv("HOODRY_TOKEN"); *token == "" && env != "" {
		*token = env
	}
	fmt.Printf("Houdry server %s listening on http://%s\n", version.Version, *listen)
	fmt.Printf("Install (Linux/macOS): curl -fsSL http://<host>:%s/install.sh | sh\n", portOf(*listen))
	fmt.Printf("Install (Windows):     irm http://<host>:%s/install.ps1 | iex\n", portOf(*listen))
	return server.ListenAndServe(*listen, server.Options{
		DataDir:     *dataDir,
		BinariesDir: *binaries,
		Token:       *token,
		Version:     version.Version,
	})
}

func loadJoinConfig(serverURL, token string) (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if serverURL != "" {
		cfg.Server = strings.TrimRight(serverURL, "/")
	} else if v := os.Getenv("HOODRY_SERVER"); v != "" {
		cfg.Server = strings.TrimRight(v, "/")
	}
	if token != "" {
		cfg.Token = token
	} else if v := os.Getenv("HOODRY_TOKEN"); v != "" {
		cfg.Token = v
	}
	if cfg.Server == "" {
		return nil, errors.New("no server URL: pass --server, set HOODRY_SERVER, or run the install script from a Houdry server")
	}
	if err := cfg.EnsureNodeID(); err != nil {
		return nil, err
	}
	_ = cfg.Save()
	return cfg, nil
}

func printInventory(w io.Writer, inv gpu.Inventory) {
	fmt.Fprintf(w, "Host: %s (%s/%s)\n", inv.Host.Hostname, inv.Host.OS, inv.Host.Arch)
	if inv.Host.Kernel != "" {
		fmt.Fprintf(w, "Kernel: %s\n", inv.Host.Kernel)
	}
	fmt.Fprintf(w, "Node: %s\n", inv.NodeID)
	fmt.Fprintf(w, "GPUs: %d\n", len(inv.GPUs))
	if len(inv.Sources) > 0 {
		fmt.Fprintf(w, "Sources: %s\n", strings.Join(inv.Sources, ", "))
	}
	fmt.Fprintln(w)
	if len(inv.GPUs) == 0 {
		fmt.Fprintln(w, "No GPUs detected on this machine.")
	}
	for _, g := range inv.GPUs {
		fmt.Fprintf(w, "  [%d] %s\n", g.Index, g.Name)
		fmt.Fprintf(w, "      Vendor: %s\n", g.Vendor)
		if g.UUID != "" {
			fmt.Fprintf(w, "      UUID: %s\n", g.UUID)
		}
		if g.PCIBusID != "" {
			fmt.Fprintf(w, "      PCI: %s\n", g.PCIBusID)
		}
		if g.MemoryTotalBytes > 0 {
			if g.MemoryUsedBytes > 0 {
				fmt.Fprintf(w, "      Memory: %s / %s\n", formatBytes(g.MemoryUsedBytes), formatBytes(g.MemoryTotalBytes))
			} else {
				fmt.Fprintf(w, "      Memory: %s\n", formatBytes(g.MemoryTotalBytes))
			}
		}
		if g.UtilizationGPU != nil || g.UtilizationMemory != nil {
			fmt.Fprintf(w, "      Utilization: %s GPU, %s mem\n", formatPct(g.UtilizationGPU), formatPct(g.UtilizationMemory))
		}
		if g.TemperatureC != nil {
			fmt.Fprintf(w, "      Temp: %d°C\n", *g.TemperatureC)
		}
		if g.DriverVersion != "" {
			fmt.Fprintf(w, "      Driver: %s\n", g.DriverVersion)
		}
		if g.ComputeCapability != "" {
			fmt.Fprintf(w, "      Compute capability: %s\n", g.ComputeCapability)
		}
		if g.CUDAVersion != "" {
			fmt.Fprintf(w, "      CUDA: %s\n", g.CUDAVersion)
		}
		fmt.Fprintf(w, "      Source: %s\n", g.Source)
		fmt.Fprintln(w)
	}
	if len(inv.Warnings) > 0 {
		fmt.Fprintln(w, "Warnings:")
		for _, wmsg := range inv.Warnings {
			fmt.Fprintf(w, "  - %s\n", wmsg)
		}
	}
}

func formatBytes(n uint64) string {
	if n == 0 {
		return "unknown"
	}
	const (
		ki = 1024
		mi = 1024 * ki
		gi = 1024 * mi
	)
	switch {
	case n >= gi:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(gi))
	case n >= mi:
		return fmt.Sprintf("%.0f MiB", float64(n)/float64(mi))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func formatPct(v *int) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%d%%", *v)
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func portOf(listen string) string {
	if i := strings.LastIndex(listen, ":"); i >= 0 {
		return listen[i+1:]
	}
	return "8080"
}
