package agent

import (
	"testing"

	"houdry/internal/server"
)

func TestInferOptionsIgnoresUnboundedAgentMaxTokens(t *testing.T) {
	opts := inferOptionsFor(server.Job{Payload: map[string]any{"max_tokens": float64(65536)}}, "hi", true)
	if opts.MaxTokens != 256 {
		t.Fatalf("MaxTokens=%d, want heuristic 256 for unbounded client max_tokens", opts.MaxTokens)
	}
}
