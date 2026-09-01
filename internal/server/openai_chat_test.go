package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"houdry/internal/gpu"
	"houdry/internal/modelruntime"
	"houdry/internal/openaicompat"
	"houdry/internal/routing"
)

func startFakeInferenceWorker(t *testing.T, baseURL string, nodeID string) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			job, ok, err := ClaimJob(context.Background(), baseURL, "", nodeID)
			if err != nil || !ok {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			text := "Hello from Houdry OpenAI compat"
			result := map[string]any{
				"text":          text,
				"model":         job.Requirements.ModelIdentity().Ref(),
				"prompt_tokens": 11,
				"output_tokens": 7,
				"runtime":       "fake",
				"workload":      "inference",
				"finish_reason": "stop",
			}
			if job.Type == JobTypeInference {
				if p, _ := job.Payload["prompt"].(string); strings.Contains(strings.ToLower(p), "refactor") {
					result["text"] = "coding-model-reply"
				}
				if tools, ok := job.Payload["tools"].([]any); ok && len(tools) > 0 {
					result["text"] = ""
					result["finish_reason"] = "tool_calls"
					result["tool_calls"] = []map[string]any{{
						"id":   "call_test_1",
						"type": "function",
						"function": map[string]any{
							"name":      "execute_bash",
							"arguments": `{"command":"python fibonacci.py 10"}`,
						},
					}}
					result["output_tokens"] = 12
				}
			}
			_, _ = ReportJobResult(context.Background(), baseURL, "", job.ID, nodeID, true, result, "")
		}
	}()
	return cancel
}

func joinChatTestNode(t *testing.T, url, id string, models []modelruntime.Model) {
	t.Helper()
	inv := gpu.Inventory{
		NodeID: id, DetectedAt: time.Now().UTC(),
		Host: gpu.Host{Hostname: id, OS: "linux", Arch: "amd64"},
		GPUs: []gpu.GPU{{
			Index: 0, ID: "gpu-" + id, Vendor: gpu.VendorNVIDIA,
			Name: "RTX 2050", MemoryTotalBytes: 4 << 30, Source: "test",
		}},
	}
	_, err := JoinAgent(context.Background(), url, "", JoinRequest{
		Inventory:     inv,
		AgentVersion:  "test",
		Status:        StatusReady,
		Runtimes:      []string{"nvidia"},
		ModelRuntimes: []string{"ollama"},
		Models:        models,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestChatCompletionsAutoUsesRouter(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test", OpenAIWait: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	joinChatTestNode(t, ts.URL, "node-1", []modelruntime.Model{
		{Name: "tinyllama", Tag: "latest", Runtime: "ollama", State: modelruntime.StateAvailable},
		{Name: "qwen2.5-coder", Tag: "1.5b", Runtime: "ollama", State: modelruntime.StateAvailable},
	})
	stop := startFakeInferenceWorker(t, ts.URL, "node-1")
	defer stop()

	body := map[string]any{
		"model": "auto",
		"messages": []map[string]string{
			{"role": "user", "content": "Say hello from Houdry."},
		},
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, data)
	}
	var out openaicompat.ChatCompletionResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Object != openaicompat.ObjectChatCompletion || len(out.Choices) != 1 {
		t.Fatalf("%+v", out)
	}
	if out.Choices[0].Message.ContentString() == "" {
		t.Fatal("empty content")
	}
	if out.Usage.TotalTokens != 18 {
		t.Fatalf("usage=%+v", out.Usage)
	}
	// auto should resolve to a concrete model id in the response
	if out.Model == "auto" || out.Model == "" {
		t.Fatalf("expected resolved model, got %q", out.Model)
	}
	// Prefer tinyllama for simple greeting
	if !strings.Contains(out.Model, "tinyllama") {
		t.Fatalf("expected tinyllama for simple auto route, got %s", out.Model)
	}
}

func TestChatCompletionsExplicitModel(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test", OpenAIWait: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	joinChatTestNode(t, ts.URL, "node-1", []modelruntime.Model{
		{Name: "tinyllama", Tag: "latest", Runtime: "ollama", State: modelruntime.StateAvailable},
	})
	stop := startFakeInferenceWorker(t, ts.URL, "node-1")
	defer stop()

	body := `{"model":"tinyllama:latest","messages":[{"role":"user","content":"Hi"}]}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
	var out openaicompat.ChatCompletionResponse
	_ = json.Unmarshal(data, &out)
	if out.Model != "tinyllama:latest" {
		t.Fatalf("model=%s", out.Model)
	}
}

func TestChatCompletionsInvalidMessages(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"auto","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var errBody openaicompat.ErrorBody
	_ = json.NewDecoder(resp.Body).Decode(&errBody)
	if errBody.Error.Code != "invalid_messages" {
		t.Fatalf("%+v", errBody)
	}
}

func TestChatCompletionsUnavailableModel(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	// READY GPU node but no model runtime → cannot serve LLM inference.
	inv := gpu.Inventory{
		NodeID: "node-1", DetectedAt: time.Now().UTC(),
		Host: gpu.Host{Hostname: "n1", OS: "linux", Arch: "amd64"},
		GPUs: []gpu.GPU{{Index: 0, ID: "g1", Vendor: gpu.VendorNVIDIA, Name: "RTX 2050", MemoryTotalBytes: 4 << 30, Source: "test"}},
	}
	_, err = JoinAgent(context.Background(), ts.URL, "", JoinRequest{
		Inventory: inv, AgentVersion: "test", Status: StatusReady, Runtimes: []string{"nvidia"},
	})
	if err != nil {
		t.Fatal(err)
	}

	body := `{"model":"tinyllama:latest","messages":[{"role":"user","content":"Hi"}]}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 && resp.StatusCode != 503 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
}

func TestChatCompletionsNoREADYGPU(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), DisableLocalInference: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	body := `{"model":"auto","messages":[{"role":"user","content":"Hi"}]}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
}

func TestChatCompletionsDisabled(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), DisableOpenAICompat: true})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 && resp.StatusCode != 405 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestExistingAPIsUnaffected(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	resp, err = http.Get(ts.URL + "/v1/nodes")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
	resp, err = http.Get(ts.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.Status)
	}
}

func TestChatCompletionsGoesThroughRouteMetadata(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test", OpenAIWait: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	joinChatTestNode(t, ts.URL, "node-1", []modelruntime.Model{
		{Name: "tinyllama", Tag: "latest", Runtime: "ollama", State: modelruntime.StateAvailable},
	})
	stop := startFakeInferenceWorker(t, ts.URL, "node-1")
	defer stop()

	body := `{"model":"auto","messages":[{"role":"user","content":"Say hello"}]}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	jobs := s.jobs.List()
	if len(jobs) == 0 {
		t.Fatal("expected inference job")
	}
	j := jobs[len(jobs)-1]
	if j.Type != JobTypeInference {
		t.Fatalf("type=%s", j.Type)
	}
	route, _ := j.Payload["route"].(map[string]any)
	if route == nil || route["mode"] != "auto" {
		t.Fatalf("expected auto route metadata, got %+v", j.Payload)
	}
	if j.Payload["source"] != "openai.chat.completions" {
		t.Fatalf("source=%v", j.Payload["source"])
	}
}

func TestChatCompletionsAutoWithToolsSelectsCoder(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test", OpenAIWait: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	joinChatTestNode(t, ts.URL, "node-1", []modelruntime.Model{
		{Name: "tinyllama", Tag: "latest", Runtime: "ollama", State: modelruntime.StateLoaded},
		{Name: "qwen2.5-coder", Tag: "1.5b", Runtime: "ollama", State: modelruntime.StateAvailable},
	})
	stop := startFakeInferenceWorker(t, ts.URL, "node-1")
	defer stop()

	body := map[string]any{
		"model": "auto",
		"messages": []map[string]string{
			{"role": "user", "content": "Say hello from Houdry."},
		},
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "execute_bash",
				"description": "Run a command",
				"parameters":  map[string]any{"type": "object"},
			},
		}},
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, data)
	}
	var out openaicompat.ChatCompletionResponse
	_ = json.Unmarshal(data, &out)
	if !strings.Contains(out.Model, "qwen2.5-coder") {
		t.Fatalf("expected tool-capable coder for auto+tools, got %q", out.Model)
	}
}

func TestChatCompletionsExplicitTinyllamaWithToolsRejected(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	joinChatTestNode(t, ts.URL, "node-1", []modelruntime.Model{
		{Name: "tinyllama", Tag: "latest", Runtime: "ollama", State: modelruntime.StateAvailable},
	})

	body := `{"model":"tinyllama:latest","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"x","parameters":{}}}]}`
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
}

func TestChatCompletionsPreservesToolsAndReturnsToolCalls(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test", OpenAIWait: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	joinChatTestNode(t, ts.URL, "node-1", []modelruntime.Model{
		{Name: "qwen2.5-coder", Tag: "1.5b", Runtime: "ollama", State: modelruntime.StateAvailable},
	})
	stop := startFakeInferenceWorker(t, ts.URL, "node-1")
	defer stop()

	body := map[string]any{
		"model": "qwen2.5-coder:1.5b",
		"messages": []map[string]string{
			{"role": "user", "content": "Create fibonacci.py and run it."},
		},
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "execute_bash",
				"description": "Execute a bash command",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]string{"type": "string"},
					},
					"required": []string{"command"},
				},
			},
		}},
		"tool_choice": "auto",
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, data)
	}

	jobs := s.jobs.List()
	if len(jobs) == 0 {
		t.Fatal("expected job")
	}
	j := jobs[len(jobs)-1]
	tools, ok := j.Payload["tools"].([]openaicompat.Tool)
	if !ok || len(tools) != 1 || tools[0].Function.Name != "execute_bash" {
		// JSON round-trip via map may decode as []any depending on Create path.
		if rawTools, ok := j.Payload["tools"].([]any); !ok || len(rawTools) != 1 {
			t.Fatalf("tools not preserved in job payload: %#v", j.Payload["tools"])
		}
	}
	if j.Payload["tool_choice"] != "auto" {
		t.Fatalf("tool_choice=%v", j.Payload["tool_choice"])
	}
	if _, ok := j.Payload["messages"]; !ok {
		t.Fatal("messages not preserved in job payload")
	}

	var out openaicompat.ChatCompletionResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Choices) != 1 {
		t.Fatalf("%+v", out)
	}
	msg := out.Choices[0].Message
	if out.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish_reason=%s body=%s", out.Choices[0].FinishReason, data)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "execute_bash" {
		t.Fatalf("tool_calls=%+v body=%s", msg.ToolCalls, data)
	}
	if !strings.Contains(msg.ToolCalls[0].Function.Arguments, "fibonacci") {
		t.Fatalf("arguments=%s", msg.ToolCalls[0].Function.Arguments)
	}
}

func TestPickNodeModelSkipsUnfittable7b(t *testing.T) {
	n := Node{
		Inventory: gpu.Inventory{
			NodeID: "n1",
			GPUs: []gpu.GPU{{
				ID: "gpu-0", Vendor: gpu.VendorNVIDIA, Name: "RTX 2050", MemoryTotalBytes: 4 << 30,
			}},
		},
		Status: StatusBusy,
		Resources: ResourceProfile{
			Static:  StaticResources{GPUs: []StaticGPU{{ID: "gpu-0", MemoryTotalBytes: 4 << 30}}},
			Dynamic: DynamicResources{GPUs: []DynamicGPU{{ID: "gpu-0", MemoryAvailableBytes: 4 << 30}}},
		},
		Models: []modelruntime.Model{
			{Name: "qwen2.5-coder", Tag: "7b", Runtime: "ollama", SizeBytes: 4683087561},
			{Name: "qwen2.5-coder", Tag: "1.5b", Runtime: "ollama", SizeBytes: 986062089},
		},
	}
	name, tag := pickNodeModel(n, true, routing.DefaultCatalog())
	if name != "qwen2.5-coder" || tag != "1.5b" {
		t.Fatalf("got %s:%s, want qwen2.5-coder:1.5b", name, tag)
	}
}

func TestChatCompletionsBusyNodePicksFittingCoderNot7b(t *testing.T) {
	s, err := New(Options{DataDir: t.TempDir(), Version: "test", OpenAIWait: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()

	joinChatTestNode(t, ts.URL, "node-1", []modelruntime.Model{
		{Name: "qwen2.5-coder", Tag: "7b", Runtime: "ollama", State: modelruntime.StateAvailable, SizeBytes: 4683087561},
		{Name: "qwen2.5-coder", Tag: "1.5b", Runtime: "ollama", State: modelruntime.StateAvailable, SizeBytes: 986062089},
		{Name: "tinyllama", Tag: "latest", Runtime: "ollama", State: modelruntime.StateAvailable},
	})
	s.store.SetStatus("node-1", StatusBusy, "job-other")
	stop := startFakeInferenceWorker(t, ts.URL, "node-1")
	defer stop()
	go func() {
		time.Sleep(80 * time.Millisecond)
		s.store.SetStatus("node-1", StatusReady, "")
		s.tryScheduleQueued()
	}()

	body := map[string]any{
		"model": "auto",
		"messages": []map[string]string{
			{"role": "user", "content": "what is the capital of france?"},
		},
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "execute_bash",
				"description": "Run a command",
				"parameters":  map[string]any{"type": "object"},
			},
		}},
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+"/v1/chat/completions", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, data)
	}
	var found string
	for _, j := range s.jobs.List() {
		if ref := j.Requirements.ModelIdentity().Ref(); strings.Contains(ref, "qwen2.5-coder") {
			found = ref
		}
	}
	if found != "qwen2.5-coder:1.5b" {
		t.Fatalf("expected 1.5b on busy 4GiB node, got %q", found)
	}
}
