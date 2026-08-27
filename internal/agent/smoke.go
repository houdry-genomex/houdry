package agent

import (
	"context"
	"encoding/json"
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
	messages := payloadMessages(job.Payload["messages"])
	tools := payloadTools(job.Payload["tools"])
	toolChoice := payloadToolChoice(job.Payload["tool_choice"])

	if id.Name == "" {
		return nil, fmt.Errorf("inference requires model identity")
	}
	if prompt == "" && len(messages) == 0 {
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

	opts := inferOptionsFor(job, prompt, len(tools) > 0)
	res, err := rt.Infer(ctx, modelruntime.InferRequest{
		Name:       id.Name,
		Tag:        id.Tag,
		Prompt:     prompt,
		Messages:   messages,
		Tools:      tools,
		ToolChoice: toolChoice,
		Options:    opts,
	})
	if err != nil {
		return nil, err
	}

	out := map[string]any{
		"workload":      "inference",
		"runtime":       res.Runtime,
		"model":         res.Model,
		"model_name":    firstNonEmpty(res.Name, id.Name),
		"model_tag":     firstNonEmpty(res.Tag, id.Tag),
		"model_state":   ensured.State,
		"text":          res.Text,
		"finish_reason": firstNonEmpty(res.FinishReason, "stop"),
		"duration_ms":   res.DurationMS,
		"load_ms":       res.LoadMS,
		"prompt_ms":     res.PromptMS,
		"generate_ms":   res.GenerateMS,
		"prompt_tokens": res.PromptTokens,
		"output_tokens": res.OutputTokens,
		"details":       res.Details,
	}
	if len(res.ToolCalls) > 0 {
		out["tool_calls"] = res.ToolCalls
		out["finish_reason"] = "tool_calls"
	}
	return out, nil
}

func payloadMessages(v any) []modelruntime.ChatMessage {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var msgs []modelruntime.ChatMessage
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return nil
	}
	return msgs
}

func payloadTools(v any) []modelruntime.Tool {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var tools []modelruntime.Tool
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil
	}
	return tools
}

func payloadToolChoice(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return raw
}

// inferOptionsFor keeps simple replies short and models warm in VRAM.
func inferOptionsFor(job server.Job, prompt string, hasTools bool) modelruntime.InferOptions {
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
	if hasTools && opts.MaxTokens < 512 {
		opts.MaxTokens = 512
	}
	if v, ok := job.Payload["max_tokens"].(float64); ok && v > 0 {
		opts.MaxTokens = int(v)
	}
	if v, ok := job.Payload["temperature"].(float64); ok {
		t := v
		opts.Temperature = &t
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
