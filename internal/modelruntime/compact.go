package modelruntime

import "strings"

// Caps applied when Ollama is running on CPU (size_vram=0). Hermes Agent
// otherwise sends ~80k-character system prompts plus dozens of tool schemas;
// prompt-eval of that on CPU is six minutes before the first token.
const (
	slowRuntimeSystemChars = 4000
	slowRuntimeNumCtx      = 4096
	slowRuntimeMaxTokens   = 128
)

func compactSlowInference(in InferRequest) InferRequest {
	out := in
	if len(in.Messages) > 0 {
		msgs := make([]ChatMessage, len(in.Messages))
		copy(msgs, in.Messages)
		for i := range msgs {
			if strings.EqualFold(msgs[i].Role, "system") && len(msgs[i].Content) > slowRuntimeSystemChars {
				msgs[i].Content = msgs[i].Content[:slowRuntimeSystemChars] + "\n[truncated for local CPU inference]"
			}
		}
		out.Messages = msgs
	}
	if len(out.Prompt) > slowRuntimeSystemChars {
		out.Prompt = out.Prompt[:slowRuntimeSystemChars] + "\n[truncated for local CPU inference]"
	}
	if !keepToolsOnSlowRuntime(lastUserText(out.Messages), out.Prompt) {
		out.Tools = nil
		out.ToolChoice = nil
	}
	if out.Options.NumCtx <= 0 || out.Options.NumCtx > slowRuntimeNumCtx {
		out.Options.NumCtx = slowRuntimeNumCtx
	}
	if out.Options.MaxTokens <= 0 || out.Options.MaxTokens > slowRuntimeMaxTokens {
		out.Options.MaxTokens = slowRuntimeMaxTokens
	}
	return out
}

func lastUserText(msgs []ChatMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if strings.EqualFold(msgs[i].Role, "user") && strings.TrimSpace(msgs[i].Content) != "" {
			return msgs[i].Content
		}
	}
	return ""
}

func hugeAgentDump(in InferRequest) bool {
	if len(in.Tools) >= 8 {
		return true
	}
	n := len(in.Prompt)
	for _, m := range in.Messages {
		n += len(m.Content)
	}
	return n >= 16000
}

func keepToolsOnSlowRuntime(user, prompt string) bool {
	lower := strings.ToLower(user + "\n" + prompt)
	needles := []string{
		"implement ", "refactor", "debug ", "unit test", "codebase",
		"```", ".py", "write a function", "write a program", "fix this code",
		"typescript", "golang", "stack trace",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}
