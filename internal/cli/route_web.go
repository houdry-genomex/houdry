package cli

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"houdry/internal/routerchat"
	"houdry/internal/routeropenai"
)

// chatHTML is the chat UI, kept as its own file (webui/chat.html) so design
// iterations never touch Go code; go:embed keeps the single-binary story.
//
//go:embed webui/chat.html
var chatHTML []byte

// runRouteWeb serves the routed chat: every message is analyzed, routed to the
// best local model, executed, and answered with metrics — no manual mode
// switches. The transport is thin on purpose: all logic lives in routerchat.
// The HTML bench used to be `houdry route --web`. Chat, streaming, and CAD
// now live on houdry serve; this file is kept so the UI can be remounted
// on the control plane later.
func runRouteWeb(ctx context.Context, ollamaURL, addr string) error {
	backend := routerchat.NewLocalOllama(ollamaURL)
	svc := routerchat.New(backend, backend)

	// Generated artifacts (STEP models, uploaded drawings) live under ./generated
	// and are served read-only at /files/.
	filesDir, err := filepath.Abs("generated")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/files/", http.StripPrefix("/files/", http.FileServer(http.Dir(filesDir))))

	// OpenAI-compatible surface, so Houdry Agent (and any OpenAI SDK) can use
	// this server directly as a provider base_url.
	routeropenai.Register(mux, svc, filesDir)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(chatHTML)
	})

	mux.HandleFunc("/api/models", func(w http.ResponseWriter, r *http.Request) {
		catalog, nodes, err := svc.Snapshot(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, err)
			return
		}
		writeJSON(w, map[string]any{"catalog": catalog, "nodes": nodes})
	})

	// Non-streaming JSON answer (kept for scripts/tests and simple clients).
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req routerchat.AnswerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Prompt) == "" {
			writeJSONError(w, http.StatusBadRequest, fmt.Errorf("body must be {\"prompt\": \"...\"}"))
			return
		}
		resp, err := svc.Answer(r.Context(), req)
		if err != nil {
			// The decision (when routing worked but execution failed) still
			// helps the UI explain what was attempted.
			writeJSON(w, map[string]any{"error": err.Error(), "decision": resp.Decision})
			return
		}
		writeJSON(w, resp)
	})

	// Streaming answer: SSE events (decision → delta* → [retry → delta*] → done).
	mux.HandleFunc("/api/chat/stream", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req routerchat.AnswerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Prompt) == "" {
			writeJSONError(w, http.StatusBadRequest, fmt.Errorf("body must be {\"prompt\": \"...\"}"))
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeJSONError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")

		send := func(ev routerchat.StreamEvent) {
			payload, err := json.Marshal(ev)
			if err != nil {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
		// A drawing plus CAD intent routes to the cad3dify tool pipeline
		// instead of plain vision chat: same stream contract, real artifact out.
		if len(req.Images) > 0 && routeropenai.CadIntent(req.Prompt) {
			if err := routeropenai.RunCADStream(r.Context(), req, filesDir, send); err != nil {
				send(routerchat.StreamEvent{Type: "error", Err: err.Error()})
			}
			return
		}
		if err := svc.AnswerStream(r.Context(), req, send); err != nil {
			send(routerchat.StreamEvent{Type: "error", Err: err.Error()})
		}
	})

	fmt.Printf("Houdry chat → http://%s  (Ollama: %s)\n", addr, ollamaURL)
	fmt.Printf("OpenAI-compatible API → http://%s/v1  (model \"auto\" routes)\n", addr)
	fmt.Println("Ctrl+C to stop.")
	server := &http.Server{Addr: addr, Handler: withCORS(mux), ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

// withCORS allows browser clients (the desktop app renderer runs on a file://
// origin, so every request it makes is cross-origin) to call this server.
// Permissive is acceptable here: the service binds loopback by default and
// holds no credentials.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
