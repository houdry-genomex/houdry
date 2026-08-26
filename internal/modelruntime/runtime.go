package modelruntime

import (
	"context"
	"fmt"
	"time"
)

// Lifecycle states for a model on a node.
const (
	StateNotPresent  = "NOT_PRESENT"
	StateDownloading = "DOWNLOADING"
	StateAvailable   = "AVAILABLE" // on disk, not necessarily in VRAM
	StateLoaded      = "LOADED"    // resident / ready for low-latency infer
	StateUnloaded    = "UNLOADED"  // was loaded, now freed (still on disk → treat as AVAILABLE)
)

// Model is a runtime-normalized inventory entry reported to the control plane.
// Identity is name + tag + runtime — never an Ollama-only opaque string.
type Model struct {
	Name          string `json:"name"`
	Tag           string `json:"tag,omitempty"`
	Runtime       string `json:"runtime"`
	State         string `json:"state"`
	SizeBytes     uint64 `json:"size_bytes,omitempty"`
	Digest        string `json:"digest,omitempty"`
	Family        string `json:"family,omitempty"`
	ParameterSize string `json:"parameter_size,omitempty"`
	VRAMBytes     uint64 `json:"vram_bytes,omitempty"`
}

// Identity returns the scheduler key for this inventory row.
func (m Model) Identity() Identity {
	return Identity{Name: m.Name, Tag: m.Tag, Runtime: m.Runtime}
}

// Ref is name or name:tag for backend APIs.
func (m Model) Ref() string {
	return m.Identity().Ref()
}

// InferRequest is a framework-agnostic generation request.
type InferRequest struct {
	Name    string       `json:"name"`
	Tag     string       `json:"tag,omitempty"`
	Prompt  string       `json:"prompt"`
	Options InferOptions `json:"options,omitempty"`
}

// InferOptions are portable hints; adapters map what they support.
type InferOptions struct {
	// MaxTokens caps generated tokens (Ollama: num_predict).
	MaxTokens int `json:"max_tokens,omitempty"`
	// KeepAlive keeps the model resident after inference (e.g. "30m", "-1").
	KeepAlive   string   `json:"keep_alive,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
}

// Ref returns the backend model ref.
func (r InferRequest) Ref() string {
	return Identity{Name: r.Name, Tag: r.Tag}.Ref()
}

// InferResult is a framework-agnostic generation response.
type InferResult struct {
	Runtime      string         `json:"runtime"`
	Model        string         `json:"model"` // ref form for display
	Name         string         `json:"name,omitempty"`
	Tag          string         `json:"tag,omitempty"`
	Text         string         `json:"text"`
	Duration     time.Duration  `json:"-"`
	DurationMS   int64          `json:"duration_ms"`
	LoadMS       int64          `json:"load_ms,omitempty"`
	PromptMS     int64          `json:"prompt_ms,omitempty"`
	GenerateMS   int64          `json:"generate_ms,omitempty"`
	PromptTokens int            `json:"prompt_tokens,omitempty"`
	OutputTokens int            `json:"output_tokens,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
}

// Runtime is a pluggable model backend (Ollama, vLLM, llama.cpp, …).
// Jobs and the scheduler depend only on this interface.
type Runtime interface {
	Name() string
	Available(ctx context.Context) bool
	ListModels(ctx context.Context) ([]Model, error)
	// EnsureModel makes the model AVAILABLE (download if needed). Idempotent.
	// name/tag are runtime-agnostic; adapters map them to vendor refs.
	EnsureModel(ctx context.Context, name, tag string) (Model, error)
	Infer(ctx context.Context, req InferRequest) (InferResult, error)
}

// DefaultRuntimes returns built-in adapters. Unavailable backends stay listed
// so discovery can report "not installed" without coupling Houdry to one vendor.
func DefaultRuntimes() []Runtime {
	return []Runtime{
		NewOllama(""),
		VLLM{},
		LlamaCPP{},
	}
}

// SelectAvailable returns runtimes that report Available.
func SelectAvailable(ctx context.Context, all []Runtime) []Runtime {
	out := make([]Runtime, 0, len(all))
	for _, r := range all {
		if r.Available(ctx) {
			out = append(out, r)
		}
	}
	return out
}

// Discover lists models from every available runtime.
func Discover(ctx context.Context, runtimes []Runtime) (runtimeNames []string, models []Model, err error) {
	available := SelectAvailable(ctx, runtimes)
	for _, r := range available {
		runtimeNames = append(runtimeNames, r.Name())
		ms, listErr := r.ListModels(ctx)
		if listErr != nil {
			continue
		}
		models = append(models, ms...)
	}
	return runtimeNames, models, nil
}

// Find selects a runtime that can serve the identity (prefer one that already has it).
func Find(ctx context.Context, runtimes []Runtime, id Identity) (Runtime, Model, error) {
	available := SelectAvailable(ctx, runtimes)
	if id.Runtime != "" {
		filtered := make([]Runtime, 0, 1)
		for _, r := range available {
			if r.Name() == id.Runtime {
				filtered = append(filtered, r)
			}
		}
		available = filtered
	}
	if len(available) == 0 {
		if id.Runtime != "" {
			return nil, Model{}, fmt.Errorf("model runtime %q is not available on this node", id.Runtime)
		}
		return nil, Model{}, fmt.Errorf("no model runtime available")
	}

	var fallback Runtime
	for _, r := range available {
		if fallback == nil {
			fallback = r
		}
		ms, err := r.ListModels(ctx)
		if err != nil {
			continue
		}
		want := id
		want.Runtime = r.Name()
		if m, ok := Match(ms, want); ok {
			return r, m, nil
		}
	}
	return fallback, Model{
		Name:    id.Name,
		Tag:     id.Tag,
		Runtime: fallback.Name(),
		State:   StateNotPresent,
	}, nil
}

// Match finds a model in inventory that satisfies want.
func Match(models []Model, want Identity) (Model, bool) {
	for _, m := range models {
		if want.Matches(m.Identity()) {
			return m, true
		}
	}
	return Model{}, false
}

// HasModel reports whether models contains an available/loaded entry for want.
func HasModel(models []Model, want Identity) bool {
	m, ok := Match(models, want)
	if !ok {
		return false
	}
	switch m.State {
	case StateAvailable, StateLoaded, StateUnloaded, "":
		return true
	default:
		return false
	}
}

// IsLoaded reports LOADED state for want.
func IsLoaded(models []Model, want Identity) bool {
	m, ok := Match(models, want)
	return ok && m.State == StateLoaded
}
