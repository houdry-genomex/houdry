package openaicompat

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// WriteError writes an OpenAI-compatible error JSON body.
func WriteError(w http.ResponseWriter, status int, typ, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(NewError(typ, code, message))
}

// WriteJSON writes a successful JSON body.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// WriteSSECompletion emits a minimal OpenAI-compatible SSE stream for an
// already-completed reply (no mid-generation streaming from the runtime).
func WriteSSECompletion(w http.ResponseWriter, id, model, content string) error {
	return WriteSSECompletionFull(w, id, model, content, nil)
}

// WriteSSECompletionFull emits post-completion SSE, including tool_calls when present.
func WriteSSECompletionFull(w http.ResponseWriter, id, model, content string, toolCalls []ToolCall) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming unsupported by response writer")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	created := timeUnix()
	delta := map[string]any{
		"role": "assistant",
	}
	if content != "" {
		delta["content"] = content
	}
	if len(toolCalls) > 0 {
		delta["tool_calls"] = toolCalls
	} else if content == "" {
		delta["content"] = ""
	}
	finish := any(nil)
	chunk := map[string]any{
		"id":      id,
		"object":  ObjectChatCompletionChunk,
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         delta,
			"finish_reason": finish,
		}},
	}
	if err := writeSSE(w, chunk); err != nil {
		return err
	}
	flusher.Flush()

	reason := "stop"
	if len(toolCalls) > 0 {
		reason = "tool_calls"
	}
	done := map[string]any{
		"id":      id,
		"object":  ObjectChatCompletionChunk,
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": reason,
		}},
	}
	if err := writeSSE(w, done); err != nil {
		return err
	}
	flusher.Flush()
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	flusher.Flush()
	return nil
}

func writeSSE(w http.ResponseWriter, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	return err
}

func timeUnix() int64 {
	return unixNow()
}
