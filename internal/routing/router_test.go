package routing

import (
	"testing"

	"houdry/internal/modelruntime"
)

func TestAnalyzeSimpleVsCode(t *testing.T) {
	simple := Analyze("Say hello in one short sentence")
	if simple.Complexity != ComplexityLow || simple.Modality != ModalityText {
		t.Fatalf("simple: %+v", simple)
	}
	code := Analyze("Refactor this Go function and add unit tests:\nfunc Foo() {}")
	if code.Modality != ModalityCode {
		t.Fatalf("code modality: %+v", code)
	}
	if ComplexityRank(code.Complexity) < ComplexityRank(ComplexityMedium) {
		t.Fatalf("code complexity: %+v", code)
	}
}

func TestRoutePrefersSimpleModelForGreeting(t *testing.T) {
	nodes := []NodeView{{
		NodeID: "n1", Host: "laptop", Status: "READY",
		ModelRuntimes: []string{"ollama"},
		Models: []modelruntime.Model{
			{Name: "tinyllama", Tag: "latest", Runtime: "ollama", State: modelruntime.StateAvailable},
			{Name: "qwen2.5-coder", Tag: "1.5b", Runtime: "ollama", State: modelruntime.StateAvailable},
		},
		VRAMTotal: 4 << 30, VRAMAvailable: 4 << 30,
	}}
	d := Route(RouteRequest{
		Prompt: "Say hello from Houdry", Catalog: DefaultCatalog(), Nodes: nodes,
		PreferLoaded: true, AllowPull: true,
	})
	if d.Selected == nil {
		t.Fatalf("no selection: %+v", d)
	}
	if d.Selected.Entry.Name != "tinyllama" && d.Selected.Entry.Name != "qwen2.5" {
		t.Fatalf("expected small model, got %s", d.Selected.Entry.Ref())
	}
}

func TestRoutePrefersCoderForCodeTask(t *testing.T) {
	nodes := []NodeView{{
		NodeID: "n1", Host: "laptop", Status: "READY",
		ModelRuntimes: []string{"ollama"},
		Models: []modelruntime.Model{
			{Name: "tinyllama", Tag: "latest", Runtime: "ollama", State: modelruntime.StateAvailable},
			{Name: "qwen2.5-coder", Tag: "1.5b", Runtime: "ollama", State: modelruntime.StateAvailable},
		},
		VRAMTotal: 4 << 30, VRAMAvailable: 4 << 30,
	}}
	d := Route(RouteRequest{
		Prompt: "Refactor this Python function and fix the bug in foo()", Catalog: DefaultCatalog(), Nodes: nodes,
		PreferLoaded: true, AllowPull: false, RequirePresent: true,
	})
	if d.Selected == nil || d.Selected.Entry.Name != "qwen2.5-coder" {
		t.Fatalf("expected coder model, got %+v", d.Selected)
	}
}

func TestRouteDefersVision(t *testing.T) {
	d := Route(RouteRequest{
		Prompt: "OCR this scanned PDF invoice", Catalog: DefaultCatalog(),
		Nodes:     []NodeView{{NodeID: "n1", Status: "READY", ModelRuntimes: []string{"ollama"}}},
		AllowPull: true,
	})
	if !d.Deferred {
		t.Fatalf("expected deferred: %+v", d)
	}
}

func TestRouteFallbackWhenOnlyTinyPresent(t *testing.T) {
	nodes := []NodeView{{
		NodeID: "n1", Status: "READY", ModelRuntimes: []string{"ollama"},
		Models: []modelruntime.Model{
			{Name: "tinyllama", Tag: "latest", Runtime: "ollama", State: modelruntime.StateAvailable},
		},
		VRAMTotal: 4 << 30, VRAMAvailable: 4 << 30,
	}}
	d := Route(RouteRequest{
		Prompt:  "Refactor this large Go codebase module and redesign the architecture",
		Catalog: DefaultCatalog(), Nodes: nodes, AllowPull: false, RequirePresent: true,
	})
	if d.Selected == nil || d.Selected.Entry.Name != "tinyllama" {
		t.Fatalf("expected fallback to present tinyllama: %+v", d)
	}
}

func TestRouteToolsAvoidsTinyllama(t *testing.T) {
	nodes := []NodeView{{
		NodeID: "n1", Host: "laptop", Status: "READY",
		ModelRuntimes: []string{"ollama"},
		Models: []modelruntime.Model{
			{Name: "tinyllama", Tag: "latest", Runtime: "ollama", State: modelruntime.StateLoaded},
			{Name: "qwen2.5-coder", Tag: "1.5b", Runtime: "ollama", State: modelruntime.StateAvailable},
		},
		VRAMTotal: 4 << 30, VRAMAvailable: 4 << 30,
	}}
	d := Route(RouteRequest{
		Prompt: "Say hello from Houdry", Catalog: DefaultCatalog(), Nodes: nodes,
		PreferLoaded: true, AllowPull: false, RequirePresent: true, RequireTools: true,
	})
	if d.Selected == nil || d.Selected.Entry.Name != "qwen2.5-coder" {
		t.Fatalf("expected tool-capable coder, got %+v msg=%s", d.Selected, d.Message)
	}
}

func TestRouteToolsNoFallbackToTinyllama(t *testing.T) {
	nodes := []NodeView{{
		NodeID: "n1", Status: "READY", ModelRuntimes: []string{"ollama"},
		Models: []modelruntime.Model{
			{Name: "tinyllama", Tag: "latest", Runtime: "ollama", State: modelruntime.StateAvailable},
		},
		VRAMTotal: 4 << 30, VRAMAvailable: 4 << 30,
	}}
	d := Route(RouteRequest{
		Prompt: "hi", Catalog: DefaultCatalog(), Nodes: nodes,
		AllowPull: false, RequirePresent: true, RequireTools: true,
	})
	if d.Selected != nil {
		t.Fatalf("expected no selection when only tinyllama present with tools: %+v", d.Selected)
	}
}

func TestEntrySupportsToolsHeuristic(t *testing.T) {
	if EntrySupportsTools(CatalogEntry{Name: "tinyllama", Tag: "latest"}) {
		t.Fatal("tinyllama should not support tools")
	}
	if !EntrySupportsTools(CatalogEntry{Name: "qwen2.5-coder", Tag: "1.5b", Capabilities: []string{"code", "chat"}}) {
		t.Fatal("coder heuristic should support tools")
	}
}
