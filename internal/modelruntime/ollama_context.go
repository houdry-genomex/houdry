package modelruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Ollama's context window is not the model's context window.
//
// A request that omits `options.num_ctx` gets whatever the daemon defaults to —
// historically 2048, currently 4096 — regardless of what the model was trained
// to handle. deepseek-r1:1.5b will happily accept 131072 tokens, but a 4344
// token prompt against the default is rejected outright:
//
//	request (4344 tokens) exceeds the available context size (4096 tokens)
//
// which reads as "the model is too small" when it is really "we never asked for
// the room". Any agent transcript with a system prompt and a couple of tool
// results clears 4096 within two or three turns, so on a default daemon the
// fabric dies almost immediately.
//
// So we ask. For every ref we resolve the largest window the model actually
// supports and pass it on every request.

// A context window is memory: the KV cache scales linearly with num_ctx, and a
// model that *can* do 131072 will still fail to allocate it on a laptop GPU.
// There is no "unlimited" to ask for — so we take the model's full trained
// window up to a ceiling that fits commodity hardware, and let anyone with the
// VRAM raise or remove it.
//
// 32768 is chosen to be well past where agent transcripts actually live while
// staying inside what an 8 GB card can hold for a small model.
const defaultNumCtxCap = 32768

// Floor for when /api/show tells us nothing — an unreachable daemon, a runtime
// that isn't Ollama, GGUF metadata without a context_length key. Still four
// times the daemon default, so the failure mode of detection is a smaller
// window rather than the 4096 wall.
const fallbackNumCtx = 8192

// HOUDRY_OLLAMA_NUM_CTX overrides both detection and the cap:
//
//	HOUDRY_OLLAMA_NUM_CTX=65536   request exactly this many tokens
//	HOUDRY_OLLAMA_NUM_CTX=max     the model's full trained window, uncapped
//
// "max" is the escape hatch for a workstation with the VRAM to back it. It can
// still fail to allocate — that is Ollama's error to report, not ours to
// pre-empt, and a clear OOM beats a silently truncated window.
const numCtxEnvVar = "HOUDRY_OLLAMA_NUM_CTX"

// numCtxCache memoises /api/show per (base URL, ref). The answer is a property
// of the GGUF on disk, so it cannot change under a running daemon without a
// pull — and re-probing on every token of a stream would add a round-trip to
// the hot path for a value we already know.
type numCtxCache struct {
	mu     sync.Mutex
	byRef  map[string]int
	loaded map[string]bool
}

var sharedNumCtx = &numCtxCache{byRef: map[string]int{}, loaded: map[string]bool{}}

func (c *numCtxCache) get(key string) (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.byRef[key]
	return v, ok && c.loaded[key]
}

func (c *numCtxCache) put(key string, value int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byRef[key] = value
	c.loaded[key] = true
}

// parseNumCtxEnv reports the operator's override: a token count, or 0 with
// uncapped=true for "max". An unparseable value is ignored rather than fatal —
// a typo in an env var should not take inference down.
func parseNumCtxEnv() (value int, uncapped bool) {
	raw := strings.TrimSpace(os.Getenv(numCtxEnvVar))
	if raw == "" {
		return 0, false
	}
	if strings.EqualFold(raw, "max") || strings.EqualFold(raw, "unlimited") {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, false
}

// showContextLength asks the daemon what the model was trained for.
//
// The Modelfile's explicit `PARAMETER num_ctx` wins over GGUF metadata when
// present: someone who baked a window into their model meant it, and silently
// requesting a different one would make their Modelfile a lie.
func (o *Ollama) showContextLength(ctx context.Context, ref string) int {
	payload, _ := json.Marshal(map[string]any{"name": ref})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/api/show", strings.NewReader(string(payload)))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.shortClient().Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0
	}

	var parsed struct {
		ModelInfo  map[string]any `json:"model_info"`
		Parameters string         `json:"parameters"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return 0
	}

	for _, line := range strings.Split(parsed.Parameters, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "num_ctx" {
			if n, err := strconv.Atoi(fields[len(fields)-1]); err == nil && n > 0 {
				return n
			}
		}
	}

	// The key is architecture-prefixed ("qwen2.context_length",
	// "llama.context_length", …), so match on the suffix rather than
	// enumerating architectures we would have to keep chasing.
	for key, value := range parsed.ModelInfo {
		if !strings.HasSuffix(key, "context_length") {
			continue
		}
		if n, ok := value.(float64); ok && n > 0 {
			return int(n)
		}
	}
	return 0
}

// resolveNumCtx returns the window to request for ref.
//
// Precedence: an explicit per-request value, then the operator's env override,
// then the model's own maximum clamped to the cap, then the fallback floor.
// A caller who named a number gets that number unclamped — they are answering
// for their own hardware.
func (o *Ollama) resolveNumCtx(ctx context.Context, ref string, requested int) int {
	if requested > 0 {
		return requested
	}

	envValue, uncapped := parseNumCtxEnv()
	if envValue > 0 {
		return envValue
	}

	key := o.BaseURL + "|" + ref
	detected, cached := sharedNumCtx.get(key)
	if !cached {
		detected = o.showContextLength(ctx, ref)
		sharedNumCtx.put(key, detected)
	}

	if detected <= 0 {
		return fallbackNumCtx
	}
	if !uncapped && detected > defaultNumCtxCap {
		return defaultNumCtxCap
	}
	return detected
}

// buildOptions assembles the Ollama `options` object for a request.
//
// Every code path that talks to the daemon goes through here — chat, generate
// and the streaming variant all built this map by hand before, which is exactly
// how num_ctx came to be missing from all three at once.
func (o *Ollama) buildOptions(ctx context.Context, ref string, in InferOptions) map[string]any {
	opts := map[string]any{}
	if in.MaxTokens > 0 {
		opts["num_predict"] = in.MaxTokens
	}
	if in.Temperature != nil {
		opts["temperature"] = *in.Temperature
	}
	if n := o.resolveNumCtx(ctx, ref, in.NumCtx); n > 0 {
		opts["num_ctx"] = n
	}
	return opts
}
