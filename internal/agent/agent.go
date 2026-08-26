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
	fmt.Printf("Node agent joined %s\n", opts.ServerURL)
	fmt.Printf("Node ID: %s\n", n.NodeID)
	fmt.Printf("Host:    %s (%s/%s)\n", n.Host.Hostname, n.Host.OS, n.Host.Arch)
	fmt.Printf("GPUs:    %d physical\n", len(n.GPUs))
	if len(n.Runtimes) > 0 {
		fmt.Printf("GPU runtimes: %v\n", n.Runtimes)
	}
	if len(n.ModelRuntimes) > 0 {
		fmt.Printf("Model runtimes: %v\n", n.ModelRuntimes)
	}
	if len(n.Models) > 0 {
		fmt.Printf("Models:  %d\n", len(n.Models))
		for _, m := range n.Models {
			tag := m.Tag
			if tag == "" {
				tag = "—"
			}
			fmt.Printf("  - %s tag=%s runtime=%s (%s)\n", m.Name, tag, m.Runtime, m.State)
		}
	}
	fmt.Printf("Status:  %s\n", n.Status)
	fmt.Printf("View:    %s/\n", trimSlash(opts.ServerURL))
	fmt.Println("Heartbeating and waiting for jobs (Ctrl+C drains then leaves)…")

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
				fmt.Println("Node was removed from the control plane; stopping agent.")
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
		fmt.Printf("Claimed job %s (%s)\n", job.ID, job.Type)
		go func(j server.Job) {
			result, execErr := Execute(ctx, j)
			ok := execErr == nil
			errMsg := ""
			if execErr != nil {
				errMsg = execErr.Error()
				fmt.Fprintf(os.Stderr, "job %s failed: %v\n", j.ID, execErr)
			} else {
				fmt.Printf("Job %s succeeded\n", j.ID)
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
				fmt.Println("Node was removed from the control plane; stopping agent.")
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
				fmt.Println("Node agent stopped.")
				return nil
			}
			fmt.Println("Draining node (no new jobs)…")
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
				fmt.Println("Node left the cluster.")
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
