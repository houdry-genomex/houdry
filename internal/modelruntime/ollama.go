package modelruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Ollama talks to a local Ollama daemon over HTTP.
// Docs: https://docs.ollama.com/api/tags
//
// This adapter maps Identity (name+tag) ↔ Ollama refs ("name:tag").
// The rest of Houdry never depends on Ollama naming.
type Ollama struct {
	BaseURL string
	Client  *http.Client
}

func NewOllama(baseURL string) *Ollama {
	if baseURL == "" {
		baseURL = os.Getenv("OLLAMA_HOST")
	}
	if baseURL == "" {
		baseURL = "http://127.0.0.1:11434"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &Ollama{
		BaseURL: baseURL,
		Client:  &http.Client{Timeout: 10 * time.Minute},
	}
}

func (o *Ollama) Name() string { return "ollama" }

func (o *Ollama) Available(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := o.shortClient().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func (o *Ollama) shortClient() *http.Client {
	return &http.Client{Timeout: 3 * time.Second}
}

func (o *Ollama) ListModels(ctx context.Context) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ollama tags: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		Models []struct {
			Name    string `json:"name"`
			Model   string `json:"model"`
			Size    int64  `json:"size"`
			Digest  string `json:"digest"`
			Details struct {
				Family            string `json:"family"`
				ParameterSize     string `json:"parameter_size"`
				QuantizationLevel string `json:"quantization_level"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}

	loaded := o.loadedSet(ctx)
	out := make([]Model, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		ref := m.Name
		if ref == "" {
			ref = m.Model
		}
		id := ParseRef(ref)
		state := StateAvailable
		if loaded[normalize(id.Ref())] || loaded[normalize(id.Name)] {
			state = StateLoaded
		}
		out = append(out, Model{
			Name:          id.Name,
			Tag:           id.Tag,
			Runtime:       o.Name(),
			State:         state,
			SizeBytes:     uint64(m.Size),
			Digest:        m.Digest,
			Family:        m.Details.Family,
			ParameterSize: m.Details.ParameterSize,
		})
	}
	return out, nil
}

func (o *Ollama) loadedSet(ctx context.Context) map[string]bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"/api/ps", nil)
	if err != nil {
		return nil
	}
	resp, err := o.shortClient().Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil
	}
	var parsed struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, m := range parsed.Models {
		ref := m.Name
		if ref == "" {
			ref = m.Model
		}
		id := ParseRef(ref)
		out[normalize(id.Ref())] = true
		out[normalize(id.Name)] = true
	}
	return out
}

func (o *Ollama) EnsureModel(ctx context.Context, name, tag string) (Model, error) {
	id := Identity{Name: name, Tag: tag, Runtime: o.Name()}
	if ms, err := o.ListModels(ctx); err == nil {
		if m, ok := Match(ms, id); ok {
			return m, nil
		}
	}
	ref := id.Ref()
	payload, _ := json.Marshal(map[string]any{
		"model":  ref,
		"stream": false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/pull", bytes.NewReader(payload))
	if err != nil {
		return Model{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.Client.Do(req)
	if err != nil {
		return Model{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return Model{}, fmt.Errorf("ollama pull: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	ms, err := o.ListModels(ctx)
	if err != nil {
		return Model{Name: name, Tag: tag, Runtime: o.Name(), State: StateAvailable}, nil
	}
	if m, ok := Match(ms, id); ok {
		return m, nil
	}
	return Model{Name: name, Tag: tag, Runtime: o.Name(), State: StateAvailable}, nil
}

func (o *Ollama) Infer(ctx context.Context, in InferRequest) (InferResult, error) {
	ref := in.Ref()
	if ref == "" {
		return InferResult{}, fmt.Errorf("model name is required")
	}
	if in.UsesChatAPI() {
		return o.inferChat(ctx, in)
	}
	if in.Prompt == "" {
		return InferResult{}, fmt.Errorf("prompt is required")
	}
	bodyMap := map[string]any{
		"model":  ref,
		"prompt": in.Prompt,
		"stream": false,
	}
	// Keep model warm so the next request skips VRAM reload.
	keepAlive := in.Options.KeepAlive
	if keepAlive == "" {
		keepAlive = "30m"
	}
	bodyMap["keep_alive"] = keepAlive

	opts := map[string]any{}
	if in.Options.MaxTokens > 0 {
		opts["num_predict"] = in.Options.MaxTokens
	}
	if in.Options.Temperature != nil {
		opts["temperature"] = *in.Options.Temperature
	}
	if len(opts) > 0 {
		bodyMap["options"] = opts
	}

	payload, _ := json.Marshal(bodyMap)
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/generate", bytes.NewReader(payload))
	if err != nil {
		return InferResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.Client.Do(req)
	if err != nil {
		return InferResult{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode >= 300 {
		return InferResult{}, fmt.Errorf("ollama generate: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		Model              string `json:"model"`
		Response           string `json:"response"`
		Done               bool   `json:"done"`
		TotalDuration      int64  `json:"total_duration"`       // ns
		LoadDuration       int64  `json:"load_duration"`        // ns
		PromptEvalDuration int64  `json:"prompt_eval_duration"` // ns
		EvalDuration       int64  `json:"eval_duration"`        // ns
		PromptEvalCount    int    `json:"prompt_eval_count"`
		EvalCount          int    `json:"eval_count"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return InferResult{}, err
	}
	outRef := parsed.Model
	if outRef == "" {
		outRef = ref
	}
	outID := ParseRef(outRef)
	dur := time.Since(start)
	loadMS := parsed.LoadDuration / 1e6
	promptMS := parsed.PromptEvalDuration / 1e6
	genMS := parsed.EvalDuration / 1e6
	return InferResult{
		Runtime:      o.Name(),
		Model:        outRef,
		Name:         firstNonEmpty(outID.Name, in.Name),
		Tag:          firstNonEmpty(outID.Tag, in.Tag),
		Text:         parsed.Response,
		FinishReason: "stop",
		Duration:     dur,
		DurationMS:   dur.Milliseconds(),
		LoadMS:       loadMS,
		PromptMS:     promptMS,
		GenerateMS:   genMS,
		PromptTokens: parsed.PromptEvalCount,
		OutputTokens: parsed.EvalCount,
		Details: map[string]any{
			"done":       parsed.Done,
			"keep_alive": keepAlive,
			"max_tokens": in.Options.MaxTokens,
			"endpoint":   "/api/generate",
			"note":       "load_ms is VRAM load/cold-start; generate_ms is token generation",
		},
	}, nil
}

func (o *Ollama) inferChat(ctx context.Context, in InferRequest) (InferResult, error) {
	ref := in.Ref()
	messages := in.Messages
	if len(messages) == 0 && in.Prompt != "" {
		messages = []ChatMessage{{Role: "user", Content: in.Prompt}}
	}
	if len(messages) == 0 {
		return InferResult{}, fmt.Errorf("messages or prompt is required")
	}

	keepAlive := in.Options.KeepAlive
	if keepAlive == "" {
		keepAlive = "30m"
	}

	ollamaMsgs := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		msg := map[string]any{
			"role":    m.Role,
			"content": m.Content,
		}
		if m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		if m.Name != "" {
			msg["name"] = m.Name
		}
		if len(m.ToolCalls) > 0 {
			// Ollama accepts OpenAI-ish tool_calls; arguments may be object or string.
			calls := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				var args any = map[string]any{}
				if strings.TrimSpace(tc.Function.Arguments) != "" {
					if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
						args = tc.Function.Arguments
					}
				}
				calls = append(calls, map[string]any{
					"type": firstNonEmpty(tc.Type, "function"),
					"function": map[string]any{
						"name":      tc.Function.Name,
						"arguments": args,
					},
				})
			}
			msg["tool_calls"] = calls
		}
		ollamaMsgs = append(ollamaMsgs, msg)
	}

	bodyMap := map[string]any{
		"model":      ref,
		"messages":   ollamaMsgs,
		"stream":     false,
		"keep_alive": keepAlive,
	}
	if len(in.Tools) > 0 {
		bodyMap["tools"] = in.Tools
	}
	if len(in.ToolChoice) > 0 && string(in.ToolChoice) != "null" {
		var tc any
		if err := json.Unmarshal(in.ToolChoice, &tc); err == nil {
			bodyMap["tool_choice"] = tc
		}
	}
	opts := map[string]any{}
	if in.Options.MaxTokens > 0 {
		opts["num_predict"] = in.Options.MaxTokens
	}
	if in.Options.Temperature != nil {
		opts["temperature"] = *in.Options.Temperature
	}
	if len(opts) > 0 {
		bodyMap["options"] = opts
	}

	payload, _ := json.Marshal(bodyMap)
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return InferResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.Client.Do(req)
	if err != nil {
		return InferResult{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode >= 300 {
		return InferResult{}, fmt.Errorf("ollama chat: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		Model   string `json:"model"`
		Message struct {
			Role      string          `json:"role"`
			Content   string          `json:"content"`
			ToolCalls json.RawMessage `json:"tool_calls"`
		} `json:"message"`
		Done               bool   `json:"done"`
		DoneReason         string `json:"done_reason"`
		LoadDuration       int64  `json:"load_duration"`
		PromptEvalDuration int64  `json:"prompt_eval_duration"`
		EvalDuration       int64  `json:"eval_duration"`
		PromptEvalCount    int    `json:"prompt_eval_count"`
		EvalCount          int    `json:"eval_count"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return InferResult{}, err
	}

	toolCalls := OllamaToolCallsToOpenAI(parsed.Message.ToolCalls)
	text := parsed.Message.Content
	// Small models often emit tool intent as JSON text instead of tool_calls.
	if len(toolCalls) == 0 && in.HasTools() {
		if recovered := NormalizeToolCallsFromText(text); len(recovered) > 0 {
			toolCalls = recovered
			text = ""
		}
	}
	finish := parsed.DoneReason
	if finish == "" {
		finish = "stop"
	}
	if len(toolCalls) > 0 {
		finish = "tool_calls"
	}

	outRef := parsed.Model
	if outRef == "" {
		outRef = ref
	}
	outID := ParseRef(outRef)
	dur := time.Since(start)
	return InferResult{
		Runtime:      o.Name(),
		Model:        outRef,
		Name:         firstNonEmpty(outID.Name, in.Name),
		Tag:          firstNonEmpty(outID.Tag, in.Tag),
		Text:         text,
		ToolCalls:    toolCalls,
		FinishReason: finish,
		Duration:     dur,
		DurationMS:   dur.Milliseconds(),
		LoadMS:       parsed.LoadDuration / 1e6,
		PromptMS:     parsed.PromptEvalDuration / 1e6,
		GenerateMS:   parsed.EvalDuration / 1e6,
		PromptTokens: parsed.PromptEvalCount,
		OutputTokens: parsed.EvalCount,
		Details: map[string]any{
			"done":       parsed.Done,
			"keep_alive": keepAlive,
			"max_tokens": in.Options.MaxTokens,
			"endpoint":   "/api/chat",
			"tools":      len(in.Tools),
		},
	}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
