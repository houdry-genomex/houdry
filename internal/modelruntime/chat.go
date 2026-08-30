package modelruntime

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// ChatMessage is a portable chat turn for runtimes that support /chat APIs.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Images     []string   `json:"images,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

// Tool is an OpenAI-style function tool definition.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction describes a callable function.
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolCall is an OpenAI-compatible tool invocation (arguments as a JSON string).
type ToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction holds the function name and JSON-string arguments.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// HasTools reports whether the request carries tool definitions.
func (r InferRequest) HasTools() bool {
	return len(r.Tools) > 0
}

// UsesChatAPI reports whether Infer should prefer a chat endpoint.
func (r InferRequest) UsesChatAPI() bool {
	return len(r.Messages) > 0 || r.HasTools() || r.ToolChoice != nil
}

var (
	reToolCallXML = regexp.MustCompile(`(?s)<tool_call>\s*(\{.*?\})\s*</tool_call>`)
	reFenceJSON   = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")
)

// NormalizeToolCallsFromText recovers tool calls when a model (or runtime)
// emits tool intent as plain text instead of structured tool_calls.
// Returns nil when content is not a recognizable tool call.
func NormalizeToolCallsFromText(content string) []ToolCall {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	candidates := []string{}
	if m := reToolCallXML.FindStringSubmatch(content); len(m) == 2 {
		candidates = append(candidates, m[1])
	}
	if m := reFenceJSON.FindStringSubmatch(content); len(m) == 2 {
		candidates = append(candidates, m[1])
	}
	candidates = append(candidates, content)

	for _, c := range candidates {
		if tc := parseToolCallJSON(c); len(tc) > 0 {
			return tc
		}
	}
	return nil
}

func parseToolCallJSON(raw string) []ToolCall {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Single object: {"name":"...","arguments":{...}} or OpenAI function shape.
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err == nil {
		if tc, ok := toolCallFromMap(obj); ok {
			return []ToolCall{tc}
		}
	}
	// Array of calls.
	var arr []map[string]any
	if err := json.Unmarshal([]byte(raw), &arr); err == nil {
		var out []ToolCall
		for _, m := range arr {
			if tc, ok := toolCallFromMap(m); ok {
				out = append(out, tc)
			}
		}
		return out
	}
	return nil
}

func toolCallFromMap(m map[string]any) (ToolCall, bool) {
	// {"name":"...","arguments":...}
	if name, _ := m["name"].(string); name != "" {
		if _, hasArgs := m["arguments"]; hasArgs {
			return ToolCall{
				Type: "function",
				Function: ToolCallFunction{
					Name:      name,
					Arguments: argsToJSONString(m["arguments"]),
				},
			}, true
		}
	}
	// {"type":"function","function":{"name":"...","arguments":...}}
	if fn, ok := m["function"].(map[string]any); ok {
		name, _ := fn["name"].(string)
		if name == "" {
			return ToolCall{}, false
		}
		id, _ := m["id"].(string)
		typ, _ := m["type"].(string)
		if typ == "" {
			typ = "function"
		}
		return ToolCall{
			ID:   id,
			Type: typ,
			Function: ToolCallFunction{
				Name:      name,
				Arguments: argsToJSONString(fn["arguments"]),
			},
		}, true
	}
	return ToolCall{}, false
}

func argsToJSONString(v any) string {
	switch a := v.(type) {
	case nil:
		return "{}"
	case string:
		if strings.TrimSpace(a) == "" {
			return "{}"
		}
		// Already a JSON string?
		if json.Valid([]byte(a)) {
			return a
		}
		b, _ := json.Marshal(a)
		return string(b)
	default:
		b, err := json.Marshal(a)
		if err != nil {
			return "{}"
		}
		return string(b)
	}
}

// OllamaToolCallsToOpenAI converts Ollama's tool_calls (arguments as objects)
// into OpenAI-compatible tool_calls (arguments as JSON strings).
func OllamaToolCallsToOpenAI(raw json.RawMessage) []ToolCall {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	out := make([]ToolCall, 0, len(arr))
	for i, m := range arr {
		tc, ok := toolCallFromMap(m)
		if !ok {
			continue
		}
		if tc.ID == "" {
			tc.ID = "call_" + strconv.Itoa(i)
		}
		if tc.Type == "" {
			tc.Type = "function"
		}
		out = append(out, tc)
	}
	return out
}
