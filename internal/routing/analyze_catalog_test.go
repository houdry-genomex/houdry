package routing

import (
	"testing"

	"houdry/internal/modelruntime"
)

func TestAnalyzeTiersByPromptWeight(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		want   Complexity
	}{
		{"greeting is low", "hi", ComplexityLow},
		{"one-liner is low", "say hello in one sentence", ComplexityLow},
		{"proof is high", "Prove that the square root of 2 is irrational, step by step.", ComplexityHigh},
		{"code with fence is medium+", "Fix this code:\n```go\nfunc main() { fmt.Println(x) }\n```", ComplexityMedium},
		{"analysis is medium", "Compare the trade-offs between message queues and shared databases for service integration, and explain why one scales better.", ComplexityMedium},
		{"plain factual question is medium", "What is the capital of France?", ComplexityMedium},
		{"composition request is medium", "Write a short poem about refinery safety.", ComplexityMedium},
		{"deep dive is high", "Do a deep dive research on hydrogen embrittlement in pipelines and explain the failure modes.", ComplexityHigh},
		{"root cause is medium+", "Investigate the root cause of the pump seal failure.", ComplexityMedium},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Analyze(tc.prompt)
			if ComplexityRank(got.Complexity) < ComplexityRank(tc.want) {
				t.Fatalf("Analyze(%q).Complexity = %s (score %d, hints %v), want at least %s",
					tc.prompt, got.Complexity, got.Score, got.Hints, tc.want)
			}
			if tc.want == ComplexityLow && got.Complexity != ComplexityLow {
				t.Fatalf("Analyze(%q).Complexity = %s (score %d), want low", tc.prompt, got.Complexity, got.Score)
			}
		})
	}
}

func TestAnalyzeCodePromptGetsCodeCapability(t *testing.T) {
	got := Analyze("Refactor this Python function to avoid the stack trace on empty input.")
	if got.Modality != ModalityCode {
		t.Fatalf("modality = %s, want code", got.Modality)
	}
	if !hasCap(got.Capabilities, "code") {
		t.Fatalf("capabilities = %v, want code", got.Capabilities)
	}
}

func localModels() []modelruntime.Model {
	return []modelruntime.Model{
		{Name: "deepseek-r1", Tag: "14b", Runtime: "ollama", State: modelruntime.StateAvailable, SizeBytes: 9 << 30},
		{Name: "lfm2.5-thinking", Tag: "1.2b", Runtime: "ollama", State: modelruntime.StateAvailable, SizeBytes: 731 << 20},
	}
}

func TestCatalogFromModelsInfersTiers(t *testing.T) {
	entries := CatalogFromModels(localModels())
	byRef := map[string]CatalogEntry{}
	for _, e := range entries {
		byRef[e.Ref()] = e
	}

	deep := byRef["deepseek-r1:14b"]
	if deep.MaxComplexity != ComplexityHigh {
		t.Fatalf("deepseek-r1:14b tier = %s, want high", deep.MaxComplexity)
	}
	if !hasCap(deep.Capabilities, "reasoning") {
		t.Fatalf("deepseek-r1:14b caps = %v, want reasoning", deep.Capabilities)
	}

	small := byRef["lfm2.5-thinking:1.2b"]
	if small.MaxComplexity != ComplexityLow {
		t.Fatalf("lfm2.5-thinking:1.2b tier = %s, want low", small.MaxComplexity)
	}
	if !hasCap(small.Capabilities, "simple") {
		t.Fatalf("lfm2.5-thinking:1.2b caps = %v, want simple", small.Capabilities)
	}
}

func TestRouteRightSizesAcrossLocalModels(t *testing.T) {
	models := localModels()
	node := NodeView{
		NodeID:        "local",
		Status:        "READY",
		ModelRuntimes: []string{"ollama"},
		Models:        models,
	}
	req := func(prompt string) RouteRequest {
		return RouteRequest{
			Prompt:         prompt,
			Catalog:        CatalogFromModels(models),
			Nodes:          []NodeView{node},
			RequirePresent: true,
		}
	}

	easy := Route(req("hi"))
	if easy.Selected == nil || easy.Selected.Entry.Ref() != "lfm2.5-thinking:1.2b" {
		t.Fatalf("easy prompt routed to %+v, want lfm2.5-thinking:1.2b (candidates %v)", easy.Selected, easy.Candidates)
	}

	hard := Route(req("Prove that the square root of 2 is irrational, step by step, and explain the contradiction."))
	if hard.Selected == nil || hard.Selected.Entry.Ref() != "deepseek-r1:14b" {
		t.Fatalf("hard prompt routed to %+v, want deepseek-r1:14b (candidates %v)", hard.Selected, hard.Candidates)
	}
}

// With three tiers installed, a medium prompt goes to the mid-size model even
// while the big reasoning model sits warm in VRAM: right-sizing outvotes both
// warmth and priority.
func TestMediumPromptPrefersMidTierOverWarmLarge(t *testing.T) {
	models := []modelruntime.Model{
		{Name: "deepseek-r1", Tag: "14b", Runtime: "ollama", State: modelruntime.StateLoaded, SizeBytes: 9 << 30},
		{Name: "llama3.1", Tag: "8b", Runtime: "ollama", State: modelruntime.StateAvailable, SizeBytes: 5 << 30},
		{Name: "lfm2.5-thinking", Tag: "1.2b", Runtime: "ollama", State: modelruntime.StateAvailable, SizeBytes: 731 << 20},
	}
	node := NodeView{NodeID: "local", Status: "READY", ModelRuntimes: []string{"ollama"}, Models: models}
	dec := Route(RouteRequest{
		Prompt:         "What is the capital of France?",
		Catalog:        CatalogFromModels(models),
		Nodes:          []NodeView{node},
		RequirePresent: true,
		PreferLoaded:   true,
	})
	if dec.Selected == nil || dec.Selected.Entry.Ref() != "llama3.1:8b" {
		t.Fatalf("medium prompt routed to %+v, want llama3.1:8b (candidates %v)", dec.Selected, dec.Candidates)
	}
}

// A warm oversized model must not hijack trivial prompts: warmth is a latency
// bonus, and the tier-scaled cap keeps right-sizing in charge.
func TestWarmOverkillModelDoesNotStealTrivialPrompts(t *testing.T) {
	models := []modelruntime.Model{
		{Name: "deepseek-r1", Tag: "14b", Runtime: "ollama", State: modelruntime.StateLoaded, SizeBytes: 9 << 30},
		{Name: "lfm2.5-thinking", Tag: "1.2b", Runtime: "ollama", State: modelruntime.StateAvailable, SizeBytes: 731 << 20},
	}
	node := NodeView{NodeID: "local", Status: "READY", ModelRuntimes: []string{"ollama"}, Models: models}
	dec := Route(RouteRequest{
		Prompt:         "hi",
		Catalog:        CatalogFromModels(models),
		Nodes:          []NodeView{node},
		RequirePresent: true,
		PreferLoaded:   true,
	})
	if dec.Selected == nil || dec.Selected.Entry.Ref() != "lfm2.5-thinking:1.2b" {
		t.Fatalf("trivial prompt routed to %+v with warm 14B present, want lfm2.5-thinking:1.2b (candidates %v)",
			dec.Selected, dec.Candidates)
	}
}

func TestParseParamBillions(t *testing.T) {
	cases := map[string]float64{
		"deepseek-r1:14b":      14,
		"lfm2.5-thinking:1.2b": 1.2,
		"qwen2.5vl:7b":         7,
		"no-size:latest":       0,
	}
	for ref, want := range cases {
		if got := parseParamBillions(ref, ""); got != want {
			t.Fatalf("parseParamBillions(%q) = %v, want %v", ref, got, want)
		}
	}
	if got := parseParamBillions("model:latest", "14.8B"); got != 14.8 {
		t.Fatalf("parameterSize path = %v, want 14.8", got)
	}
}
