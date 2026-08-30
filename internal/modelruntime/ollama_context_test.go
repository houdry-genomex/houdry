package modelruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// showServer stands in for an Ollama daemon's /api/show, counting hits so the
// cache can be observed.
func showServer(t *testing.T, body map[string]any, hits *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/show" {
			http.NotFound(w, r)
			return
		}
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Each test gets a clean cache; it is process-global by design.
func resetNumCtxCache(t *testing.T) {
	t.Helper()
	sharedNumCtx.mu.Lock()
	sharedNumCtx.byRef = map[string]int{}
	sharedNumCtx.loaded = map[string]bool{}
	sharedNumCtx.mu.Unlock()
}

func TestResolveNumCtxUsesModelMaximum(t *testing.T) {
	resetNumCtxCache(t)
	srv := showServer(t, map[string]any{
		"model_info": map[string]any{"qwen2.context_length": 32768},
	}, nil)

	o := NewOllama(srv.URL)
	if got := o.resolveNumCtx(context.Background(), "deepseek-r1:1.5b", 0); got != 32768 {
		t.Fatalf("num_ctx = %d, want 32768", got)
	}
}

func TestResolveNumCtxCapsHugeWindows(t *testing.T) {
	resetNumCtxCache(t)
	// deepseek-r1:1.5b reports 131072. Honouring that verbatim allocates a KV
	// cache no laptop GPU has room for, so the cap is what keeps detection from
	// trading a 4096 wall for an out-of-memory one.
	srv := showServer(t, map[string]any{
		"model_info": map[string]any{"qwen2.context_length": 131072},
	}, nil)

	o := NewOllama(srv.URL)
	if got := o.resolveNumCtx(context.Background(), "deepseek-r1:1.5b", 0); got != defaultNumCtxCap {
		t.Fatalf("num_ctx = %d, want cap %d", got, defaultNumCtxCap)
	}
}

func TestResolveNumCtxHonoursModelfileParameter(t *testing.T) {
	resetNumCtxCache(t)
	// A baked-in PARAMETER num_ctx beats GGUF metadata: the author meant it,
	// and requesting something else would make their Modelfile a lie.
	srv := showServer(t, map[string]any{
		"parameters": "stop \"<|end|>\"\nnum_ctx 16384\n",
		"model_info": map[string]any{"llama.context_length": 131072},
	}, nil)

	o := NewOllama(srv.URL)
	if got := o.resolveNumCtx(context.Background(), "custom:latest", 0); got != 16384 {
		t.Fatalf("num_ctx = %d, want 16384", got)
	}
}

func TestResolveNumCtxFallsBackAboveDaemonDefault(t *testing.T) {
	resetNumCtxCache(t)
	// Unreachable daemon, non-Ollama runtime, GGUF without a context_length —
	// all land here. The floor must still clear 4096, or detection failing puts
	// us back at the wall it exists to remove.
	o := NewOllama("http://127.0.0.1:1")
	got := o.resolveNumCtx(context.Background(), "whatever:latest", 0)
	if got != fallbackNumCtx {
		t.Fatalf("num_ctx = %d, want %d", got, fallbackNumCtx)
	}
	if got <= 4096 {
		t.Fatalf("fallback %d does not clear the daemon default", got)
	}
}

func TestResolveNumCtxPrefersExplicitRequest(t *testing.T) {
	resetNumCtxCache(t)
	srv := showServer(t, map[string]any{
		"model_info": map[string]any{"qwen2.context_length": 131072},
	}, nil)

	o := NewOllama(srv.URL)
	// A caller who named a number is answering for their own hardware, so it is
	// passed through unclamped.
	if got := o.resolveNumCtx(context.Background(), "m:latest", 65536); got != 65536 {
		t.Fatalf("num_ctx = %d, want 65536", got)
	}
}

func TestNumCtxEnvOverride(t *testing.T) {
	resetNumCtxCache(t)
	t.Setenv(numCtxEnvVar, "65536")

	o := NewOllama("http://127.0.0.1:1")
	if got := o.resolveNumCtx(context.Background(), "m:latest", 0); got != 65536 {
		t.Fatalf("num_ctx = %d, want 65536", got)
	}
}

func TestNumCtxEnvMaxLiftsTheCap(t *testing.T) {
	resetNumCtxCache(t)
	t.Setenv(numCtxEnvVar, "max")
	srv := showServer(t, map[string]any{
		"model_info": map[string]any{"qwen2.context_length": 131072},
	}, nil)

	o := NewOllama(srv.URL)
	if got := o.resolveNumCtx(context.Background(), "m:latest", 0); got != 131072 {
		t.Fatalf("num_ctx = %d, want the full 131072", got)
	}
}

func TestNumCtxEnvGarbageIsIgnored(t *testing.T) {
	resetNumCtxCache(t)
	// A typo in an env var must not take inference down.
	t.Setenv(numCtxEnvVar, "lots please")
	srv := showServer(t, map[string]any{
		"model_info": map[string]any{"qwen2.context_length": 16384},
	}, nil)

	o := NewOllama(srv.URL)
	if got := o.resolveNumCtx(context.Background(), "m:latest", 0); got != 16384 {
		t.Fatalf("num_ctx = %d, want detection to proceed (16384)", got)
	}
}

func TestResolveNumCtxCachesTheProbe(t *testing.T) {
	resetNumCtxCache(t)
	var hits int32
	srv := showServer(t, map[string]any{
		"model_info": map[string]any{"qwen2.context_length": 8192},
	}, &hits)

	o := NewOllama(srv.URL)
	for i := 0; i < 5; i++ {
		o.resolveNumCtx(context.Background(), "m:latest", 0)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("/api/show called %d times, want 1 — streaming would pay this per token", got)
	}
}

func TestBuildOptionsAlwaysCarriesNumCtx(t *testing.T) {
	resetNumCtxCache(t)
	srv := showServer(t, map[string]any{
		"model_info": map[string]any{"qwen2.context_length": 16384},
	}, nil)

	o := NewOllama(srv.URL)
	temp := 0.7
	opts := o.buildOptions(context.Background(), "m:latest", InferOptions{MaxTokens: 256, Temperature: &temp})

	if opts["num_ctx"] != 16384 {
		t.Fatalf("num_ctx = %v, want 16384", opts["num_ctx"])
	}
	if opts["num_predict"] != 256 {
		t.Fatalf("num_predict = %v, want 256", opts["num_predict"])
	}
	if opts["temperature"] != 0.7 {
		t.Fatalf("temperature = %v, want 0.7", opts["temperature"])
	}
}
