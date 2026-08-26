package modelruntime

import (
	"context"
	"testing"
)

type fakeRuntime struct {
	name     string
	avail    bool
	models   []Model
	inferOut InferResult
}

func (f fakeRuntime) Name() string                   { return f.name }
func (f fakeRuntime) Available(context.Context) bool { return f.avail }
func (f fakeRuntime) ListModels(context.Context) ([]Model, error) {
	return f.models, nil
}
func (f fakeRuntime) EnsureModel(_ context.Context, name, tag string) (Model, error) {
	return Model{Name: name, Tag: tag, Runtime: f.name, State: StateAvailable}, nil
}
func (f fakeRuntime) Infer(_ context.Context, req InferRequest) (InferResult, error) {
	out := f.inferOut
	if out.Text == "" {
		out.Text = "hello from " + req.Ref()
	}
	out.Model = req.Ref()
	out.Name = req.Name
	out.Tag = req.Tag
	out.Runtime = f.name
	return out, nil
}

func TestParseRef(t *testing.T) {
	id := ParseRef("tinyllama")
	if id.Name != "tinyllama" || id.Tag != "" {
		t.Fatalf("%+v", id)
	}
	id = ParseRef("qwen2:0.5b")
	if id.Name != "qwen2" || id.Tag != "0.5b" {
		t.Fatalf("%+v", id)
	}
}

func TestMatchIgnoresRuntimeUnlessRequested(t *testing.T) {
	models := []Model{
		{Name: "qwen2", Tag: "7b", Runtime: "ollama", State: StateAvailable},
		{Name: "qwen2", Tag: "7b", Runtime: "vllm", State: StateAvailable},
	}
	if !HasModel(models, Identity{Name: "qwen2", Tag: "7b"}) {
		t.Fatal("expected match without runtime filter")
	}
	if !HasModel(models, Identity{Name: "qwen2", Tag: "7b", Runtime: "vllm"}) {
		t.Fatal("expected vllm match")
	}
	if HasModel(models, Identity{Name: "qwen2", Tag: "7b", Runtime: "llama.cpp"}) {
		t.Fatal("unexpected llama.cpp match")
	}
}

func TestFindPrefersRuntimeWithModel(t *testing.T) {
	runtimes := []Runtime{
		fakeRuntime{name: "ollama", avail: true, models: []Model{
			{Name: "tinyllama", Tag: "latest", Runtime: "ollama", State: StateAvailable},
		}},
		fakeRuntime{name: "vllm", avail: false},
	}
	r, m, err := Find(context.Background(), runtimes, Identity{Name: "tinyllama"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Name() != "ollama" || m.State != StateAvailable || m.Name != "tinyllama" {
		t.Fatalf("%s %+v", r.Name(), m)
	}
}

func TestEmptyTagMatchesAnyTag(t *testing.T) {
	models := []Model{{Name: "tinyllama", Tag: "latest", State: StateAvailable, Runtime: "ollama"}}
	if !HasModel(models, Identity{Name: "tinyllama"}) {
		t.Fatal("expected name-only job to match tagged inventory")
	}
}
