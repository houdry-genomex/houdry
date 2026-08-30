package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"houdry/internal/openaicompat"
	"houdry/internal/routerchat"
)

// OpenAI-compatible surface for the routed chat server.
//
// `houdry serve` also exposes /v1, but that path dispatches cluster jobs
// against the seeded catalog and fakes streaming (it buffers the whole answer
// and emits it as one chunk). This adapter instead sits directly on
// routerchat, so clients pointed at this port get the live Ollama inventory,
// real token-by-token streaming, vision input, and the drawing->STEP pipeline
// through the same OpenAI shape any SDK already speaks.

// registerOpenAICompat mounts the /v1 endpoints onto the routed chat server.
func registerOpenAICompat(mux *http.ServeMux, svc *routerchat.Service, filesDir string) {
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		catalog, _, err := svc.Snapshot(r.Context())
		if err != nil {
			openaicompat.WriteError(w, http.StatusServiceUnavailable, "server_error", "upstream_unavailable", err.Error())
			return
		}
		// "auto" first: it is the model clients should pin, since picking a
		// specific one bypasses the router.
		data := []map[string]any{{
			"id": "auto", "object": openaicompat.ObjectModel,
			"created": 0, "owned_by": "houdry",
		}}
		for _, e := range catalog {
			id := e.Name
			if e.Tag != "" {
				id = e.Name + ":" + e.Tag
			}
			data = append(data, map[string]any{
				"id": id, "object": openaicompat.ObjectModel,
				"created": 0, "owned_by": "houdry",
			})
		}
		openaicompat.WriteJSON(w, http.StatusOK, map[string]any{
			"object": openaicompat.ObjectList, "data": data,
		})
	})

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			openaicompat.WriteError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "POST only")
			return
		}
		var req openaicompat.ChatCompletionRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
			openaicompat.WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_body", err.Error())
			return
		}
		if err := openaicompat.ValidateChatRequest(req); err != nil {
			openaicompat.WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request", err.Error())
			return
		}

		answerReq := routerchat.AnswerRequest{
			Prompt:  openaicompat.LastUserText(req.Messages),
			History: historyFromMessages(req.Messages),
			Images:  imagesFromMessages(req.Messages),
		}
		if req.MaxTokens != nil && *req.MaxTokens > 0 {
			answerReq.MaxTokens = *req.MaxTokens
		}
		if strings.TrimSpace(answerReq.Prompt) == "" {
			openaicompat.WriteError(w, http.StatusBadRequest, "invalid_request_error", "empty_prompt", "no user message content")
			return
		}

		// A path typed in the message counts as an attachment; see
		// imagesFromPrompt for why this is load-bearing for CAD.
		if len(answerReq.Images) == 0 {
			answerReq.Images = imagesFromPrompt(answerReq.Prompt)
		}

		id := "chatcmpl-" + randomID()
		wantsCAD := cadIntent(answerReq.Prompt)
		isCAD := len(answerReq.Images) > 0 && wantsCAD

		// Asked for a 3D model with no usable image. Answer directly instead of
		// handing the turn to a chat model: it cannot see, cannot run the
		// pipeline, and small models respond to that gap by inventing tools and
		// fabricating a result, which reads as success to the user.
		if wantsCAD && len(answerReq.Images) == 0 {
			writeCADNeedsImage(w, req.Stream, id)
			return
		}

		if req.Stream {
			streamOpenAI(r.Context(), w, svc, answerReq, id, isCAD, filesDir, absoluteFileBase(r))
			return
		}

		// Non-streaming: collect the answer, then shape it as a completion.
		var (
			text  strings.Builder
			model = "auto"
			file  *routerchat.Artifact
		)
		emit := func(ev routerchat.StreamEvent) {
			switch ev.Type {
			case "delta":
				text.WriteString(ev.Delta)
			case "retry":
				text.Reset() // a failover invalidates everything streamed so far
			case "done":
				if ev.Response != nil {
					model = ev.Response.Model
					file = ev.Response.File
					if ev.Response.Answer != "" {
						text.Reset()
						text.WriteString(ev.Response.Answer)
					}
				}
			}
		}
		var err error
		if isCAD {
			err = runCADStream(r.Context(), answerReq, filesDir, emit)
		} else {
			err = svc.AnswerStream(r.Context(), answerReq, emit)
		}
		if err != nil {
			openaicompat.WriteError(w, http.StatusBadGateway, "server_error", "inference_failed", err.Error())
			return
		}
		content := text.String()
		if file != nil {
			content += artifactNote(file, absoluteFileBase(r))
		}
		openaicompat.WriteJSON(w, http.StatusOK,
			openaicompat.BuildCompletion(id, model, content, 0, 0))
	})
}

const cadNeedsImageMessage = "I can build a real 3D model, but I need the drawing itself — " +
	"I can't see one in this message.\n\n" +
	"Attach the image, or paste its full path (for example " +
	"`C:\\Users\\you\\Desktop\\part.jpg`) and I'll run it through the CAD " +
	"pipeline and show you the model inline.\n\n" +
	"Note on what this produces: it reads **2D engineering drawings** — " +
	"outlines, dimension callouts, hole sizes, orthographic views — and " +
	"outputs a solid STEP part. Photographs, screenshots and architecture " +
	"diagrams aren't dimensioned geometry, so they won't yield a meaningful part."

// writeCADNeedsImage answers a CAD request that arrived without a drawing.
// Written in both response shapes so a streaming client sees it in the
// transcript like any other reply.
func writeCADNeedsImage(w http.ResponseWriter, stream bool, id string) {
	if !stream {
		openaicompat.WriteJSON(w, http.StatusOK,
			openaicompat.BuildCompletion(id, "houdry-cad", cadNeedsImageMessage, 0, 0))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		openaicompat.WriteJSON(w, http.StatusOK,
			openaicompat.BuildCompletion(id, "houdry-cad", cadNeedsImageMessage, 0, 0))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(delta map[string]any, finish any) {
		payload, err := json.Marshal(map[string]any{
			"id": id, "object": openaicompat.ObjectChatCompletionChunk,
			"created": 0, "model": "houdry-cad",
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		})
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}
	send(map[string]any{"role": "assistant", "content": cadNeedsImageMessage}, nil)
	send(map[string]any{}, "stop")
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// streamOpenAI relays routerchat events as chat.completion.chunk SSE frames.
func streamOpenAI(ctx context.Context, w http.ResponseWriter, svc *routerchat.Service,
	req routerchat.AnswerRequest, id string, isCAD bool, filesDir, fileBase string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		openaicompat.WriteError(w, http.StatusInternalServerError, "server_error", "no_streaming", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	model := "auto"
	sendChunk := func(delta map[string]any, finish any) {
		payload, err := json.Marshal(map[string]any{
			"id": id, "object": openaicompat.ObjectChatCompletionChunk,
			"created": 0, "model": model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		})
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}
	// The role chunk rides along with the first content chunk rather than
	// leading: routing resolves after the request starts, so emitting it up
	// front would stamp every stream with the literal "auto".
	roleSent := false
	sendContent := func(text string) {
		delta := map[string]any{"content": text}
		if !roleSent {
			delta["role"] = "assistant"
			roleSent = true
		}
		sendChunk(delta, nil)
	}

	emit := func(ev routerchat.StreamEvent) {
		switch ev.Type {
		case "decision":
			// Report the model the router actually picked, not the literal
			// "auto" the client asked for — clients log and display this.
			if ev.Decision != nil && ev.Decision.Selected != nil {
				model = ev.Decision.Selected.Entry.Ref()
			} else if ev.Model != "" {
				model = ev.Model
			}
		case "delta":
			sendContent(ev.Delta)
		case "retry":
			// The OpenAI wire format has no "discard what you have" signal, so
			// surface the failover as visible text rather than silently
			// stitching two different models' output together.
			if ev.Model != "" {
				model = ev.Model
			}
			sendContent("\n\n[retrying on " + model + "]\n\n")
		case "done":
			if ev.Response != nil && ev.Response.File != nil {
				sendContent(artifactNote(ev.Response.File, fileBase))
			}
		case "error":
			sendContent("\n\n[error] " + ev.Err)
		}
	}

	var err error
	if isCAD {
		err = runCADStream(ctx, req, filesDir, emit)
	} else {
		err = svc.AnswerStream(ctx, req, emit)
	}
	if err != nil {
		sendContent("\n\n[error] " + err.Error())
	}
	sendChunk(map[string]any{}, "stop")
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// artifactNote appends a markdown link, so any OpenAI client can surface the
// generated file, followed by a `::model3d{...}` transcript directive.
//
// Houdry Agent renders directives as components inline in the message, which
// is how the 3D preview mounts. The directive must be its own paragraph and a
// single line, or the agent's parser correctly ignores it; clients that do not
// know the name just show one short line of literal text under the link.
//
// The directive carries `origin` plus root-relative paths rather than absolute
// URLs. A bare "http://..." inside the attributes is autolinked by the
// agent's GFM pipeline before the directive is recognised, which turns the
// paragraph into markup, disqualifies it as a directive, and leaves the user
// looking at prose with prettified link labels instead of a 3D preview.
// Nothing here may be autolinkable: no scheme, no "www.".
func artifactNote(f *routerchat.Artifact, base string) string {
	url := base + f.URL
	preview := ""
	if f.PreviewURL != "" {
		preview = fmt.Sprintf(" preview=%q", f.PreviewURL)
	}
	return fmt.Sprintf("\n\n[%s](%s)\n\n::model3d{name=%q origin=%q url=%q%s size=%q}\n",
		f.Name, url, f.Name, strings.TrimPrefix(strings.TrimPrefix(base, "http://"), "https://"),
		f.URL, preview, fmt.Sprint(f.SizeBytes))
}

func absoluteFileBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// historyFromMessages converts prior turns, dropping the final user message
// (routerchat takes that separately as Prompt).
func historyFromMessages(msgs []openaicompat.Message) []routerchat.Turn {
	lastUser := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if strings.EqualFold(msgs[i].Role, "user") {
			lastUser = i
			break
		}
	}
	var out []routerchat.Turn
	for i, m := range msgs {
		if i == lastUser {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role != "user" && role != "assistant" && role != "system" {
			continue
		}
		if text := m.ContentString(); strings.TrimSpace(text) != "" {
			out = append(out, routerchat.Turn{Role: role, Content: text})
		}
	}
	return out
}

// imagesFromMessages pulls base64 payloads out of image_url content parts.
// Both data: URLs and bare base64 are accepted; remote URLs are ignored
// because fetching them would break the on-premise guarantee.
func imagesFromMessages(msgs []openaicompat.Message) []string {
	var images []string
	for _, m := range msgs {
		var parts []map[string]any
		if err := json.Unmarshal(m.Content, &parts); err != nil {
			continue
		}
		for _, p := range parts {
			if t, _ := p["type"].(string); t != "image_url" {
				continue
			}
			raw := ""
			switch v := p["image_url"].(type) {
			case string:
				raw = v
			case map[string]any:
				raw, _ = v["url"].(string)
			}
			if b64 := decodeImagePayload(raw); b64 != "" {
				images = append(images, b64)
			}
		}
	}
	return images
}

// localImagePathRE matches an absolute path to an image file — Windows
// (C:\dir\pic.jpg, C:/dir/pic.jpg) or POSIX (/home/me/pic.jpg).
var localImagePathRE = regexp.MustCompile(
	`(?i)([A-Za-z]:[\\/][^"'<>|?*\r\n]+?|/[^\s"'<>|?*\r\n]+?)\.(jpe?g|png|webp|bmp|tiff?)\b`)

// maxLocalImageBytes caps what a single referenced file may contribute. The
// pipeline downscales anyway; this only stops a stray path to a huge file from
// being slurped into memory.
const maxLocalImageBytes = 64 << 20

// imagesFromPrompt loads images the user referred to by path in their message.
//
// People paste "make a 3D model of C:\...\part.jpg" rather than attaching a
// file, and without this the request has no images at all: CAD intent detection
// fails and the turn falls through to a chat model, which will cheerfully
// invent a result instead of admitting it cannot see the file. Reading from
// the local disk keeps the on-premise guarantee intact — unlike the http(s)
// case in decodeImagePayload, nothing leaves the machine.
func imagesFromPrompt(prompt string) []string {
	var images []string
	seen := map[string]bool{}
	for _, match := range localImagePathRE.FindAllString(prompt, -1) {
		path := strings.Trim(strings.TrimSpace(match), `"'`)
		if seen[path] {
			continue
		}
		seen[path] = true
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() > maxLocalImageBytes {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		images = append(images, base64.StdEncoding.EncodeToString(data))
	}
	return images
}

// decodeImagePayload normalizes a data: URL or bare base64 string, returning
// "" for anything that is not valid inline base64.
func decodeImagePayload(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "data:") {
		idx := strings.Index(raw, ",")
		if idx < 0 {
			return ""
		}
		raw = raw[idx+1:]
	} else if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return "" // remote fetch would leave the machine
	}
	raw = strings.TrimSpace(raw)
	if _, err := base64.StdEncoding.DecodeString(raw); err != nil {
		return ""
	}
	return raw
}

func randomID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "houdry"
	}
	return hex.EncodeToString(b[:])
}
