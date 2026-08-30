// Package routerchat is the integration seam between the routing engine and
// anything that wants routed answers (web bench, OpenAI-compat layer, agents).
//
// Layering:
//
//	routing     — pure decision-making: prompt → TaskProfile → ranked (model, node)
//	routerchat  — decision + execution + metrics, behind two small interfaces
//	transports  — HTTP/CLI/UI call routerchat and render; they never route
//
// The two interfaces are the extension points:
//
//   - NodeSource yields the cluster view to route across. LocalOllama yields
//     this machine; the fabric server can yield its full node registry.
//   - Executor runs an inference on a chosen node. LocalOllama executes
//     in-process; the fabric can submit a job to the owning node instead.
//
// Swap those two and everything above (chat UI, API shapes, metrics) is reused
// unchanged — that is the contract this package promises.
package routerchat

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"houdry/internal/modelruntime"
	"houdry/internal/routing"
)

// NodeSource yields the current routable cluster view.
type NodeSource interface {
	Nodes(ctx context.Context) ([]routing.NodeView, []routing.CatalogEntry, error)
}

// Executor runs an inference request on a specific node.
type Executor interface {
	Infer(ctx context.Context, nodeID string, req modelruntime.InferRequest) (modelruntime.InferResult, error)
}

// StreamingExecutor is optionally implemented by executors that can deliver
// text deltas as they generate. Without it, Answer falls back to one delta
// carrying the whole reply — callers never need to care which they got.
type StreamingExecutor interface {
	InferStream(ctx context.Context, nodeID string, req modelruntime.InferRequest, onDelta func(string)) (modelruntime.InferResult, error)
}

// StreamEvent is one observation during a streamed answer.
//
//	decision — routing finished; Decision is set
//	delta    — Delta carries newly generated text
//	retry    — the attempt on Model failed with Err; the next candidate starts
//	           (any streamed text so far must be discarded by the consumer)
//	done     — Response carries the final answer and metrics
type StreamEvent struct {
	Type     string            `json:"type"`
	Decision *routing.Decision `json:"decision,omitempty"`
	Delta    string            `json:"delta,omitempty"`
	Model    string            `json:"model,omitempty"`
	Err      string            `json:"error,omitempty"`
	Response *AnswerResponse   `json:"response,omitempty"`
}

// Turn is one prior conversation turn ("user" or "assistant").
type Turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// AnswerRequest asks for a routed answer to the latest prompt, with optional
// prior turns so follow-ups keep their context.
type AnswerRequest struct {
	Prompt  string `json:"prompt"`
	History []Turn `json:"history,omitempty"`
	// KeepAlive keeps the model warm after answering (runtime default if empty).
	KeepAlive string `json:"keep_alive,omitempty"`
	// MaxTokens caps generated tokens (0 = runtime default). Demo insurance:
	// a deep-think can be bounded instead of running unattended for minutes.
	MaxTokens int `json:"max_tokens,omitempty"`
	// Images are base64-encoded attachments; their presence routes the prompt
	// to a vision-capable model.
	Images []string `json:"images,omitempty"`
}

// Metrics is the benchmark block attached to every answer.
type Metrics struct {
	WallMS       int64   `json:"wall_ms"`
	LoadMS       int64   `json:"load_ms"`
	PromptMS     int64   `json:"prompt_ms"`
	GenerateMS   int64   `json:"generate_ms"`
	PromptTokens int     `json:"prompt_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TokensPerSec float64 `json:"tokens_per_sec"`
	WeightsBytes uint64  `json:"weights_bytes,omitempty"`
}

// AnswerResponse is a routed, executed, measured reply.
type AnswerResponse struct {
	Decision routing.Decision `json:"decision"`
	Model    string           `json:"model"`
	NodeID   string           `json:"node_id"`
	Answer   string           `json:"answer"`
	// Thought is a reasoning model's <think> preamble, separated so interfaces
	// can fold it away instead of dumping deliberation on the user.
	Thought string  `json:"thought,omitempty"`
	Metrics Metrics `json:"metrics"`
	// Attempts counts execution tries: 1 = first candidate answered; >1 means
	// automatic failover kicked in and a lower-ranked candidate delivered.
	Attempts int `json:"attempts"`
	// File is a generated artifact (e.g. a STEP model from the CAD pipeline).
	// The chat service itself never sets it; tool pipelines layered on top do.
	File *Artifact `json:"file,omitempty"`
}

// Artifact is a downloadable file produced while answering.
type Artifact struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	// PreviewURL points at a renderable mesh (STL) of the same model, when one
	// could be tessellated. The primary artifact is STEP, which a browser
	// cannot draw without a geometry kernel; clients that show a 3D preview
	// load this instead. Empty when no mesh was produced.
	PreviewURL string `json:"preview_url,omitempty"`
}

// Service routes prompts and executes them. Safe for concurrent use.
type Service struct {
	source NodeSource
	exec   Executor

	// AttemptTimeout bounds one candidate's inference; on expiry the next
	// ranked candidate is tried. Zero means no per-attempt deadline.
	AttemptTimeout time.Duration
	// MaxAttempts caps how many ranked candidates are tried before giving up.
	MaxAttempts int

	// The cluster view is cached briefly: chat turns arrive seconds apart and
	// a model inventory rarely changes between them.
	ttl     time.Duration
	mu      sync.Mutex
	nodes   []routing.NodeView
	catalog []routing.CatalogEntry
	fresh   time.Time
}

// New builds a Service over any NodeSource + Executor pair.
func New(source NodeSource, exec Executor) *Service {
	return &Service{
		source:         source,
		exec:           exec,
		ttl:            10 * time.Second,
		AttemptTimeout: 5 * time.Minute,
		MaxAttempts:    3,
	}
}

// Snapshot returns the current catalog and nodes (refreshing if stale).
func (s *Service) Snapshot(ctx context.Context) ([]routing.CatalogEntry, []routing.NodeView, error) {
	if err := s.refresh(ctx, false); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalog, s.nodes, nil
}

// Decide routes a prompt without executing it.
func (s *Service) Decide(ctx context.Context, prompt string) (routing.Decision, error) {
	return s.decide(ctx, prompt, false)
}

func (s *Service) decide(ctx context.Context, prompt string, imageAttached bool) (routing.Decision, error) {
	if err := s.refresh(ctx, false); err != nil {
		return routing.Decision{}, err
	}
	s.mu.Lock()
	catalog, nodes := s.catalog, s.nodes
	s.mu.Unlock()
	return routing.Route(routing.RouteRequest{
		Prompt:         prompt,
		Catalog:        catalog,
		Nodes:          nodes,
		PreferLoaded:   true,
		RequirePresent: true,
		ImageAttached:  imageAttached,
	}), nil
}

// Answer routes the prompt, executes it (with automatic failover), and
// returns the reply with metrics. It is AnswerStream with the deltas ignored.
func (s *Service) Answer(ctx context.Context, req AnswerRequest) (AnswerResponse, error) {
	var out AnswerResponse
	err := s.AnswerStream(ctx, req, func(ev StreamEvent) {
		if ev.Type == "done" && ev.Response != nil {
			out = *ev.Response
		}
		if ev.Type == "decision" && ev.Decision != nil {
			out.Decision = *ev.Decision
		}
	})
	return out, err
}

// AnswerStream routes the prompt and executes it, emitting typed events as it
// goes: the routing decision first, then text deltas, then done with metrics.
// If a candidate fails or times out, a retry event fires and the next ranked
// candidate takes over — consumers must discard text streamed so far.
//
// Routing looks at the newest prompt only; the full history still reaches the
// model so follow-ups stay coherent.
func (s *Service) AnswerStream(ctx context.Context, req AnswerRequest, emit func(StreamEvent)) error {
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	dec, err := s.decide(ctx, prompt, len(req.Images) > 0)
	if err != nil {
		return err
	}
	emit(StreamEvent{Type: "decision", Decision: &dec})
	if dec.Selected == nil {
		return fmt.Errorf("no routable model: %s", dec.Message)
	}

	candidates := failoverOrder(dec, s.MaxAttempts)
	started := time.Now()
	var lastErr error

	for attempt, cand := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt > 0 {
			emit(StreamEvent{Type: "retry", Model: cand.Entry.Ref(), Err: lastErr.Error()})
		}

		infer := buildInferRequest(cand, prompt, req)
		attemptCtx := ctx
		cancel := func() {}
		if s.AttemptTimeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, s.AttemptTimeout)
		}

		res, err := s.inferMaybeStreaming(attemptCtx, cand.NodeID, infer, emit)
		cancel()
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", cand.Entry.Ref(), err)
			// The parent context ending is the caller's signal, not a model
			// failure — do not burn remaining candidates on it.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}

		answer, thought := SplitThink(res.Text)
		m := Metrics{
			WallMS:       time.Since(started).Milliseconds(),
			LoadMS:       res.LoadMS,
			PromptMS:     res.PromptMS,
			GenerateMS:   res.GenerateMS,
			PromptTokens: res.PromptTokens,
			OutputTokens: res.OutputTokens,
			WeightsBytes: cand.Entry.MinVRAMBytes,
		}
		if res.GenerateMS > 0 && res.OutputTokens > 0 {
			m.TokensPerSec = float64(res.OutputTokens) / (float64(res.GenerateMS) / 1000)
		}
		resp := AnswerResponse{
			Decision: dec,
			Model:    cand.Entry.Ref(),
			NodeID:   cand.NodeID,
			Answer:   answer,
			Thought:  thought,
			Metrics:  m,
			Attempts: attempt + 1,
		}
		emit(StreamEvent{Type: "done", Response: &resp})
		return nil
	}
	return fmt.Errorf("all %d candidate(s) failed; last error: %w", len(candidates), lastErr)
}

// inferMaybeStreaming prefers the streaming path when the executor offers it;
// otherwise it emits the whole reply as one delta so consumers stay uniform.
func (s *Service) inferMaybeStreaming(ctx context.Context, nodeID string, infer modelruntime.InferRequest, emit func(StreamEvent)) (modelruntime.InferResult, error) {
	if streamer, ok := s.exec.(StreamingExecutor); ok {
		return streamer.InferStream(ctx, nodeID, infer, func(delta string) {
			emit(StreamEvent{Type: "delta", Delta: delta})
		})
	}
	res, err := s.exec.Infer(ctx, nodeID, infer)
	if err == nil && res.Text != "" {
		emit(StreamEvent{Type: "delta", Delta: res.Text})
	}
	return res, err
}

// failoverOrder returns up to max ranked candidates, skipping duplicates of
// the same model on the same node (a model may appear once per node).
func failoverOrder(dec routing.Decision, max int) []routing.Candidate {
	if max <= 0 {
		max = 1
	}
	seen := map[string]bool{}
	out := make([]routing.Candidate, 0, max)
	// Selected first, then the rest of the ranked list.
	ordered := append([]routing.Candidate{*dec.Selected}, dec.Candidates...)
	for _, c := range ordered {
		key := c.Entry.Ref() + "@" + c.NodeID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
		if len(out) == max {
			break
		}
	}
	return out
}

func buildInferRequest(cand routing.Candidate, prompt string, req AnswerRequest) modelruntime.InferRequest {
	infer := modelruntime.InferRequest{
		Name: cand.Entry.Name,
		Tag:  cand.Entry.Tag,
	}
	if len(req.History) > 0 {
		msgs := make([]modelruntime.ChatMessage, 0, len(req.History)+1)
		for _, t := range req.History {
			msgs = append(msgs, modelruntime.ChatMessage{Role: t.Role, Content: t.Content})
		}
		// Attachments ride the newest user turn only.
		infer.Messages = append(msgs, modelruntime.ChatMessage{Role: "user", Content: prompt, Images: req.Images})
	} else {
		infer.Prompt = prompt
		infer.Images = req.Images
	}
	if req.KeepAlive != "" {
		infer.Options.KeepAlive = req.KeepAlive
	}
	if req.MaxTokens > 0 {
		infer.Options.MaxTokens = req.MaxTokens
	}
	return infer
}

func (s *Service) refresh(ctx context.Context, force bool) error {
	s.mu.Lock()
	stale := force || time.Since(s.fresh) > s.ttl || len(s.nodes) == 0
	s.mu.Unlock()
	if !stale {
		return nil
	}
	nodes, catalog, err := s.source.Nodes(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.nodes, s.catalog, s.fresh = nodes, catalog, time.Now()
	s.mu.Unlock()
	return nil
}

// SplitThink separates a reasoning model's <think>…</think> preamble from its
// final answer, so interfaces show decisions rather than deliberation.
func SplitThink(text string) (answer, thought string) {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	start := strings.Index(lower, "<think>")
	if start < 0 {
		return trimmed, ""
	}
	end := strings.Index(lower, "</think>")
	if end < 0 || end < start {
		return trimmed, ""
	}
	thought = strings.TrimSpace(trimmed[start+len("<think>") : end])
	answer = strings.TrimSpace(trimmed[:start] + trimmed[end+len("</think>"):])
	if answer == "" {
		return trimmed, ""
	}
	return answer, thought
}

// ── Local backend: this machine's Ollama daemon as a one-node cluster. ──────

// LocalOllama implements NodeSource and Executor over a loopback Ollama.
type LocalOllama struct {
	rt *modelruntime.Ollama
}

// NewLocalOllama wraps the Ollama daemon at baseURL.
func NewLocalOllama(baseURL string) *LocalOllama {
	return &LocalOllama{rt: modelruntime.NewOllama(baseURL)}
}

// Nodes yields this machine as a single READY node with a live-derived catalog.
func (l *LocalOllama) Nodes(ctx context.Context) ([]routing.NodeView, []routing.CatalogEntry, error) {
	if !l.rt.Available(ctx) {
		return nil, nil, fmt.Errorf("no Ollama daemon reachable at %s (start Ollama first)", l.rt.BaseURL)
	}
	models, err := l.rt.ListModels(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list local models: %w", err)
	}
	if len(models) == 0 {
		return nil, nil, fmt.Errorf("Ollama has no models; pull one first (e.g. `ollama pull deepseek-r1:14b`)")
	}
	node := routing.NodeView{
		NodeID:        "local",
		Host:          l.rt.BaseURL,
		Status:        "READY",
		ModelRuntimes: []string{"ollama"},
		Models:        models,
	}
	return []routing.NodeView{node}, routing.CatalogFromModels(models), nil
}

// Infer executes in-process; nodeID is always "local" here.
func (l *LocalOllama) Infer(ctx context.Context, _ string, req modelruntime.InferRequest) (modelruntime.InferResult, error) {
	return l.rt.Infer(ctx, req)
}

// InferStream implements StreamingExecutor over the local daemon.
func (l *LocalOllama) InferStream(ctx context.Context, _ string, req modelruntime.InferRequest, onDelta func(string)) (modelruntime.InferResult, error) {
	return l.rt.InferStream(ctx, req, onDelta)
}
