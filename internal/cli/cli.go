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

	"houdry/internal/agent"
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
	case "node":
		return runNode(args[1:])
	case "model":
		return runModel(args[1:])
	case "route":
		return runRoute(args[1:])
	case "job":
		return runJob(args[1:])
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
	fmt.Fprintf(w, `Houdry — private GPU fabric

Usage:
  houdry gpu detect|join|list …
  houdry node join|list|drain|leave …
  houdry model list [--server URL] [--json]
  houdry route --prompt TEXT [--execute] [--wait] [--runtime NAME]
  houdry job submit gpu.smoke|inference …
  houdry job list|get …
  houdry serve …
  houdry version

Common commands:

  houdry node join --server http://HOST:18080
  houdry node list --server http://HOST:18080
  houdry job submit gpu.smoke --server http://HOST:18080 --wait
  houdry job submit inference --model NAME --prompt TEXT --wait

Phase 5 routing (analyze → pick model+node → optional execute):

  houdry route --prompt "Say hello" --execute --wait
  houdry route --prompt "Refactor this Go function…" --execute --wait
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
		st := n.Status
		if st == "" {
			st = server.StatusJoined
		}
		fmt.Printf("%s  %s (%s/%s)  %s  %d GPU(s)  last seen %s\n",
			shortID(n.NodeID), n.Host.Hostname, n.Host.OS, n.Host.Arch, st, len(n.GPUs), n.LastSeen.Local().Format(time.RFC3339))
		for _, g := range n.GPUs {
			fmt.Printf("    [%d] %s  %s  %s\n", g.Index, g.Vendor, g.Name, formatBytes(g.MemoryTotalBytes))
		}
	}
	return nil
}

func runNode(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: houdry node join|list|drain|leave")
	}
	switch args[0] {
	case "join":
		return runNodeJoin(args[1:])
	case "list":
		return runNodeList(args[1:])
	case "drain":
		return runNodeDrain(args[1:])
	case "leave":
		return runNodeLeave(args[1:])
	default:
		return fmt.Errorf("unknown node command %q", args[0])
	}
}

func runNodeJoin(args []string) error {
	fs := flag.NewFlagSet("node join", flag.ContinueOnError)
	serverURL := fs.String("server", "", "Houdry server URL")
	token := fs.String("token", "", "join token")
	interval := fs.Duration("interval", 2*time.Second, "heartbeat / claim interval")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadJoinConfig(*serverURL, *token)
	if err != nil {
		return err
	}
	return agent.Run(context.Background(), agent.Options{
		ServerURL: cfg.Server,
		Token:     cfg.Token,
		NodeID:    cfg.NodeID,
		Interval:  *interval,
	})
}

func runNodeList(args []string) error {
	fs := flag.NewFlagSet("node list", flag.ContinueOnError)
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
	summary, nodes, err := server.GetCluster(context.Background(), cfg.Server, cfg.Token)
	if err != nil {
		// Fall back to /v1/nodes if older server.
		nodes, err = server.ListNodes(context.Background(), cfg.Server, cfg.Token)
		if err != nil {
			return err
		}
		summary = server.SummarizeCluster(nodes, 0)
	}
	if *asJSON {
		return encodeJSON(os.Stdout, map[string]any{"summary": summary, "nodes": nodes})
	}
	fmt.Println("HOUDRY CLUSTER")
	fmt.Println()
	fmt.Printf("Nodes: %d\n", summary.Nodes)
	fmt.Printf("GPUs: %d\n", summary.GPUs)
	fmt.Printf("Available VRAM: %s\n", formatBytes(summary.AvailableVRAM))
	if summary.QueuedJobs > 0 {
		fmt.Printf("Queued jobs: %d\n", summary.QueuedJobs)
	}
	fmt.Println()
	if len(nodes) == 0 {
		fmt.Println("No nodes registered.")
		return nil
	}
	fmt.Printf("%-12s %-16s %-10s %s\n", "NODE", "GPU", "VRAM", "STATUS")
	fmt.Println(strings.Repeat("-", 56))
	for _, n := range nodes {
		name := n.Host.Hostname
		if name == "" {
			name = shortID(n.NodeID)
		}
		gpuName := "—"
		vram := uint64(0)
		if len(n.Resources.Static.GPUs) > 0 {
			gpuName = n.Resources.Static.GPUs[0].Name
			vram = n.Resources.Static.GPUs[0].MemoryTotalBytes
		} else if len(n.GPUs) > 0 {
			gpuName = n.GPUs[0].Name
			vram = n.GPUs[0].MemoryTotalBytes
		}
		fmt.Printf("%-12s %-16s %-10s %s\n", truncate(name, 12), truncate(gpuName, 16), formatBytes(vram), n.Status)
	}
	return nil
}

func runNodeDrain(args []string) error {
	fs := flag.NewFlagSet("node drain", flag.ContinueOnError)
	serverURL := fs.String("server", "", "Houdry server URL")
	token := fs.String("token", "", "join token")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadJoinConfig(*serverURL, *token)
	if err != nil {
		return err
	}
	n, err := server.DrainNode(context.Background(), cfg.Server, cfg.Token, cfg.NodeID)
	if err != nil {
		return err
	}
	fmt.Printf("Node %s is now %s\n", n.NodeID, n.Status)
	return nil
}

func runNodeLeave(args []string) error {
	fs := flag.NewFlagSet("node leave", flag.ContinueOnError)
	serverURL := fs.String("server", "", "Houdry server URL")
	token := fs.String("token", "", "join token")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadJoinConfig(*serverURL, *token)
	if err != nil {
		return err
	}
	if _, err := server.DrainNode(context.Background(), cfg.Server, cfg.Token, cfg.NodeID); err != nil {
		return err
	}
	if err := server.LeaveNode(context.Background(), cfg.Server, cfg.Token, cfg.NodeID); err != nil {
		return err
	}
	fmt.Printf("Node %s left the cluster\n", cfg.NodeID)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func runJob(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: houdry job submit|list|get")
	}
	switch args[0] {
	case "submit":
		return runJobSubmit(args[1:])
	case "list":
		return runJobList(args[1:])
	case "get":
		return runJobGet(args[1:])
	default:
		return fmt.Errorf("unknown job command %q", args[0])
	}
}

func runJobSubmit(args []string) error {
	fs := flag.NewFlagSet("job submit", flag.ContinueOnError)
	serverURL := fs.String("server", "", "Houdry server URL")
	token := fs.String("token", "", "join token")
	nodeID := fs.String("node", "", "optional preferred node ID")
	minVRAMMB := fs.Uint64("min-vram-mb", 0, "minimum GPU VRAM in MiB")
	requireGPU := fs.Bool("gpu", true, "require a GPU")
	noGPU := fs.Bool("no-gpu", false, "do not require a GPU")
	model := fs.String("model", "", "model name (required for inference)")
	prompt := fs.String("prompt", "", "prompt text (required for inference)")
	runtimeName := fs.String("runtime", "", "preferred model runtime (e.g. ollama)")
	requirePresent := fs.Bool("require-model", false, "only schedule nodes that already have the model")
	wait := fs.Bool("wait", false, "wait for job completion")
	asJSON := fs.Bool("json", false, "print machine-readable JSON")
	fs.SetOutput(os.Stderr)

	jobType := server.JobTypeGPUSmoke
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		jobType = rest[0]
		rest = rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		jobType = fs.Arg(0)
	}
	cfg, err := loadJoinConfig(*serverURL, *token)
	if err != nil {
		return err
	}
	req := server.Requirements{
		GPURequired:         *requireGPU && !*noGPU,
		MinVRAMBytes:        *minVRAMMB * 1024 * 1024,
		Model:               *model,
		ModelRuntime:        *runtimeName,
		RequireModelPresent: *requirePresent,
	}
	if *minVRAMMB > 0 || *model != "" {
		req.GPURequired = true
	}

	var j server.Job
	if jobType == server.JobTypeInference {
		if *model == "" || *prompt == "" {
			return errors.New("usage: houdry job submit inference --model NAME --prompt TEXT [--wait]")
		}
		j, err = server.SubmitInference(context.Background(), cfg.Server, cfg.Token, *model, *prompt, *nodeID, req)
	} else {
		j, err = server.SubmitJobWithRequirements(context.Background(), cfg.Server, cfg.Token, jobType, *nodeID, req)
	}
	if err != nil {
		return err
	}
	if *wait {
		timeout := 2 * time.Minute
		if jobType == server.JobTypeInference {
			timeout = 30 * time.Minute // may include first-time model pull
		}
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			cur, err := server.GetJob(context.Background(), cfg.Server, cfg.Token, j.ID)
			if err != nil {
				return err
			}
			j = cur
			if j.Status == server.JobSucceeded || j.Status == server.JobFailed {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	if *asJSON {
		return encodeJSON(os.Stdout, j)
	}
	fmt.Printf("Job %s  type=%s  status=%s\n", j.ID, j.Type, j.Status)
	if j.Requirements.GPURequired || j.Requirements.MinVRAMBytes > 0 || j.Requirements.ModelIdentity().Name != "" {
		fmt.Printf("Requirements: gpu=%v", j.Requirements.GPURequired)
		if j.Requirements.MinVRAMBytes > 0 {
			fmt.Printf(" min_vram=%s", formatBytes(j.Requirements.MinVRAMBytes))
		}
		if id := j.Requirements.ModelIdentity(); id.Name != "" {
			fmt.Printf(" model=%s", id.Ref())
			if id.Runtime != "" {
				fmt.Printf(" runtime=%s", id.Runtime)
			}
		}
		fmt.Println()
	}
	if j.NodeID != "" {
		fmt.Printf("Node: %s\n", j.NodeID)
	}
	if j.Error != "" {
		fmt.Printf("Error: %s\n", j.Error)
	}
	if j.Result != nil {
		printInferenceTiming(j.Result)
		if text, ok := j.Result["text"].(string); ok && text != "" && !*asJSON {
			fmt.Println("Text:")
			fmt.Println(text)
		}
		fmt.Printf("Result: ")
		_ = encodeJSON(os.Stdout, j.Result)
	}
	if *wait && j.Status != server.JobSucceeded && j.Status != server.JobFailed {
		return errors.New("timed out waiting for job")
	}
	if *wait && j.Status == server.JobFailed {
		return fmt.Errorf("job failed: %s", j.Error)
	}
	return nil
}

func printInferenceTiming(result map[string]any) {
	load, _ := asInt64(result["load_ms"])
	prompt, _ := asInt64(result["prompt_ms"])
	gen, _ := asInt64(result["generate_ms"])
	total, _ := asInt64(result["duration_ms"])
	if load == 0 && prompt == 0 && gen == 0 && total == 0 {
		return
	}
	fmt.Printf("Timing: total=%dms  load=%dms  prompt=%dms  generate=%dms\n", total, load, prompt, gen)
	if load > 2000 {
		fmt.Println("Note: most of the wait was model load into VRAM (cold start). Retry while LOADED for a faster reply.")
	}
}

func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	default:
		return 0, false
	}
}

func runModel(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: houdry model list|catalog")
	}
	switch args[0] {
	case "list":
		return runModelList(args[1:])
	case "catalog":
		return runModelCatalog(args[1:])
	default:
		return fmt.Errorf("unknown model command %q", args[0])
	}
}

func runModelCatalog(args []string) error {
	fs := flag.NewFlagSet("model catalog", flag.ContinueOnError)
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
	models, err := server.GetCatalog(context.Background(), cfg.Server, cfg.Token)
	if err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(os.Stdout, models)
	}
	if len(models) == 0 {
		fmt.Println("Catalog is empty.")
		return nil
	}
	fmt.Printf("%-18s %-10s %-28s %s\n", "NAME", "TAG", "CAPABILITIES", "MAX_COMPLEXITY")
	fmt.Println(strings.Repeat("-", 72))
	for _, m := range models {
		name, _ := m["name"].(string)
		tag, _ := m["tag"].(string)
		if tag == "" {
			tag = "—"
		}
		maxC, _ := m["max_complexity"].(string)
		caps := ""
		if raw, ok := m["capabilities"].([]any); ok {
			parts := make([]string, 0, len(raw))
			for _, c := range raw {
				parts = append(parts, fmt.Sprint(c))
			}
			caps = strings.Join(parts, ",")
		}
		fmt.Printf("%-18s %-10s %-28s %s\n", truncate(name, 18), truncate(tag, 10), truncate(caps, 28), maxC)
	}
	return nil
}

func runRoute(args []string) error {
	fs := flag.NewFlagSet("route", flag.ContinueOnError)
	serverURL := fs.String("server", "", "Houdry server URL")
	token := fs.String("token", "", "join token")
	prompt := fs.String("prompt", "", "user request to analyze and route")
	runtimeName := fs.String("runtime", "", "preferred model runtime")
	requirePresent := fs.Bool("require-model", false, "only use models already on a node")
	execute := fs.Bool("execute", false, "submit inference after routing")
	wait := fs.Bool("wait", false, "wait for inference job (implies --execute)")
	asJSON := fs.Bool("json", false, "print machine-readable JSON")
	local := fs.Bool("local", false, "route against this machine's Ollama daemon (no fabric needed)")
	run := fs.Bool("run", false, "with --local: execute the selected model and print its answer")
	interactive := fs.Bool("interactive", false, "with --local: REPL test bench — type prompts, see routing decisions")
	web := fs.Bool("web", false, "serve the router test bench as a local web page")
	addr := fs.String("addr", "127.0.0.1:8090", "listen address for --web")
	ollamaURL := fs.String("ollama", defaultOllamaURL, "Ollama base URL for --local")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	// A bare positional prompt reads naturally: houdry route --local "hi".
	if strings.TrimSpace(*prompt) == "" && fs.NArg() > 0 {
		*prompt = strings.Join(fs.Args(), " ")
	}

	// Local mode: single-machine routing straight against Ollama — the test
	// bench for the router itself. No server, no join config.
	if *local || *interactive || *web {
		ctx := context.Background()
		if *web {
			return runRouteWeb(ctx, *ollamaURL, *addr)
		}
		if *interactive {
			return runRouteInteractive(ctx, *ollamaURL, *run)
		}
		if strings.TrimSpace(*prompt) == "" {
			return errors.New("usage: houdry route --local [--run] \"PROMPT\"  (or --interactive)")
		}
		return runLocalRoute(ctx, *ollamaURL, *prompt, *run, *asJSON)
	}

	if strings.TrimSpace(*prompt) == "" {
		return errors.New("usage: houdry route --prompt TEXT [--execute] [--wait]")
	}
	if *wait {
		*execute = true
	}
	cfg, err := loadJoinConfig(*serverURL, *token)
	if err != nil {
		return err
	}
	out, err := server.Route(context.Background(), cfg.Server, cfg.Token, *prompt, *runtimeName, *requirePresent, *execute)
	if err != nil {
		return err
	}
	if *asJSON && !*wait {
		return encodeJSON(os.Stdout, out)
	}

	dec, _ := out["decision"].(map[string]any)
	if dec != nil {
		if profile, ok := dec["profile"].(map[string]any); ok {
			fmt.Printf("Profile: modality=%v complexity=%v caps=%v\n",
				profile["modality"], profile["complexity"], profile["capabilities"])
		}
		if msg, _ := dec["message"].(string); msg != "" {
			fmt.Printf("Route: %s\n", msg)
		}
		if sel, ok := dec["selected"].(map[string]any); ok {
			if entry, ok := sel["entry"].(map[string]any); ok {
				fmt.Printf("Model: %v", entry["name"])
				if tag, _ := entry["tag"].(string); tag != "" {
					fmt.Printf(":%s", tag)
				}
				fmt.Println()
			}
			if node, _ := sel["node_id"].(string); node != "" {
				fmt.Printf("Node: %s\n", node)
			}
			if reasons, ok := sel["reasons"].([]any); ok && len(reasons) > 0 {
				fmt.Printf("Reasons: %v\n", reasons)
			}
		}
	}

	jobRaw, hasJob := out["job"]
	if !hasJob || !*execute {
		if *asJSON {
			return encodeJSON(os.Stdout, out)
		}
		return nil
	}

	// Wait / print job
	jb, _ := json.Marshal(jobRaw)
	var j server.Job
	if err := json.Unmarshal(jb, &j); err != nil {
		return encodeJSON(os.Stdout, out)
	}
	if *wait {
		timeout := 30 * time.Minute
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			cur, err := server.GetJob(context.Background(), cfg.Server, cfg.Token, j.ID)
			if err != nil {
				return err
			}
			j = cur
			if j.Status == server.JobSucceeded || j.Status == server.JobFailed {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	if *asJSON {
		return encodeJSON(os.Stdout, map[string]any{"decision": dec, "job": j})
	}
	fmt.Printf("Job %s  status=%s\n", j.ID, j.Status)
	if j.Error != "" {
		fmt.Printf("Error: %s\n", j.Error)
	}
	if j.Result != nil {
		printInferenceTiming(j.Result)
		if text, ok := j.Result["text"].(string); ok && text != "" {
			fmt.Println("Text:")
			fmt.Println(text)
		}
		fmt.Printf("Result: ")
		_ = encodeJSON(os.Stdout, j.Result)
	}
	if *wait && j.Status != server.JobSucceeded && j.Status != server.JobFailed {
		return errors.New("timed out waiting for job")
	}
	if *wait && j.Status == server.JobFailed {
		return fmt.Errorf("job failed: %s", j.Error)
	}
	return nil
}

func runModelList(args []string) error {
	fs := flag.NewFlagSet("model list", flag.ContinueOnError)
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
	type row struct {
		NodeID   string `json:"node_id"`
		Hostname string `json:"hostname"`
		Runtime  string `json:"runtime"`
		Name     string `json:"name"`
		Tag      string `json:"tag,omitempty"`
		State    string `json:"state"`
		Size     uint64 `json:"size_bytes,omitempty"`
	}
	var rows []row
	for _, n := range nodes {
		for _, m := range n.Models {
			rows = append(rows, row{
				NodeID:   n.NodeID,
				Hostname: n.Host.Hostname,
				Runtime:  m.Runtime,
				Name:     m.Name,
				Tag:      m.Tag,
				State:    m.State,
				Size:     m.SizeBytes,
			})
		}
		if len(n.Models) == 0 && len(n.ModelRuntimes) > 0 {
			for _, rt := range n.ModelRuntimes {
				rows = append(rows, row{
					NodeID:   n.NodeID,
					Hostname: n.Host.Hostname,
					Runtime:  rt,
					Name:     "(none)",
					State:    "—",
				})
			}
		}
	}
	if *asJSON {
		return encodeJSON(os.Stdout, map[string]any{"models": rows, "nodes": nodes})
	}
	if len(rows) == 0 {
		fmt.Println("No model runtimes/models reported. Install a model runtime (e.g. Ollama) and restart the node agent.")
		return nil
	}
	fmt.Printf("%-14s %-12s %-18s %-12s %s\n", "NODE", "RUNTIME", "MODEL", "TAG", "STATE")
	fmt.Println(strings.Repeat("-", 72))
	for _, r := range rows {
		host := r.Hostname
		if host == "" {
			host = shortID(r.NodeID)
		}
		tag := r.Tag
		if tag == "" {
			tag = "—"
		}
		fmt.Printf("%-14s %-12s %-18s %-12s %s\n", truncate(host, 14), r.Runtime, truncate(r.Name, 18), truncate(tag, 12), r.State)
	}
	return nil
}

func runJobList(args []string) error {
	fs := flag.NewFlagSet("job list", flag.ContinueOnError)
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
	jobs, err := server.ListJobs(context.Background(), cfg.Server, cfg.Token)
	if err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(os.Stdout, jobs)
	}
	if len(jobs) == 0 {
		fmt.Println("No jobs yet.")
		return nil
	}
	for _, j := range jobs {
		fmt.Printf("%s  %-10s  %-10s  node=%s\n", j.ID, j.Type, j.Status, j.NodeID)
	}
	return nil
}

func runJobGet(args []string) error {
	fs := flag.NewFlagSet("job get", flag.ContinueOnError)
	serverURL := fs.String("server", "", "Houdry server URL")
	token := fs.String("token", "", "join token")
	asJSON := fs.Bool("json", false, "print machine-readable JSON")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: houdry job get JOB_ID")
	}
	cfg, err := loadJoinConfig(*serverURL, *token)
	if err != nil {
		return err
	}
	j, err := server.GetJob(context.Background(), cfg.Server, cfg.Token, fs.Arg(0))
	if err != nil {
		return err
	}
	if *asJSON {
		return encodeJSON(os.Stdout, j)
	}
	return encodeJSON(os.Stdout, j)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := fs.String("listen", "0.0.0.0:8080", "listen address")
	dataDir := fs.String("data", "", "directory for joined-node state (default: ~/.houdry/server)")
	binaries := fs.String("binaries", "dist", "directory of cross-compiled houdry binaries")
	token := fs.String("token", "", "optional join token")
	noOpenAI := fs.Bool("no-openai-compat", false, "disable OpenAI-compatible /v1/chat/completions")
	openaiWait := fs.Duration("openai-wait", 10*time.Minute, "max wait for chat completion inference jobs")
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
	if !*noOpenAI {
		fmt.Printf("OpenAI-compatible API: POST http://<host>:%s/v1/chat/completions  (model=auto uses Houdry router)\n", portOf(*listen))
	}
	fmt.Printf("Install (Linux/macOS): curl -fsSL http://<host>:%s/install.sh | sh\n", portOf(*listen))
	fmt.Printf("Install (Windows):     irm http://<host>:%s/install.ps1 | iex\n", portOf(*listen))
	return server.ListenAndServe(*listen, server.Options{
		DataDir:             *dataDir,
		BinariesDir:         *binaries,
		Token:               *token,
		Version:             version.Version,
		DisableOpenAICompat: *noOpenAI,
		OpenAIWait:          *openaiWait,
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
