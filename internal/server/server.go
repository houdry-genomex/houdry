package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"houdry/internal/gpu"
	"houdry/internal/host"
	"houdry/internal/modelruntime"
	"houdry/internal/version"
)

type Options struct {
	DataDir               string
	BinariesDir           string
	Token                 string
	Version               string
	DisableOpenAICompat   bool          // when true, /v1/chat/completions is not registered
	DisableLocalInference bool          // tests: never fall back to loopback Ollama
	OpenAIWait            time.Duration // max wait for chat completion inference
}

// JoinRequest is the body for join and heartbeat. Phase 1 clients send only
// inventory fields; agents also send agent_version, status, host resources,
// and available runtimes.
type JoinRequest struct {
	gpu.Inventory
	AgentVersion  string               `json:"agent_version,omitempty"`
	Status        string               `json:"status,omitempty"`
	CurrentJobID  string               `json:"current_job_id,omitempty"`
	HostResources host.Resources       `json:"host_resources,omitempty"`
	Runtimes      []string             `json:"runtimes,omitempty"` // GPU runtimes
	ModelRuntimes []string             `json:"model_runtimes,omitempty"`
	Models        []modelruntime.Model `json:"models,omitempty"`
}

type Server struct {
	opts      Options
	store     *Store
	jobs      *JobStore
	mux       *http.ServeMux
	stopSweep chan struct{}
}

func New(opts Options) (*Server, error) {
	if opts.Version == "" {
		opts.Version = version.Version
	}
	if opts.OpenAIWait <= 0 {
		opts.OpenAIWait = 10 * time.Minute
	}
	st, err := NewStore(opts.DataDir)
	if err != nil {
		return nil, err
	}
	js, err := NewJobStore(opts.DataDir)
	if err != nil {
		return nil, err
	}
	s := &Server{
		opts:      opts,
		store:     st,
		jobs:      js,
		mux:       http.NewServeMux(),
		stopSweep: make(chan struct{}),
	}
	s.routes()
	go s.sweepOffline()
	return s, nil
}

func ListenAndServe(addr string, opts Options) error {
	s, err := New(opts)
	if err != nil {
		return err
	}
	return http.ListenAndServe(addr, s)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) Close() {
	select {
	case <-s.stopSweep:
	default:
		close(s.stopSweep)
	}
}

func (s *Server) generatedDir() string {
	return filepath.Join(s.opts.DataDir, "generated")
}

func (s *Server) sweepOffline() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stopSweep:
			return
		case <-t.C:
			for _, id := range s.store.MarkStaleOffline() {
				s.jobs.FailRunningForNode(id, "node heartbeat timeout")
			}
			s.tryScheduleQueued()
		}
	}
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /.well-known/houdry.json", s.handleWellKnown)
	s.mux.HandleFunc("GET /", s.handleDashboard)
	s.mux.HandleFunc("GET /v1/cluster", s.handleCluster)
	s.mux.HandleFunc("GET /v1/nodes", s.handleListNodes)
	s.mux.HandleFunc("POST /v1/nodes/join", s.handleJoin)
	s.mux.HandleFunc("POST /v1/nodes/heartbeat", s.handleHeartbeat)
	s.mux.HandleFunc("POST /v1/nodes/drain", s.handleDrain)
	s.mux.HandleFunc("POST /v1/nodes/leave", s.handleLeave)
	s.mux.HandleFunc("GET /v1/jobs", s.handleListJobs)
	s.mux.HandleFunc("POST /v1/jobs", s.handleSubmitJob)
	s.mux.HandleFunc("GET /v1/jobs/{id}", s.handleGetJob)
	s.mux.HandleFunc("POST /v1/jobs/claim", s.handleClaimJob)
	s.mux.HandleFunc("POST /v1/jobs/{id}/result", s.handleJobResult)
	s.mux.HandleFunc("GET /v1/catalog", s.handleCatalog)
	s.mux.HandleFunc("POST /v1/route", s.handleRoute)
	if !s.opts.DisableOpenAICompat {
		s.mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
		s.mux.HandleFunc("GET /v1/models", s.handleOpenAIModels)
	}
	filesDir := s.generatedDir()
	_ = os.MkdirAll(filesDir, 0o755)
	s.mux.Handle("GET /files/", http.StripPrefix("/files/", http.FileServer(http.Dir(filesDir))))
	s.mux.HandleFunc("GET /install.sh", s.handleInstallSH)
	s.mux.HandleFunc("GET /install.ps1", s.handleInstallPS1)
	s.mux.HandleFunc("GET /download/{os}/{arch}", s.handleDownload)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.opts.Version})
}

func (s *Server) handleWellKnown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"houdry":  "control-plane",
		"v":       1,
		"version": s.opts.Version,
		"path":    "/v1",
		"openai":  !s.opts.DisableOpenAICompat,
		"auth":    s.opts.Token != "",
	})
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	req, ok := readJoinRequest(w, r)
	if !ok {
		return
	}
	n := Node{
		Inventory:     req.Inventory,
		AgentVersion:  req.AgentVersion,
		Status:        req.Status,
		RemoteIP:      clientIP(r),
		Runtimes:      req.Runtimes,
		ModelRuntimes: req.ModelRuntimes,
		Models:        req.Models,
	}
	hostRes := req.HostResources
	if hostRes.CPUCores == 0 {
		hostRes = defaultHostFromInventory(req.Inventory)
	}
	enrichNode(&n, hostRes, req.Runtimes)
	if n.Status == "" {
		if n.AgentVersion != "" {
			n.Status = StatusReady
		} else {
			n.Status = StatusJoined
		}
	}
	n = s.store.Upsert(n)
	s.jobs.FailRunningExcept(n.NodeID, req.CurrentJobID, "worker restarted")
	s.tryScheduleQueued()
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	req, ok := readJoinRequest(w, r)
	if !ok {
		return
	}
	n := Node{
		Inventory:     req.Inventory,
		AgentVersion:  req.AgentVersion,
		Status:        req.Status,
		CurrentJobID:  req.CurrentJobID,
		RemoteIP:      clientIP(r),
		Runtimes:      req.Runtimes,
		ModelRuntimes: req.ModelRuntimes,
		Models:        req.Models,
	}
	hostRes := req.HostResources
	if hostRes.CPUCores == 0 {
		hostRes = defaultHostFromInventory(req.Inventory)
	}
	enrichNode(&n, hostRes, req.Runtimes)
	out, found := s.store.Heartbeat(n)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "node not registered; call /v1/nodes/join first"})
		return
	}
	// Idle heartbeats (empty CurrentJobID) must not fail pending jobs —
	// those are assigned and waiting for the next claim tick.
	s.jobs.FailRunningExcept(out.NodeID, req.CurrentJobID, "worker is no longer running this job")
	if out.Status == StatusReady {
		s.tryScheduleQueued()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDrain(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	id, ok := readNodeID(w, r)
	if !ok {
		return
	}
	n, found := s.store.SetDrain(id)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "node not registered"})
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) handleLeave(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	id, ok := readNodeID(w, r)
	if !ok {
		return
	}
	n, found := s.store.Get(id)
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "node not registered"})
		return
	}
	if n.Status == StatusBusy || (n.Status == StatusDraining && n.CurrentJobID != "") {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "node still has an active job; drain and wait for completion",
		})
		return
	}
	s.jobs.FailRunningForNode(id, "node left cluster")
	s.store.SetStatus(id, StatusOffline, "")
	// Remove from registry after leave so it no longer appears in scheduling pool.
	s.store.Remove(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "node_id": id, "removed": true})
}

func (s *Server) handleCluster(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	nodes := s.store.List()
	writeJSON(w, http.StatusOK, map[string]any{
		"summary": SummarizeCluster(nodes, s.jobs.CountByStatus(JobQueued)),
		"nodes":   nodes,
	})
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.store.List())
}

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	catalog, err := s.loadCatalog()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": catalog})
}

func (s *Server) handleRoute(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body"})
		return
	}
	var req struct {
		Prompt          string `json:"prompt"`
		Runtime         string `json:"runtime,omitempty"`
		RequirePresent  bool   `json:"require_model_present,omitempty"`
		Execute         bool   `json:"execute,omitempty"`
		PreferredNodeID string `json:"node_id,omitempty"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "prompt is required"})
		return
	}
	decision := s.routePrompt(req.Prompt, req.Runtime, req.RequirePresent)
	if req.PreferredNodeID != "" && decision.Selected != nil {
		decision.Selected.NodeID = req.PreferredNodeID
	}

	resp := map[string]any{"decision": decision}
	if !req.Execute {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if decision.Deferred || decision.Selected == nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    "cannot execute: no routable decision",
			"decision": decision,
		})
		return
	}
	sel := decision.Selected
	jobReq := Requirements{
		GPURequired:         true,
		MinVRAMBytes:        sel.Entry.MinVRAMBytes,
		ModelName:           sel.Entry.Name,
		ModelTag:            sel.Entry.Tag,
		ModelRuntime:        sel.Entry.Runtime,
		RequireModelPresent: req.RequirePresent,
	}
	if req.Runtime != "" {
		jobReq.ModelRuntime = req.Runtime
	}
	jobReq.NormalizeModelFields()
	j, err := s.jobs.Create(JobTypeInference, sel.NodeID, jobReq, map[string]any{
		"prompt": req.Prompt,
		"model":  sel.Entry.Ref(),
		"route": map[string]any{
			"modality":   decision.Profile.Modality,
			"complexity": decision.Profile.Complexity,
			"score":      sel.Score,
			"reasons":    sel.Reasons,
		},
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.tryScheduleQueued()
	if updated, ok := s.jobs.Get(j.ID); ok {
		j = updated
	}
	resp["job"] = j
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSubmitJob(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body"})
		return
	}
	var req struct {
		Type         string         `json:"type"`
		NodeID       string         `json:"node_id,omitempty"`
		Requirements Requirements   `json:"requirements"`
		Payload      map[string]any `json:"payload,omitempty"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
		return
	}
	if req.Type == "" {
		req.Type = JobTypeGPUSmoke
	}
	if req.NodeID != "" {
		if _, ok := s.store.Get(req.NodeID); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown node_id"})
			return
		}
	}
	j, err := s.jobs.Create(req.Type, req.NodeID, req.Requirements, req.Payload)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.tryScheduleQueued()
	if updated, ok := s.jobs.Get(j.ID); ok {
		j = updated
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.jobs.List())
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	j, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) handleClaimJob(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body"})
		return
	}
	var req struct {
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.NodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})
		return
	}
	node, ok := s.store.Get(req.NodeID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "node not registered"})
		return
	}
	if node.Status != StatusReady {
		// DRAINING / BUSY / OFFLINE / JOINED cannot take new work.
		writeJSON(w, http.StatusNoContent, nil)
		return
	}
	j, ok := s.jobs.Claim(req.NodeID)
	if !ok {
		writeJSON(w, http.StatusNoContent, nil)
		return
	}
	s.store.SetStatus(req.NodeID, StatusBusy, j.ID)
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) handleJobResult(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body"})
		return
	}
	var req struct {
		OK     bool           `json:"ok"`
		Result map[string]any `json:"result,omitempty"`
		Error  string         `json:"error,omitempty"`
		NodeID string         `json:"node_id,omitempty"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
		return
	}
	id := r.PathValue("id")
	j, ok := s.jobs.Complete(id, req.OK, req.Result, req.Error)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}
	nodeID := req.NodeID
	if nodeID == "" {
		nodeID = j.NodeID
	}
	if nodeID != "" {
		s.store.SetStatus(nodeID, StatusReady, "")
	}
	s.tryScheduleQueued()
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	tmpl, err := template.New("dashboard").Funcs(template.FuncMap{
		"bytes": formatBytes,
		"pct": func(v *int) string {
			if v == nil {
				return "—"
			}
			return fmt.Sprintf("%d%%", *v)
		},
		"temp": func(v *int) string {
			if v == nil {
				return "—"
			}
			return fmt.Sprintf("%d°C", *v)
		},
		"time": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.Local().Format("2006-01-02 15:04:05")
		},
		"statusClass": func(st string) string {
			switch st {
			case StatusReady:
				return "ready"
			case StatusBusy:
				return "busy"
			case StatusDraining:
				return "draining"
			case StatusOffline:
				return "offline"
			default:
				return "joined"
			}
		},
	}).Parse(dashboardHTML)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	nodes := s.store.List()
	summary := SummarizeCluster(nodes, s.jobs.CountByStatus(JobQueued))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(w, map[string]any{
		"Version": s.opts.Version,
		"Nodes":   nodes,
		"Jobs":    s.jobs.List(),
		"Server":  publicURL(r),
		"Summary": summary,
	})
}

func (s *Server) handleInstallSH(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_ = installSHTmpl.Execute(w, installData{Server: publicURL(r), Version: s.opts.Version})
}

func (s *Server) handleInstallPS1(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_ = installPS1Tmpl.Execute(w, installData{Server: publicURL(r), Version: s.opts.Version})
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	osName := r.PathValue("os")
	arch := r.PathValue("arch")
	osName = normalizeOS(osName)
	arch = normalizeArch(arch)
	if osName == "" || arch == "" {
		http.Error(w, "unsupported os/arch", http.StatusNotFound)
		return
	}

	name := "houdry-" + osName + "-" + arch
	if osName == "windows" {
		name += ".exe"
	}
	candidates := []string{
		filepath.Join(s.opts.BinariesDir, name),
	}
	if osName == runtime.GOOS && arch == runtime.GOARCH {
		if self, err := os.Executable(); err == nil {
			candidates = append(candidates, self)
		}
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="`+downloadFilename(osName)+`"`)
			http.ServeFile(w, r, p)
			return
		}
	}
	http.Error(w, fmt.Sprintf("binary for %s/%s is not available on this server; build with: make dist", osName, arch), http.StatusNotFound)
}

func downloadFilename(osName string) string {
	if osName == "windows" {
		return "houdry.exe"
	}
	return "houdry"
}

func readJoinRequest(w http.ResponseWriter, r *http.Request) (JoinRequest, bool) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body"})
		return JoinRequest{}, false
	}
	var req JoinRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
		return JoinRequest{}, false
	}
	if req.NodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})
		return JoinRequest{}, false
	}
	if req.DetectedAt.IsZero() {
		req.DetectedAt = time.Now().UTC()
	}
	return req, true
}

func readNodeID(w http.ResponseWriter, r *http.Request) (string, bool) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body"})
		return "", false
	}
	var req struct {
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.NodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})
		return "", false
	}
	return req.NodeID, true
}

func (s *Server) authorized(w http.ResponseWriter, r *http.Request) bool {
	if s.opts.Token == "" {
		return true
	}
	got := r.Header.Get("X-Houdry-Token")
	if got == "" {
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
			got = strings.TrimPrefix(a, "Bearer ")
		}
	}
	if got != s.opts.Token {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return false
	}
	return true
}

func publicURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	return scheme + "://" + r.Host
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status == http.StatusNoContent || v == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func normalizeOS(osName string) string {
	switch strings.ToLower(osName) {
	case "linux":
		return "linux"
	case "darwin", "macos", "osx":
		return "darwin"
	case "windows", "win":
		return "windows"
	default:
		return ""
	}
}

func normalizeArch(arch string) string {
	switch strings.ToLower(arch) {
	case "amd64", "x86_64", "x64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	default:
		return ""
	}
}

func formatBytes(n uint64) string {
	if n == 0 {
		return "—"
	}
	const gi = 1024 * 1024 * 1024
	const mi = 1024 * 1024
	if n >= gi {
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(gi))
	}
	if n >= mi {
		return fmt.Sprintf("%.0f MiB", float64(n)/float64(mi))
	}
	return fmt.Sprintf("%d B", n)
}

type installData struct {
	Server  string
	Version string
}

// --- HTTP client helpers (used by CLI and node agent) ---

func Join(ctx context.Context, serverURL, token string, inv gpu.Inventory) (map[string]any, error) {
	return postJSON(ctx, serverURL, token, "/v1/nodes/join", inv)
}

func JoinAgent(ctx context.Context, serverURL, token string, req JoinRequest) (Node, error) {
	var out Node
	if err := postJSONInto(ctx, serverURL, token, "/v1/nodes/join", req, &out); err != nil {
		return Node{}, err
	}
	return out, nil
}

func Heartbeat(ctx context.Context, serverURL, token string, req JoinRequest) (Node, error) {
	var out Node
	if err := postJSONInto(ctx, serverURL, token, "/v1/nodes/heartbeat", req, &out); err != nil {
		return Node{}, err
	}
	return out, nil
}

func ListNodes(ctx context.Context, serverURL, token string) ([]Node, error) {
	var nodes []Node
	if err := getJSON(ctx, serverURL, token, "/v1/nodes", &nodes); err != nil {
		return nil, err
	}
	return nodes, nil
}

func SubmitJob(ctx context.Context, serverURL, token, jobType, nodeID string) (Job, error) {
	return SubmitJobFull(ctx, serverURL, token, jobType, nodeID, Requirements{}, nil)
}

func SubmitJobWithRequirements(ctx context.Context, serverURL, token, jobType, nodeID string, req Requirements) (Job, error) {
	return SubmitJobFull(ctx, serverURL, token, jobType, nodeID, req, nil)
}

func SubmitInference(ctx context.Context, serverURL, token, model, prompt, nodeID string, req Requirements) (Job, error) {
	if req.Model == "" && req.ModelName == "" {
		req.Model = model
	}
	req.NormalizeModelFields()
	if !req.GPURequired && req.MinVRAMBytes == 0 {
		req.GPURequired = true
	}
	return SubmitJobFull(ctx, serverURL, token, JobTypeInference, nodeID, req, map[string]any{
		"prompt": prompt,
		"model":  req.ModelIdentity().Ref(),
	})
}

func SubmitJobFull(ctx context.Context, serverURL, token, jobType, nodeID string, req Requirements, payload map[string]any) (Job, error) {
	req.NormalizeModelFields()
	var out Job
	body := map[string]any{"type": jobType}
	if nodeID != "" {
		body["node_id"] = nodeID
	}
	id := req.ModelIdentity()
	if req.GPURequired || req.MinVRAMBytes > 0 || id.Name != "" || req.ModelRuntime != "" {
		body["requirements"] = req
	}
	if payload != nil {
		body["payload"] = payload
	}
	if err := postJSONInto(ctx, serverURL, token, "/v1/jobs", body, &out); err != nil {
		return Job{}, err
	}
	return out, nil
}

// RouteResponse is the control-plane routing decision (+ optional job).
type RouteResponse struct {
	Decision map[string]any `json:"decision"`
	Job      *Job           `json:"job,omitempty"`
	Error    string         `json:"error,omitempty"`
}

func GetCatalog(ctx context.Context, serverURL, token string) ([]map[string]any, error) {
	var resp struct {
		Models []map[string]any `json:"models"`
	}
	if err := getJSON(ctx, serverURL, token, "/v1/catalog", &resp); err != nil {
		return nil, err
	}
	return resp.Models, nil
}

func Route(ctx context.Context, serverURL, token, prompt, runtime string, requirePresent, execute bool) (map[string]any, error) {
	var out map[string]any
	body := map[string]any{
		"prompt":                prompt,
		"execute":               execute,
		"require_model_present": requirePresent,
	}
	if runtime != "" {
		body["runtime"] = runtime
	}
	if err := postJSONInto(ctx, serverURL, token, "/v1/route", body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func DrainNode(ctx context.Context, serverURL, token, nodeID string) (Node, error) {
	var out Node
	if err := postJSONInto(ctx, serverURL, token, "/v1/nodes/drain", map[string]string{"node_id": nodeID}, &out); err != nil {
		return Node{}, err
	}
	return out, nil
}

func LeaveNode(ctx context.Context, serverURL, token, nodeID string) error {
	return postJSONInto(ctx, serverURL, token, "/v1/nodes/leave", map[string]string{"node_id": nodeID}, nil)
}

func GetCluster(ctx context.Context, serverURL, token string) (ClusterSummary, []Node, error) {
	var resp struct {
		Summary ClusterSummary `json:"summary"`
		Nodes   []Node         `json:"nodes"`
	}
	if err := getJSON(ctx, serverURL, token, "/v1/cluster", &resp); err != nil {
		return ClusterSummary{}, nil, err
	}
	return resp.Summary, resp.Nodes, nil
}

func ListJobs(ctx context.Context, serverURL, token string) ([]Job, error) {
	var jobs []Job
	if err := getJSON(ctx, serverURL, token, "/v1/jobs", &jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

func GetJob(ctx context.Context, serverURL, token, id string) (Job, error) {
	var j Job
	if err := getJSON(ctx, serverURL, token, "/v1/jobs/"+id, &j); err != nil {
		return Job{}, err
	}
	return j, nil
}

func ClaimJob(ctx context.Context, serverURL, token, nodeID string) (Job, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(serverURL, "/")+"/v1/jobs/claim",
		bytes.NewReader(mustJSON(map[string]string{"node_id": nodeID})))
	if err != nil {
		return Job{}, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Houdry-Token", token)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return Job{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return Job{}, false, nil
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return Job{}, false, fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var j Job
	if err := json.Unmarshal(data, &j); err != nil {
		return Job{}, false, err
	}
	return j, true, nil
}

func ReportJobResult(ctx context.Context, serverURL, token, jobID, nodeID string, ok bool, result map[string]any, errMsg string) (Job, error) {
	var out Job
	body := map[string]any{
		"ok":      ok,
		"result":  result,
		"error":   errMsg,
		"node_id": nodeID,
	}
	if err := postJSONInto(ctx, serverURL, token, "/v1/jobs/"+jobID+"/result", body, &out); err != nil {
		return Job{}, err
	}
	return out, nil
}

func postJSON(ctx context.Context, serverURL, token, path string, payload any) (map[string]any, error) {
	var out map[string]any
	if err := postJSONInto(ctx, serverURL, token, path, payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func postJSONInto(ctx context.Context, serverURL, token, path string, payload, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(serverURL, "/")+path, bytes.NewReader(mustJSON(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Houdry-Token", token)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if dest == nil {
		return nil
	}
	return json.Unmarshal(data, dest)
}

func getJSON(ctx context.Context, serverURL, token, path string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(serverURL, "/")+path, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("X-Houdry-Token", token)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, dest)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
