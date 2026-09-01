package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"houdry/internal/modelruntime"
	"houdry/internal/openaicompat"
	"houdry/internal/routerchat"
	"houdry/internal/routeropenai"
	"houdry/internal/routing"
)

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		openaicompat.WriteError(w, http.StatusBadRequest, "invalid_request_error", "bad_request", "failed to read body")
		return
	}
	var req openaicompat.ChatCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		openaicompat.WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_json", "invalid json: "+err.Error())
		return
	}
	if err := openaicompat.ValidateChatRequest(req); err != nil {
		openaicompat.WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_messages", err.Error())
		return
	}

	// Local Ollama only when there is no GPU worker at all. A BUSY node used
	// to look like "no READY GPU" and leaked the next Agent turn into
	// routerchat (7b OOM, tinyllama retry) while the worker was still busy.
	if !s.hasGPUWorker() && s.handleLocalChat(w, r, req) {
		return
	}

	prompt := openaicompat.MessagesToPrompt(req.Messages)
	routeSignal := openaicompat.LastUserText(req.Messages)

	var (
		modelName string
		modelTag  string
		runtime   string
		nodeID    string
		minVRAM   uint64
		routeMeta map[string]any
	)

	if openaicompat.IsAutoModel(req.Model) {
		decision := s.routePromptOpts(routeSignal, "", false, len(req.Tools) > 0, true)
		if decision.Deferred {
			openaicompat.WriteError(w, http.StatusBadRequest, "invalid_request_error", "unsupported_modality",
				"request modality is not supported by the current Houdry pipeline")
			return
		}
		if decision.Selected == nil {
			n, ok := s.firstGPUWorker()
			name, tag := "", ""
			catalog, err := s.loadCatalog()
			if err != nil {
				catalog = routing.DefaultCatalog()
			}
			if ok {
				name, tag = pickNodeModel(n, len(req.Tools) > 0, catalog)
			}
			if name == "" {
				msg := "no suitable model or GPU node is currently available"
				if len(req.Tools) > 0 {
					msg = "no tool-capable model is available that fits this GPU (e.g. install qwen2.5-coder:1.5b)"
				}
				openaicompat.WriteError(w, http.StatusServiceUnavailable, "server_error", "no_suitable_node", msg)
				return
			}
			modelName, modelTag = name, tag
			nodeID = n.NodeID
			minVRAM = catalogMinVRAM(catalog, n, name, tag)
			routeMeta = map[string]any{
				"mode":    "auto",
				"queued":  true,
				"catalog": modelruntime.ParseRef(name + ":" + tag).Ref(),
				"tools":   len(req.Tools) > 0,
			}
		} else {
			sel := decision.Selected
			modelName = sel.Entry.Name
			modelTag = sel.Entry.Tag
			runtime = sel.Entry.Runtime
			nodeID = sel.NodeID
			minVRAM = sel.Entry.MinVRAMBytes
			routeMeta = map[string]any{
				"mode":       "auto",
				"modality":   decision.Profile.Modality,
				"complexity": decision.Profile.Complexity,
				"score":      sel.Score,
				"reasons":    sel.Reasons,
				"catalog":    sel.Entry.Ref(),
				"tools":      len(req.Tools) > 0,
			}
		}
	} else {
		id := modelruntime.ParseRef(req.Model)
		modelName, modelTag = id.Name, id.Tag
		if modelName == "" {
			openaicompat.WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_model", "model is required")
			return
		}
		if len(req.Tools) > 0 && !routing.EntrySupportsTools(routing.CatalogEntry{Name: modelName, Tag: modelTag}) {
			openaicompat.WriteError(w, http.StatusBadRequest, "invalid_request_error", "model_no_tools",
				fmt.Sprintf("model %q does not support tools; use a tool-capable model (e.g. qwen2.5-coder:1.5b) or model=auto", req.Model))
			return
		}
		jobReq := Requirements{
			GPURequired:  true,
			ModelName:    modelName,
			ModelTag:     modelTag,
			ModelRuntime: runtime,
		}
		jobReq.NormalizeModelFields()
		// Prefer nodes that already have the model; still use Houdry Fits/selectNode.
		nodeID = s.selectNode(s.store.List(), Job{Requirements: jobReq})
		if nodeID == "" {
			// Distinguish "model unknown to catalog/nodes" vs "no READY GPU".
			if !s.anyREADYNode() {
				openaicompat.WriteError(w, http.StatusServiceUnavailable, "server_error", "no_suitable_node",
					"no READY GPU node is currently available")
				return
			}
			openaicompat.WriteError(w, http.StatusNotFound, "invalid_request_error", "model_unavailable",
				"requested model is not available on any READY node with a compatible runtime")
			return
		}
		routeMeta = map[string]any{
			"mode":  "explicit",
			"model": jobReq.ModelIdentity().Ref(),
		}
	}

	jobReq := Requirements{
		GPURequired:  true,
		MinVRAMBytes: minVRAM,
		ModelName:    modelName,
		ModelTag:     modelTag,
		ModelRuntime: runtime,
	}
	jobReq.NormalizeModelFields()
	payload := map[string]any{
		"prompt":   prompt,
		"model":    jobReq.ModelIdentity().Ref(),
		"route":    routeMeta,
		"source":   "openai.chat.completions",
		"messages": openaicompat.MessagesToRuntime(req.Messages),
	}
	if len(req.Tools) > 0 {
		payload["tools"] = req.Tools
	}
	if len(req.ToolChoice) > 0 && string(req.ToolChoice) != "null" {
		var tc any
		if err := json.Unmarshal(req.ToolChoice, &tc); err == nil {
			payload["tool_choice"] = tc
		}
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 && *req.MaxTokens <= 1024 {
		payload["max_tokens"] = *req.MaxTokens
	} else if req.MaxTokens == nil && len(req.Tools) > 0 {
		// Tool-using agents need more headroom than short greeting replies.
		payload["max_tokens"] = 256
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}

	j, err := s.jobs.Create(JobTypeInference, nodeID, jobReq, payload)
	if err != nil {
		openaicompat.WriteError(w, http.StatusBadRequest, "invalid_request_error", "job_create_failed", "unable to create inference job")
		return
	}
	s.tryScheduleQueued()
	if updated, ok := s.jobs.Get(j.ID); ok {
		j = updated
	}
	if j.Status == JobQueued {
		// Still waiting for assignment — usually means node became busy; keep waiting.
	}

	timeout := s.opts.OpenAIWait
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	done, err := s.WaitJob(ctx, j.ID)
	if err != nil {
		if ctx.Err() != nil {
			openaicompat.WriteError(w, http.StatusGatewayTimeout, "timeout_error", "job_timeout",
				"inference job did not complete in time (it may still be queued or running)")
			return
		}
		openaicompat.WriteError(w, http.StatusInternalServerError, "server_error", "wait_failed", "failed while waiting for inference")
		return
	}
	if done.Status == JobFailed {
		msg := done.Error
		if msg == "" {
			msg = "inference runtime failed"
		}
		// Keep message high-level — no paths/credentials.
		openaicompat.WriteError(w, http.StatusBadGateway, "server_error", "runtime_error", msg)
		return
	}

	text := ""
	promptTokens, completionTokens := 0, 0
	resolvedModel := jobReq.ModelIdentity().Ref()
	var toolCalls []openaicompat.ToolCall
	finishReason := "stop"
	if done.Result != nil {
		if t, ok := done.Result["text"].(string); ok {
			text = t
		}
		promptTokens = asInt(done.Result["prompt_tokens"])
		completionTokens = asInt(done.Result["output_tokens"])
		if m, ok := done.Result["model"].(string); ok && m != "" {
			resolvedModel = m
		}
		if fr, ok := done.Result["finish_reason"].(string); ok && fr != "" {
			finishReason = fr
		}
		toolCalls = parseToolCalls(done.Result["tool_calls"])
		if len(toolCalls) > 0 {
			finishReason = "tool_calls"
		}
	}
	if text == "" && len(toolCalls) == 0 {
		openaicompat.WriteError(w, http.StatusBadGateway, "server_error", "empty_response", "runtime returned an empty response")
		return
	}

	id := chatCompletionID()
	displayModel := req.Model
	if openaicompat.IsAutoModel(displayModel) {
		displayModel = resolvedModel
	}

	if req.Stream {
		_ = openaicompat.WriteSSECompletionFull(w, id, displayModel, text, toolCalls)
		return
	}
	resp := openaicompat.BuildCompletionFull(id, displayModel, text, toolCalls, finishReason, promptTokens, completionTokens)
	openaicompat.WriteJSON(w, http.StatusOK, resp)
}

func (s *Server) handleOpenAIModels(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	created := time.Now().Unix()
	list := openaicompat.ModelsList{
		Object: openaicompat.ObjectList,
		Data: []openaicompat.ModelCard{{
			ID: "auto", Object: openaicompat.ObjectModel, Created: created, OwnedBy: "houdry",
		}},
	}
	seen := map[string]bool{"auto": true}
	if !s.anyREADYNode() {
		if catalog := s.localCatalog(r.Context()); len(catalog) > 0 {
			for _, e := range catalog {
				id := e.Ref()
				if seen[id] {
					continue
				}
				seen[id] = true
				list.Data = append(list.Data, openaicompat.ModelCard{
					ID: id, Object: openaicompat.ObjectModel, Created: created, OwnedBy: "houdry",
				})
			}
			openaicompat.WriteJSON(w, http.StatusOK, list)
			return
		}
	}
	catalog, err := s.loadCatalog()
	if err != nil {
		catalog = routing.DefaultCatalog()
	}
	for _, e := range catalog {
		id := e.Ref()
		if seen[id] {
			continue
		}
		seen[id] = true
		list.Data = append(list.Data, openaicompat.ModelCard{
			ID: id, Object: openaicompat.ObjectModel, Created: created, OwnedBy: "houdry",
		})
	}
	openaicompat.WriteJSON(w, http.StatusOK, list)
}

func (s *Server) anyREADYNode() bool {
	for _, n := range s.store.List() {
		if n.Status == StatusReady {
			return true
		}
	}
	return false
}

func (s *Server) hasGPUWorker() bool {
	_, ok := s.firstGPUWorker()
	return ok
}

func (s *Server) firstGPUWorker() (Node, bool) {
	for _, n := range s.store.List() {
		if n.AgentVersion == "" {
			continue
		}
		switch n.Status {
		case StatusReady, StatusBusy, StatusDraining:
			return n, true
		}
	}
	return Node{}, false
}

func pickNodeModel(n Node, tools bool, catalog []routing.CatalogEntry) (name, tag string) {
	views := nodesToViews([]Node{n})
	if len(views) == 0 {
		return "", ""
	}
	return routing.PickFittingModel(views[0], tools, catalog)
}

func catalogMinVRAM(catalog []routing.CatalogEntry, n Node, name, tag string) uint64 {
	for _, e := range catalog {
		if strings.EqualFold(e.Name, name) && strings.EqualFold(e.Tag, tag) {
			return e.MinVRAMBytes
		}
	}
	for _, m := range n.Models {
		if strings.EqualFold(m.Name, name) && strings.EqualFold(m.Tag, tag) {
			return m.SizeBytes
		}
	}
	return 0
}

func localOllamaURL() string {
	if u := strings.TrimSpace(os.Getenv("HOUDRY_OLLAMA")); u != "" {
		return u
	}
	return "http://127.0.0.1:11434"
}

// handleLocalChat serves /v1 chat through loopback Ollama. Returns false when
// the daemon is not reachable so the caller can fall through to the job-path
// 503.
func (s *Server) handleLocalChat(w http.ResponseWriter, r *http.Request, req openaicompat.ChatCompletionRequest) bool {
	if s.opts.DisableLocalInference {
		return false
	}
	lo := routerchat.NewLocalOllama(localOllamaURL())
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if _, _, err := lo.Nodes(ctx); err != nil {
		return false
	}
	filesDir := s.generatedDir()
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return false
	}
	routeropenai.ServeChat(w, r, routerchat.New(lo, lo), filesDir, req)
	return true
}

func (s *Server) localCatalog(ctx context.Context) []routing.CatalogEntry {
	if s.opts.DisableLocalInference {
		return nil
	}
	lo := routerchat.NewLocalOllama(localOllamaURL())
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, catalog, err := lo.Nodes(probe)
	if err != nil {
		return nil
	}
	return catalog
}

func chatCompletionID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("chatcmpl-%x", b)
}

func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func parseToolCalls(v any) []openaicompat.ToolCall {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var calls []openaicompat.ToolCall
	if err := json.Unmarshal(raw, &calls); err != nil {
		return nil
	}
	for i := range calls {
		if calls[i].Type == "" {
			calls[i].Type = "function"
		}
		if calls[i].ID == "" {
			calls[i].ID = fmt.Sprintf("call_%d", i)
		}
	}
	return calls
}
