package modelruntime

import "testing"

func TestIdentityMatchesAcrossRuntimes(t *testing.T) {
	want := Identity{Name: "qwen", Tag: "7b"}
	haveOllama := Identity{Name: "qwen", Tag: "7b", Runtime: "ollama"}
	haveVLLM := Identity{Name: "qwen", Tag: "7b", Runtime: "vllm"}
	if !want.Matches(haveOllama) || !want.Matches(haveVLLM) {
		t.Fatal("job without runtime should match either backend")
	}
	want.Runtime = "vllm"
	if want.Matches(haveOllama) {
		t.Fatal("runtime filter should reject ollama")
	}
	if !want.Matches(haveVLLM) {
		t.Fatal("runtime filter should accept vllm")
	}
}
