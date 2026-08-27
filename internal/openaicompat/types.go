// Package openaicompat provides OpenAI Chat Completions request/response
// shapes used by the Houdry compatibility layer. It does not call any model
// runtime — the server maps these types onto Houdry routing + inference jobs.
package openaicompat

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	ObjectChatCompletion      = "chat.completion"
	ObjectChatCompletionChunk = "chat.completion.chunk"
	ObjectList                = "list"
	ObjectModel               = "model"
)

// ChatCompletionRequest is the OpenAI-compatible request body.
type ChatCompletionRequest struct {
	Model       string          `json:"model"`
	Messages    []Message       `json:"messages"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Tools       []Tool          `json:"tools,omitempty"`
	ToolChoice  json.RawMessage `json:"tool_choice,omitempty"`
}

// Tool is an OpenAI function tool definition.
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

// ToolCall is an OpenAI tool invocation in a completion message.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction holds name + JSON-string arguments.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Message is a chat turn. Content may be a string, null, or a richer JSON value.
type Message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Name       string          `json:"name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
}

// ContentString extracts plain text from message content.
func (m Message) ContentString() string {
	if len(m.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return s
	}
	// Array of parts: [{"type":"text","text":"..."}, ...]
	var parts []map[string]any
	if err := json.Unmarshal(m.Content, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if t, _ := p["type"].(string); t == "text" || t == "" {
				if text, ok := p["text"].(string); ok {
					if b.Len() > 0 {
						b.WriteByte('\n')
					}
					b.WriteString(text)
				}
			}
		}
		return b.String()
	}
	return strings.TrimSpace(string(m.Content))
}

// MessagesToPrompt flattens chat messages into a single prompt for Houdry inference.
func MessagesToPrompt(messages []Message) string {
	var b strings.Builder
	for _, m := range messages {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "user"
		}
		text := strings.TrimSpace(m.ContentString())
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", role, text)
	}
	return strings.TrimSpace(b.String())
}

// LastUserText returns the last user message (used as a compact routing signal).
func LastUserText(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(messages[i].Role, "user") {
			if t := strings.TrimSpace(messages[i].ContentString()); t != "" {
				return t
			}
		}
	}
	return MessagesToPrompt(messages)
}

// ValidateChatRequest checks required fields.
func ValidateChatRequest(req ChatCompletionRequest) error {
	if len(req.Messages) == 0 {
		return fmt.Errorf("messages is required")
	}
	hasContent := false
	for _, m := range req.Messages {
		if strings.TrimSpace(m.ContentString()) != "" || len(m.ToolCalls) > 0 {
			hasContent = true
			break
		}
	}
	if !hasContent {
		return fmt.Errorf("messages must include non-empty content")
	}
	return nil
}

// IsAutoModel reports whether the client asked Houdry to route.
func IsAutoModel(model string) bool {
	m := strings.TrimSpace(strings.ToLower(model))
	return m == "" || m == "auto" || m == "houdry" || m == "houdry/auto"
}

// ChatCompletionResponse is the non-streaming OpenAI response.
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice is one completion alternative.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage token counts.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// NewAssistantMessage builds a message with string content.
func NewAssistantMessage(content string) Message {
	b, _ := json.Marshal(content)
	return Message{Role: "assistant", Content: b}
}

// NewAssistantToolCallMessage builds an assistant message with tool_calls.
// Content is JSON null (OpenAI convention when only tool_calls are present).
func NewAssistantToolCallMessage(calls []ToolCall) Message {
	return Message{
		Role:      "assistant",
		Content:   json.RawMessage("null"),
		ToolCalls: calls,
	}
}

// BuildCompletion constructs a standard chat.completion response.
func BuildCompletion(id, model, content string, promptTokens, completionTokens int) ChatCompletionResponse {
	return BuildCompletionFull(id, model, content, nil, "stop", promptTokens, completionTokens)
}

// BuildCompletionFull constructs a chat.completion with optional tool_calls.
func BuildCompletionFull(id, model, content string, toolCalls []ToolCall, finishReason string, promptTokens, completionTokens int) ChatCompletionResponse {
	if id == "" {
		id = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	if model == "" {
		model = "auto"
	}
	if finishReason == "" {
		if len(toolCalls) > 0 {
			finishReason = "tool_calls"
		} else {
			finishReason = "stop"
		}
	}
	var msg Message
	if len(toolCalls) > 0 {
		msg = NewAssistantToolCallMessage(toolCalls)
		if content != "" {
			b, _ := json.Marshal(content)
			msg.Content = b
		}
	} else {
		msg = NewAssistantMessage(content)
	}
	return ChatCompletionResponse{
		ID:      id,
		Object:  ObjectChatCompletion,
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []Choice{{
			Index:        0,
			Message:      msg,
			FinishReason: finishReason,
		}},
		Usage: Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		},
	}
}

// MessagesToRuntime converts OpenAI messages into portable chat messages.
func MessagesToRuntime(messages []Message) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		item := map[string]any{
			"role":    m.Role,
			"content": m.ContentString(),
		}
		if m.Name != "" {
			item["name"] = m.Name
		}
		if m.ToolCallID != "" {
			item["tool_call_id"] = m.ToolCallID
		}
		if len(m.ToolCalls) > 0 {
			calls := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				calls = append(calls, map[string]any{
					"id":   tc.ID,
					"type": firstNonEmpty(tc.Type, "function"),
					"function": map[string]any{
						"name":      tc.Function.Name,
						"arguments": tc.Function.Arguments,
					},
				})
			}
			item["tool_calls"] = calls
		}
		out = append(out, item)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// APIError is the OpenAI-style error envelope.
type APIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// ErrorBody wraps APIError.
type ErrorBody struct {
	Error APIError `json:"error"`
}

func NewError(typ, code, message string) ErrorBody {
	return ErrorBody{Error: APIError{Message: message, Type: typ, Code: code}}
}

// ModelsList is a minimal OpenAI /v1/models response.
type ModelsList struct {
	Object string      `json:"object"`
	Data   []ModelCard `json:"data"`
}

type ModelCard struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}
