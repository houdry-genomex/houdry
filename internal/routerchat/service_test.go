package routerchat

import (
	"context"
	"testing"

	"houdry/internal/modelruntime"
	"houdry/internal/routing"
)

type fakeBackend struct {
	models   []modelruntime.Model
	lastReq  modelruntime.InferRequest
	lastNode string
}

func (f *fakeBackend) Nodes(context.Context) ([]routing.NodeView, []routing.CatalogEntry, error) {
	node := routing.NodeView{
		NodeID:        "local",
		Status:        "READY",
		ModelRuntimes: []string{"ollama"},
		Models:        f.models,
	}
	return []routing.NodeView{node}, routing.CatalogFromModels(f.models), nil
}

func (f *fakeBackend) Infer(_ context.Context, nodeID string, req modelruntime.InferRequest) (modelruntime.InferResult, error) {
	f.lastNode = nodeID
	f.lastReq = req
	return modelruntime.InferResult{
		Model:        req.Ref(),
		Text:         "<think>weighing options</think>final answer",
		GenerateMS:   500,
		OutputTokens: 100,
	}, nil
}

func testModels() []modelruntime.Model {
	return []modelruntime.Model{
		{Name: "deepseek-r1", Tag: "14b", Runtime: "ollama", State: modelruntime.StateAvailable},
		{Name: "lfm2.5-thinking", Tag: "1.2b", Runtime: "ollama", State: modelruntime.StateAvailable},
	}
}

func TestAnswerRightSizesAndSplitsThought(t *testing.T) {
	backend := &fakeBackend{models: testModels()}
	svc := New(backend, backend)

	easy, err := svc.Answer(context.Background(), AnswerRequest{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if easy.Model != "lfm2.5-thinking:1.2b" {
		t.Fatalf("easy prompt ran %s, want lfm2.5-thinking:1.2b", easy.Model)
	}
	if easy.Answer != "final answer" || easy.Thought != "weighing options" {
		t.Fatalf("think split wrong: answer=%q thought=%q", easy.Answer, easy.Thought)
	}
	if easy.Metrics.TokensPerSec != 200 {
		t.Fatalf("tokens/sec = %v, want 200", easy.Metrics.TokensPerSec)
	}

	hard, err := svc.Answer(context.Background(), AnswerRequest{
		Prompt: "Prove that the square root of 2 is irrational, step by step.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if hard.Model != "deepseek-r1:14b" {
		t.Fatalf("hard prompt ran %s, want deepseek-r1:14b", hard.Model)
	}
}

func TestAnswerCarriesHistoryToTheModel(t *testing.T) {
	backend := &fakeBackend{models: testModels()}
	svc := New(backend, backend)

	_, err := svc.Answer(context.Background(), AnswerRequest{
		Prompt:  "and what about 3?",
		History: []Turn{{Role: "user", Content: "is 2 prime?"}, {Role: "assistant", Content: "yes"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs := backend.lastReq.Messages
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want history(2) + prompt(1)", len(msgs))
	}
	if msgs[2].Role != "user" || msgs[2].Content != "and what about 3?" {
		t.Fatalf("last message = %+v, want the new prompt", msgs[2])
	}
	if backend.lastNode != "local" {
		t.Fatalf("executed on %q, want local", backend.lastNode)
	}
}

// flakyBackend fails the first N Infer attempts, then succeeds — the failover
// path must walk the ranked candidates and report the attempt count.
type flakyBackend struct {
	fakeBackend
	failures int
	attempts []string
}

func (f *flakyBackend) Infer(ctx context.Context, nodeID string, req modelruntime.InferRequest) (modelruntime.InferResult, error) {
	f.attempts = append(f.attempts, req.Ref())
	if len(f.attempts) <= f.failures {
		return modelruntime.InferResult{}, context.DeadlineExceeded
	}
	return f.fakeBackend.Infer(ctx, nodeID, req)
}

func TestAnswerFailsOverToNextCandidate(t *testing.T) {
	backend := &flakyBackend{fakeBackend: fakeBackend{models: testModels()}, failures: 1}
	svc := New(backend, backend)

	var events []string
	var resp AnswerResponse
	err := svc.AnswerStream(context.Background(), AnswerRequest{Prompt: "hi"}, func(ev StreamEvent) {
		events = append(events, ev.Type)
		if ev.Type == "done" {
			resp = *ev.Response
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (one failure, one success)", resp.Attempts)
	}
	retries := 0
	for _, e := range events {
		if e == "retry" {
			retries++
		}
	}
	if retries != 1 {
		t.Fatalf("retry events = %d, want 1 (events: %v)", retries, events)
	}
	if len(backend.attempts) != 2 || backend.attempts[0] == backend.attempts[1] {
		t.Fatalf("attempted models = %v, want two distinct candidates", backend.attempts)
	}
}

func TestAnswerPlumbsMaxTokens(t *testing.T) {
	backend := &fakeBackend{models: testModels()}
	svc := New(backend, backend)

	if _, err := svc.Answer(context.Background(), AnswerRequest{Prompt: "hi", MaxTokens: 512}); err != nil {
		t.Fatal(err)
	}
	if backend.lastReq.Options.MaxTokens != 512 {
		t.Fatalf("MaxTokens = %d, want 512", backend.lastReq.Options.MaxTokens)
	}
}
