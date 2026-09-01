package agent

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"houdry/internal/gpu"
	"houdry/internal/gpuruntime"
	"houdry/internal/host"
	"houdry/internal/modelruntime"
	"houdry/internal/server"
	"houdry/internal/version"
)

type Options struct {
	ServerURL string
	Token     string
	NodeID    string
	Interval  time.Duration
}

// Run registers this machine as a node agent, heartbeats, and claims jobs.
// Local and remote agents use the same HTTP APIs. Nodes never learn about
// peer nodes — only the control plane URL.
func Run(ctx context.Context, opts Options) error {
	if opts.ServerURL == "" {
		return fmt.Errorf("server URL is required")
	}
	if opts.NodeID == "" {
		return fmt.Errorf("node ID is required")
	}
	if opts.Interval <= 0 {
		opts.Interval = 2 * time.Second
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	req := buildJoinRequest(ctx, opts.NodeID, server.StatusReady, "")
	n, err := server.JoinAgent(ctx, opts.ServerURL, opts.Token, req)
	if err != nil {
		return fmt.Errorf("join: %w", err)
	}
	logf("Registered as compute with control plane %s", trimSlash(opts.ServerURL))
	logf("Node %s  host %s (%s/%s)  %d GPU(s)", n.NodeID, n.Host.Hostname, n.Host.OS, n.Host.Arch, len(n.GPUs))
	for _, g := range n.GPUs {
		logf("  GPU [%d] %s  %s", g.Index, g.Name, formatMem(g.MemoryTotalBytes))
	}
	if len(n.Models) > 0 {
		logf("Models on this machine:")
		for _, m := range n.Models {
			tag := m.Tag
			if tag == "" {
				tag = "-"
			}
			logf("  %s:%s  %s  %s", m.Name, tag, m.Runtime, m.State)
		}
	}
	logf("End users (Houdry Agent) talk to the control plane, not this GPU.")
	logf("Waiting for work. Leave this window open. Ctrl+C to drain and leave.")

	ticker := time.NewTicker(opts.Interval)
	defer ticker.Stop()

	var mu sync.Mutex
	busyJob := ""
	draining := false
	removed := false

	tryClaim := func() bool {
		mu.Lock()
		current := busyJob
		isDraining := draining
		isRemoved := removed
		mu.Unlock()
		if current != "" || isDraining || isRemoved {
			return true
		}
		job, ok, err := server.ClaimJob(ctx, opts.ServerURL, opts.Token, opts.NodeID)
		if err != nil {
			if isNotRegistered(err) {
				mu.Lock()
				removed = true
				mu.Unlock()
				logf("This GPU was removed from the control plane; stopping.")
				return false
			}
			fmt.Fprintf(os.Stderr, "claim: %v\n", err)
			return true
		}
		if !ok {
			return true
		}
		mu.Lock()
		busyJob = job.ID
		mu.Unlock()
		logf("Work received  %s", describeJob(job))
		go func(j server.Job) {
			result, execErr := Execute(ctx, j)
			ok := execErr == nil
			errMsg := ""
			if execErr != nil {
				errMsg = execErr.Error()
				logf("Work failed    %s  %v", j.ID, execErr)
			} else {
				logf("Work finished  %s  %s", j.ID, resultSummary(j, result))
			}
			if _, err := server.ReportJobResult(context.Background(), opts.ServerURL, opts.Token, j.ID, opts.NodeID, ok, result, errMsg); err != nil {
				fmt.Fprintf(os.Stderr, "report job %s: %v\n", j.ID, err)
			}
			mu.Lock()
			busyJob = ""
			mu.Unlock()
		}(job)
		return true
	}

	heartbeat := func() bool {
		mu.Lock()
		status := server.StatusReady
		jobID := ""
		if draining {
			status = server.StatusDraining
		}
		if busyJob != "" {
			jobID = busyJob
			if !draining {
				status = server.StatusBusy
			}
		}
		mu.Unlock()

		hb := buildJoinRequest(ctx, opts.NodeID, status, jobID)
		out, err := server.Heartbeat(ctx, opts.ServerURL, opts.Token, hb)
		if err != nil {
			if isNotRegistered(err) {
				mu.Lock()
				removed = true
				mu.Unlock()
				logf("This GPU was removed from the control plane; stopping.")
				return false
			}
			fmt.Fprintf(os.Stderr, "heartbeat: %v\n", err)
			return true
		}
		// Honor remote drain (e.g. houdry node drain from another terminal).
		if out.Status == server.StatusDraining {
			mu.Lock()
			draining = true
			mu.Unlock()
		}
		return true
	}

	if !tryClaim() || !heartbeat() {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			mu.Lock()
			alreadyGone := removed
			mu.Unlock()
			if alreadyGone {
				logf("Stopped.")
				return nil
			}
			logf("Draining (no new work). Waiting for the current job to finish.")
			mu.Lock()
			draining = true
			mu.Unlock()
			if _, err := server.DrainNode(context.Background(), opts.ServerURL, opts.Token, opts.NodeID); err != nil {
				if !isNotRegistered(err) {
					fmt.Fprintf(os.Stderr, "drain: %v\n", err)
				}
			}
			deadline := time.Now().Add(2 * time.Minute)
			for time.Now().Before(deadline) {
				mu.Lock()
				idle := busyJob == ""
				mu.Unlock()
				if idle {
					break
				}
				if !heartbeat() {
					return nil
				}
				time.Sleep(opts.Interval)
			}
			if err := server.LeaveNode(context.Background(), opts.ServerURL, opts.Token, opts.NodeID); err != nil {
				if !isNotRegistered(err) {
					fmt.Fprintf(os.Stderr, "leave: %v\n", err)
				}
			} else {
				fmt.Println("Left the control plane.")
			}
			return nil
		case <-ticker.C:
			if !tryClaim() || !heartbeat() {
				return nil
			}
		}
	}
}

func isNotRegistered(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not registered") || strings.Contains(msg, "404")
}

func buildJoinRequest(ctx context.Context, nodeID, status, jobID string) server.JoinRequest {
	inv := gpu.Detect(ctx, nodeID)
	gpuRuntimes := availableGPURuntimeNames(ctx)
	modelRuntimes, models, _ := modelruntime.Discover(ctx, modelruntime.DefaultRuntimes())
	return server.JoinRequest{
		Inventory:     inv,
		AgentVersion:  version.Version,
		Status:        status,
		CurrentJobID:  jobID,
		HostResources: host.Collect(),
		Runtimes:      gpuRuntimes,
		ModelRuntimes: modelRuntimes,
		Models:        models,
	}
}

func availableGPURuntimeNames(ctx context.Context) []string {
	var names []string
	for _, r := range gpuruntime.SelectAvailable(ctx, gpuruntime.DefaultRuntimes()) {
		names = append(names, r.Name())
	}
	return names
}

func availableRuntimeNames(ctx context.Context) []string {
	return availableGPURuntimeNames(ctx)
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func logf(format string, args ...any) {
	ts := time.Now().Format("15:04:05")
	fmt.Printf("%s  "+format+"\n", append([]any{ts}, args...)...)
}

func describeJob(j server.Job) string {
	parts := []string{j.ID, j.Type}
	if id := j.Requirements.ModelIdentity(); id.Name != "" {
		parts = append(parts, "model="+id.Ref())
	} else if m, _ := j.Payload["model"].(string); m != "" {
		parts = append(parts, "model="+m)
	}
	if p, _ := j.Payload["prompt"].(string); p != "" {
		parts = append(parts, "prompt="+shortPrompt(p))
	}
	return strings.Join(parts, "  ")
}

func resultSummary(_ server.Job, result map[string]any) string {
	if result == nil {
		return "ok"
	}
	ms := durationMS(result["duration_ms"])
	if text, _ := result["text"].(string); text != "" {
		if ms > 0 {
			return fmt.Sprintf("ok  %dms  %s", int(ms), shortPrompt(text))
		}
		return "ok  " + shortPrompt(text)
	}
	if ms > 0 {
		return fmt.Sprintf("ok  %dms", int(ms))
	}
	return "ok"
}

func shortPrompt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}

func durationMS(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	default:
		return 0
	}
}

func formatMem(n uint64) string {
	if n == 0 {
		return ""
	}
	const gi = 1024 * 1024 * 1024
	if n >= gi {
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(gi))
	}
	return fmt.Sprintf("%d MiB", n/(1024*1024))
}
