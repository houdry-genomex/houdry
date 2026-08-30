package modelruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// InferStream runs an inference with streaming: onDelta receives each text
// chunk as the model generates it, and the returned InferResult carries the
// full text plus the same timing/token metrics Infer reports.
//
// Prompt-only requests use /api/generate; requests with Messages use
// /api/chat (role/content turns — tools are not supported on the streaming
// path, which the router chat never sends anyway).
func (o *Ollama) InferStream(ctx context.Context, in InferRequest, onDelta func(string)) (InferResult, error) {
	ref := in.Ref()
	if ref == "" {
		return InferResult{}, fmt.Errorf("model name is required")
	}

	keepAlive := in.Options.KeepAlive
	if keepAlive == "" {
		keepAlive = "30m"
	}

	endpoint := "/api/generate"
	bodyMap := map[string]any{
		"model":      ref,
		"stream":     true,
		"keep_alive": keepAlive,
	}
	if len(in.Messages) > 0 {
		endpoint = "/api/chat"
		msgs := make([]map[string]any, 0, len(in.Messages))
		for _, m := range in.Messages {
			msg := map[string]any{"role": m.Role, "content": m.Content}
			if len(m.Images) > 0 {
				msg["images"] = m.Images
			}
			msgs = append(msgs, msg)
		}
		bodyMap["messages"] = msgs
	} else {
		if in.Prompt == "" {
			return InferResult{}, fmt.Errorf("prompt is required")
		}
		bodyMap["prompt"] = in.Prompt
		if len(in.Images) > 0 {
			bodyMap["images"] = in.Images
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+endpoint, bytes.NewReader(payload))
	if err != nil {
		return InferResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.Client.Do(req)
	if err != nil {
		return InferResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body := make([]byte, 0, 512)
		buf := bufio.NewReader(resp.Body)
		for len(body) < 4096 {
			b, err := buf.ReadByte()
			if err != nil {
				break
			}
			body = append(body, b)
		}
		return InferResult{}, fmt.Errorf("ollama %s: %s: %s", endpoint, resp.Status, strings.TrimSpace(string(body)))
	}

	// One JSON object per line; the final line has done=true plus metrics.
	type chunk struct {
		Model    string `json:"model"`
		Response string `json:"response"` // generate endpoint
		Message  struct {
			Content string `json:"content"` // chat endpoint
		} `json:"message"`
		Done               bool  `json:"done"`
		TotalDuration      int64 `json:"total_duration"`
		LoadDuration       int64 `json:"load_duration"`
		PromptEvalDuration int64 `json:"prompt_eval_duration"`
		EvalDuration       int64 `json:"eval_duration"`
		PromptEvalCount    int   `json:"prompt_eval_count"`
		EvalCount          int   `json:"eval_count"`
	}

	var full strings.Builder
	var final chunk
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var c chunk
		if err := json.Unmarshal(line, &c); err != nil {
			continue // tolerate keep-alive noise; the final line is what matters
		}
		text := c.Response
		if text == "" {
			text = c.Message.Content
		}
		if text != "" {
			full.WriteString(text)
			if onDelta != nil {
				onDelta(text)
			}
		}
		if c.Done {
			final = c
		}
	}
	if err := scanner.Err(); err != nil {
		return InferResult{}, fmt.Errorf("stream read: %w", err)
	}

	outRef := final.Model
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
		Text:         full.String(),
		FinishReason: "stop",
		Duration:     dur,
		DurationMS:   dur.Milliseconds(),
		LoadMS:       final.LoadDuration / 1e6,
		PromptMS:     final.PromptEvalDuration / 1e6,
		GenerateMS:   final.EvalDuration / 1e6,
		PromptTokens: final.PromptEvalCount,
		OutputTokens: final.EvalCount,
		Details: map[string]any{
			"done":       final.Done,
			"keep_alive": keepAlive,
			"max_tokens": in.Options.MaxTokens,
			"endpoint":   endpoint,
			"streamed":   true,
		},
	}, nil
}
