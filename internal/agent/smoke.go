package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"houdry/internal/gpuruntime"
	"houdry/internal/modelruntime"
	"houdry/internal/server"
)

// Execute runs a claimed job on this node.
func Execute(ctx context.Context, job server.Job) (map[string]any, error) {
	switch job.Type {
	case server.JobTypeGPUSmoke:
		return runGPUSmoke(ctx)
	case server.JobTypeInference:
		return runInference(ctx, job)
	default:
		return nil, fmt.Errorf("unsupported job type %q", job.Type)
	}
}

func runGPUSmoke(ctx context.Context) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	return gpuruntime.SmokeAll(ctx, gpuruntime.DefaultRuntimes())
}

func runInference(ctx context.Context, job server.Job) (map[string]any, error) {
	id := job.Requirements.ModelIdentity()
	if id.Name == "" {
		if m, _ := job.Payload["model"].(string); m != "" {
			id = modelruntime.ParseRef(m)
			id.Runtime = job.Requirements.ModelRuntime
		}
	}
	prompt, _ := job.Payload["prompt"].(string)
	if id.Name == "" || prompt == "" {
		return nil, fmt.Errorf("inference requires model identity and prompt")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	rt, _, err := modelruntime.Find(ctx, modelruntime.DefaultRuntimes(), id)
	if err != nil {
		return nil, err
	}

	ensured, err := rt.EnsureModel(ctx, id.Name, id.Tag)
	if err != nil {
		return nil, fmt.Errorf("ensure model %q: %w", id.Ref(), err)
	}

	opts := inferOptionsFor(job, prompt)
	res, err := rt.Infer(ctx, modelruntime.InferRequest{
		Name:    id.Name,
		Tag:     id.Tag,
		Prompt:  prompt,
		Options: opts,
	})
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"workload":      "inference",
		"runtime":       res.Runtime,
		"model":         res.Model,
		"model_name":    firstNonEmpty(res.Name, id.Name),
		"model_tag":     firstNonEmpty(res.Tag, id.Tag),
		"model_state":   ensured.State,
		"text":          res.Text,
		"duration_ms":   res.DurationMS,
		"load_ms":       res.LoadMS,
		"prompt_ms":     res.PromptMS,
		"generate_ms":   res.GenerateMS,
		"prompt_tokens": res.PromptTokens,
		"output_tokens": res.OutputTokens,
		"details":       res.Details,
	}, nil
}

// inferOptionsFor keeps simple replies short and models warm in VRAM.
func inferOptionsFor(job server.Job, prompt string) modelruntime.InferOptions {
	opts := modelruntime.InferOptions{
		KeepAlive: "45m",
		MaxTokens: 256,
	}
	complexity := ""
	if route, ok := job.Payload["route"].(map[string]any); ok {
		if c, _ := route["complexity"].(string); c != "" {
			complexity = c
		}
	}
	switch strings.ToLower(complexity) {
	case "low":
		opts.MaxTokens = 64
	case "medium":
		opts.MaxTokens = 256
	case "high":
		opts.MaxTokens = 512
	default:
		// Heuristic when job was submitted without a route profile.
		if len(prompt) < 80 {
			opts.MaxTokens = 64
		} else if len(prompt) < 400 {
			opts.MaxTokens = 256
		} else {
			opts.MaxTokens = 512
		}
	}
	if v, ok := job.Payload["max_tokens"].(float64); ok && v > 0 {
		opts.MaxTokens = int(v)
	}
	return opts
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
