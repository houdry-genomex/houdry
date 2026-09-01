package routeropenai

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
// CAD intent bypasses plain vision chat and runs scripts/cad/houdry_pipeline.py,
// which reads the drawing with the local vision model, writes CadQuery with the
// local code model, and executes it into a STEP file. Everything stays on this
// machine: inference runs on Ollama and the geometry kernel is local
// OpenCascade. The approach is inspired by cad3dify (MIT, neka-nat), but the
// pipeline depends only on cadquery — see scripts/cad/README.md.

// cadIntentTerms deliberately avoids the bare word "step" — "step by step"
// is reasoning vocabulary, not CAD vocabulary.
var cadIntentTerms = []string{
	"step file", ".step", ".stp", "3d model", "3d cad", "cad model",
	"convert to 3d", "make it 3d", "generate 3d", "solid model", "3dify",
}

// CadIntent reports whether the prompt is asking for a 3D/CAD artifact.
func CadIntent(prompt string) bool {
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

// cadScript locates scripts/cad/houdry_pipeline.py, which ships in this repo.
// It is looked up relative to the working directory first and to the binary
// second, so `go run`, `./houdry` and an installed binary all resolve it.
func cadScript() (string, error) {
	if p := os.Getenv("HOUDRY_CAD_SCRIPT"); p != "" {
		return filepath.Abs(p)
	}
	rel := filepath.Join("scripts", "cad", "houdry_pipeline.py")
	roots := []string{"."}
	if exe, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Dir(exe))
	}
	for _, root := range roots {
		candidate, err := filepath.Abs(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s not found — run from the repo root or set HOUDRY_CAD_SCRIPT", rel)
}

// cadPython resolves the interpreter that has cadquery installed: the repo's
// own .venv (created by scripts/cad/setup), else an explicit override, else
// whatever python is on PATH.
func cadPython() (string, error) {
	if p := os.Getenv("HOUDRY_CAD_PYTHON"); p != "" {
		return p, nil
	}
	venvRelative := []string{
		filepath.Join(".venv", "Scripts", "python.exe"), // Windows
		filepath.Join(".venv", "bin", "python"),         // Linux/macOS
	}
	roots := []string{"."}
	if exe, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Dir(exe))
	}
	for _, root := range roots {
		for _, rel := range venvRelative {
			candidate, err := filepath.Abs(filepath.Join(root, rel))
			if err != nil {
				continue
			}
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
	}
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no python with cadquery found — see scripts/cad/README.md (or set HOUDRY_CAD_PYTHON)")
}

// RunCADStream executes the drawing→STEP pipeline, streaming cad3dify's own
// progress log as chat deltas and finishing with a downloadable artifact.
func RunCADStream(ctx context.Context, req routerchat.AnswerRequest, filesDir string, emit func(routerchat.StreamEvent)) error {
	started := time.Now()
	script, err := cadScript()
	if err != nil {
		return err
	}
	python, err := cadPython()
	if err != nil {
		return err
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

	model := os.Getenv("HOUDRY_VISION_MODEL")
	if model == "" {
		model = "qwen2.5vl:7b"
	}
	codeModel := os.Getenv("HOUDRY_CODE_MODEL")
	if codeModel == "" {
		codeModel = "llama3.1:8b"
	}
	emit(routerchat.StreamEvent{Type: "delta",
		Delta: "CAD pipeline (local): " + model + " reads the drawing → " + codeModel + " writes the CadQuery code\n"})

	runCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runCtx, python, script, imgPath, "--output_filepath", outPath)
	cmd.Dir = filepath.Dir(filepath.Dir(filepath.Dir(script))) // repo root
	cmd.Env = append(os.Environ(),
		"HOUDRY_VISION_MODEL="+model,
		"HOUDRY_CODE_MODEL="+codeModel,
		"PYTHONIOENCODING=utf-8")

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

	// The pipeline tessellates an STL beside the STEP for previewing. It is
	// best-effort there, so treat it as optional here too.
	artifact := &routerchat.Artifact{
		Name:      outName,
		URL:       "/files/" + outName,
		SizeBytes: info.Size(),
	}
	previewName := strings.TrimSuffix(outName, ".step") + ".stl"
	if _, err := os.Stat(filepath.Join(filesDir, previewName)); err == nil {
		artifact.PreviewURL = "/files/" + previewName
	}

	resp := routerchat.AnswerResponse{
		Model:  model + " + " + codeModel + " (local CAD pipeline)",
		Answer: "3D CAD model generated from the drawing. Download the STEP file below and open it in FreeCAD or any CAD tool.",
		Metrics: routerchat.Metrics{
			WallMS: time.Since(started).Milliseconds(),
		},
		Attempts: 1,
		File:     artifact,
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
