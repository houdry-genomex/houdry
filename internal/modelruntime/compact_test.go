package modelruntime

import "testing"

func TestCompactSlowInferenceTruncatesHermesDump(t *testing.T) {
	sys := make([]byte, 80000)
	for i := range sys {
		sys[i] = 'a'
	}
	in := InferRequest{
		Name: "qwen2.5-coder",
		Tag:  "1.5b",
		Messages: []ChatMessage{
			{Role: "system", Content: string(sys)},
			{Role: "user", Content: "what is the capital of france?"},
		},
		Tools:   []Tool{{Type: "function", Function: ToolFunction{Name: "web_search"}}},
		Options: InferOptions{MaxTokens: 1024, NumCtx: 32768},
	}
	out := compactSlowInference(in)
	if len(out.Messages[0].Content) > slowRuntimeSystemChars+80 {
		t.Fatalf("system still %d chars", len(out.Messages[0].Content))
	}
	if len(out.Tools) != 0 {
		t.Fatalf("trivia should not keep 37 tools, got %d", len(out.Tools))
	}
	if out.Options.NumCtx != slowRuntimeNumCtx {
		t.Fatalf("num_ctx=%d", out.Options.NumCtx)
	}
	if out.Options.MaxTokens != slowRuntimeMaxTokens {
		t.Fatalf("max_tokens=%d", out.Options.MaxTokens)
	}
}

func TestHugeAgentDumpDetectsHermesToolbox(t *testing.T) {
	in := InferRequest{Tools: make([]Tool, 37)}
	if !hugeAgentDump(in) {
		t.Fatal("37 tools should count as an agent dump")
	}
	if hugeAgentDump(InferRequest{Prompt: "hi"}) {
		t.Fatal("short prompt is not a dump")
	}
}

func TestCompactSlowInferenceKeepsToolsForCode(t *testing.T) {
	in := InferRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "Refactor this Python function and add unit tests"},
		},
		Tools: []Tool{{Type: "function", Function: ToolFunction{Name: "execute_bash"}}},
	}
	out := compactSlowInference(in)
	if len(out.Tools) != 1 {
		t.Fatalf("code tasks must keep tools, got %d", len(out.Tools))
	}
}
