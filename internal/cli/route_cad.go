package cli

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"houdry/internal/routerchat"
)

// The CAD pipeline is a routed TOOL, not a model: an attached drawing plus
// CAD intent bypasses plain vision chat and drives cad3dify (MIT, neka-nat),
// which generates CadQuery code with the local vision model and executes it
// into a STEP file. Everything stays on this machine: the vision model runs
// on Ollama and the geometry kernel is local OpenCascade.

// cadIntentTerms deliberately avoids the bare word "step" — "step by step"
// is reasoning vocabulary, not CAD vocabulary.
var cadIntentTerms = []string{
	"step file", ".step", ".stp", "3d model", "3d cad", "cad model",
	"convert to 3d", "make it 3d", "generate 3d", "solid model", "3dify",
}

func cadIntent(prompt string) bool {
	lower := strings.ToLower(prompt)
	for _, term := range cadIntentTerms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	// Bare "cad" as its own word.
	for _, f := range strings.FieldsFunc(lower, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if f == "cad" {
			return true
		}
	}
	return false
}

// cad3difyDir resolves the cad3dify checkout (sibling of this repo by default).
func cad3difyDir() (string, error) {
	dir := os.Getenv("HOUDRY_CAD3DIFY_DIR")
	if dir == "" {
		dir = filepath.Join("..", "cad3dify")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(abs, "scripts", "houdry_pipeline.py")); err != nil {
		return "", fmt.Errorf("cad3dify not found at %s (set HOUDRY_CAD3DIFY_DIR)", abs)
	}
	return abs, nil
}

// runCADStream executes the drawing→STEP pipeline, streaming cad3dify's own
// progress log as chat deltas and finishing with a downloadable artifact.
func runCADStream(ctx context.Context, req routerchat.AnswerRequest, filesDir string, emit func(routerchat.StreamEvent)) error {
	started := time.Now()
	dir, err := cad3difyDir()
	if err != nil {
		return err
	}
	venvPython := filepath.Join(dir, ".venv", "Scripts", "python.exe")
	if _, err := os.Stat(venvPython); err != nil {
		return fmt.Errorf("cad3dify venv missing at %s — run its install first", venvPython)
	}

	raw, err := base64.StdEncoding.DecodeString(req.Images[0])
	if err != nil {
		return fmt.Errorf("decode attached image: %w", err)
	}
	stamp := time.Now().Format("20060102-150405")
	imgPath := filepath.Join(filesDir, "drawing-"+stamp+sniffImageExt(raw))
	if err := os.WriteFile(imgPath, raw, 0o644); err != nil {
		return err
	}
	outName := "model-" + stamp + ".step"
	outPath := filepath.Join(filesDir, outName)

	model := os.Getenv("CAD3DIFY_OLLAMA_MODEL")
	if model == "" {
		model = "qwen2.5vl:7b"
	}
	codeModel := os.Getenv("CAD3DIFY_CODE_MODEL")
	if codeModel == "" {
		codeModel = "llama3.1:8b"
	}
	emit(routerchat.StreamEvent{Type: "delta",
		Delta: "CAD pipeline (local): " + model + " reads the drawing → " + codeModel + " writes the CadQuery code\n"})

	runCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runCtx, venvPython, filepath.Join("scripts", "houdry_pipeline.py"),
		imgPath, "--output_filepath", outPath)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CAD3DIFY_OLLAMA_MODEL="+model, "PYTHONIOENCODING=utf-8")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout // loguru logs to stderr; merge into one stream
	if err := cmd.Start(); err != nil {
		return err
	}

	var lastLines []string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lastLines = append(lastLines, line)
		if len(lastLines) > 12 {
			lastLines = lastLines[1:]
		}
		emit(routerchat.StreamEvent{Type: "delta", Delta: line + "\n"})
	}
	runErr := cmd.Wait()

	info, statErr := os.Stat(outPath)
	if statErr != nil {
		if runErr != nil {
			return fmt.Errorf("cad3dify failed: %v\n%s", runErr, strings.Join(lastLines, "\n"))
		}
		return fmt.Errorf("cad3dify produced no STEP file\n%s", strings.Join(lastLines, "\n"))
	}

	resp := routerchat.AnswerResponse{
		Model:  model + " + " + codeModel + " (local CAD pipeline)",
		Answer: "3D CAD model generated from the drawing. Download the STEP file below and open it in FreeCAD or any CAD tool.",
		Metrics: routerchat.Metrics{
			WallMS: time.Since(started).Milliseconds(),
		},
		Attempts: 1,
		File: &routerchat.Artifact{
			Name:      outName,
			URL:       "/files/" + outName,
			SizeBytes: info.Size(),
		},
	}
	emit(routerchat.StreamEvent{Type: "done", Response: &resp})
	return nil
}

func sniffImageExt(raw []byte) string {
	switch {
	case len(raw) > 3 && raw[0] == 0x89 && raw[1] == 'P':
		return ".png"
	case len(raw) > 2 && raw[0] == 0xFF && raw[1] == 0xD8:
		return ".jpg"
	default:
		return ".img"
	}
}
