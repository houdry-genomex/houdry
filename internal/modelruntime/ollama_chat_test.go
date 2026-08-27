package modelruntime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOllamaInferChatForwardsToolsAndMapsToolCalls(t *testing.T) {
	var sawTools bool
	var endpoint string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		endpoint = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		if _, ok := req["tools"]; ok {
			sawTools = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "qwen2.5-coder:1.5b",
			"message": map[string]any{
				"role":    "assistant",
				"content": "",
				"tool_calls": []map[string]any{{
					"function": map[string]any{
						"name":      "execute_bash",
						"arguments": map[string]any{"command": "python fibonacci.py"},
					},
				}},
			},
			"done":                true,
			"done_reason":         "stop",
			"prompt_eval_count":   10,
			"eval_count":          5,
			"load_duration":       1e6,
			"prompt_eval_duration": 2e6,
			"eval_duration":       3e6,
		})
	}))
	defer ts.Close()

	o := NewOllama(ts.URL)
	o.Client = &http.Client{Timeout: 5 * time.Second}
	res, err := o.Infer(context.Background(), InferRequest{
		Name: "qwen2.5-coder",
		Tag:  "1.5b",
		Messages: []ChatMessage{
			{Role: "user", Content: "run fibonacci"},
		},
		Tools: []Tool{{
			Type: "function",
			Function: ToolFunction{
				Name:        "execute_bash",
				Description: "Run a shell command",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
			},
		}},
		Options: InferOptions{MaxTokens: 128},
	})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "/api/chat" {
		t.Fatalf("endpoint=%s", endpoint)
	}
	if !sawTools {
		t.Fatal("tools not forwarded to Ollama")
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Function.Name != "execute_bash" {
		t.Fatalf("tool_calls=%+v", res.ToolCalls)
	}
	if res.FinishReason != "tool_calls" {
		t.Fatalf("finish=%s", res.FinishReason)
	}
	if !strings.Contains(res.ToolCalls[0].Function.Arguments, "fibonacci") {
		t.Fatalf("args=%s", res.ToolCalls[0].Function.Arguments)
	}
}

func TestOllamaInferChatRecoversToolCallsFromText(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "qwen2.5-coder:1.5b",
			"message": map[string]any{
				"role":    "assistant",
				"content": `{"name":"get_weather","arguments":{"city":"Paris"}}`,
			},
			"done": true,
		})
	}))
	defer ts.Close()

	o := NewOllama(ts.URL)
	res, err := o.Infer(context.Background(), InferRequest{
		Name:     "qwen2.5-coder",
		Tag:      "1.5b",
		Messages: []ChatMessage{{Role: "user", Content: "weather?"}},
		Tools:    []Tool{{Type: "function", Function: ToolFunction{Name: "get_weather"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("%+v", res.ToolCalls)
	}
	if res.Text != "" {
		t.Fatalf("expected cleared text, got %q", res.Text)
	}
}
