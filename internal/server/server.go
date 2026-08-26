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
	"houdry/internal/version"
)

type Options struct {
	DataDir     string
	BinariesDir string
	Token       string
	Version     string
}

type Server struct {
	opts  Options
	store *Store
	mux   *http.ServeMux
}

func New(opts Options) (*Server, error) {
	if opts.Version == "" {
		opts.Version = version.Version
	}
	st, err := NewStore(opts.DataDir)
	if err != nil {
		return nil, err
	}
	s := &Server{opts: opts, store: st, mux: http.NewServeMux()}
	s.routes()
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

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /", s.handleDashboard)
	s.mux.HandleFunc("GET /v1/nodes", s.handleListNodes)
	s.mux.HandleFunc("POST /v1/nodes/join", s.handleJoin)
	s.mux.HandleFunc("GET /install.sh", s.handleInstallSH)
	s.mux.HandleFunc("GET /install.ps1", s.handleInstallPS1)
	s.mux.HandleFunc("GET /download/{os}/{arch}", s.handleDownload)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.opts.Version})
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body"})
		return
	}
	var inv gpu.Inventory
	if err := json.Unmarshal(body, &inv); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
		return
	}
	if inv.NodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id is required"})
		return
	}
	if inv.DetectedAt.IsZero() {
		inv.DetectedAt = time.Now().UTC()
	}
	n := Node{Inventory: inv, RemoteIP: clientIP(r)}
	n = s.store.Upsert(n)
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.store.List())
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
	}).Parse(dashboardHTML)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(w, map[string]any{
		"Version": s.opts.Version,
		"Nodes":   s.store.List(),
		"Server":  publicURL(r),
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

// Join posts a local inventory to a Houdry server.
func Join(ctx context.Context, serverURL, token string, inv gpu.Inventory) (map[string]any, error) {
	body, err := json.Marshal(inv)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(serverURL, "/")+"/v1/nodes/join", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Houdry-Token", token)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode join response: %w", err)
	}
	return out, nil
}

func ListNodes(ctx context.Context, serverURL, token string) ([]Node, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(serverURL, "/")+"/v1/nodes", nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("X-Houdry-Token", token)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	var nodes []Node
	if err := json.Unmarshal(data, &nodes); err != nil {
		return nil, fmt.Errorf("decode nodes: %w", err)
	}
	return nodes, nil
}
