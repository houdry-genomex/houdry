package agent

import (
	"testing"

	"houdry/internal/server"
)

func TestInferOptionsDoesNotCapGeneration(t *testing.T) {
	opts := inferOptionsFor(server.Job{Payload: map[string]any{"max_tokens": float64(256)}}, "hi", true)
	if opts.MaxTokens != -1 {
		t.Fatalf("MaxTokens=%d, want -1 (unlimited) even when payload injects 256", opts.MaxTokens)
	}
	opts = inferOptionsFor(server.Job{Payload: map[string]any{"max_tokens": float64(65536)}}, "hi", true)
	if opts.MaxTokens != -1 {
		t.Fatalf("MaxTokens=%d, want -1 (unlimited)", opts.MaxTokens)
	}
}
