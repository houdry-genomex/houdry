package routing

import (
	"strings"
	"unicode"
)

// Analyze derives a TaskProfile from a user prompt using lightweight heuristics.
// Phase 5 deliberately avoids an LLM-as-judge dependency so routing works even
// before a model is selected.
func Analyze(prompt string) TaskProfile {
	p := strings.TrimSpace(prompt)
	lower := strings.ToLower(p)
	profile := TaskProfile{
		Modality:     ModalityText,
		Complexity:   ComplexityLow,
		Capabilities: []string{"chat"},
	}

	switch {
	case containsAny(lower, "ocr", "scanned pdf", "scan this", "screenshot", "image of", "picture of", "photo of"):
		profile.Modality = ModalityVision
		profile.Capabilities = []string{"vision", "ocr"}
		profile.Complexity = ComplexityHigh
		profile.Hints = append(profile.Hints, "vision/document pipeline not implemented in Phase 5")
	case containsAny(lower, ".pdf", "pdf document", "invoice", "form fields"):
		profile.Modality = ModalityDocument
		profile.Capabilities = []string{"document"}
		profile.Complexity = ComplexityHigh
		profile.Hints = append(profile.Hints, "document pipeline not implemented in Phase 5")
	case containsAny(lower,
		"func ", "function ", "class ", "refactor", "typescript", "golang", "python",
		"bug", "stack trace", "compile", "unit test", "pull request", "codebase",
		"```", "implement ", "write a program", "fix this code"):
		profile.Modality = ModalityCode
		profile.Capabilities = []string{"code", "chat"}
		profile.Complexity = ComplexityMedium
		profile.Hints = append(profile.Hints, "coding cues detected")
	}

	// Complexity bumps from length / structure.
	words := wordCount(p)
	switch {
	case words > 400 || strings.Count(p, "\n") > 40:
		if ComplexityRank(profile.Complexity) < ComplexityRank(ComplexityHigh) {
			profile.Complexity = ComplexityHigh
			profile.Hints = append(profile.Hints, "long prompt → high complexity")
		}
	case words > 80 || strings.Count(p, "\n") > 12:
		if ComplexityRank(profile.Complexity) < ComplexityRank(ComplexityMedium) {
			profile.Complexity = ComplexityMedium
			profile.Hints = append(profile.Hints, "medium-length prompt")
		}
	}

	if profile.Modality == ModalityText && containsAny(lower, "explain", "analyze", "compare", "design", "architecture", "trade-off", "tradeoff") {
		if ComplexityRank(profile.Complexity) < ComplexityRank(ComplexityMedium) {
			profile.Complexity = ComplexityMedium
			profile.Hints = append(profile.Hints, "analytical language → medium complexity")
		}
		if !hasCap(profile.Capabilities, "reasoning") {
			profile.Capabilities = append(profile.Capabilities, "reasoning")
		}
	}

	if profile.Modality == ModalityText && containsAny(lower, "hello", "hi ", "say ", "one sentence", "one short", "ping") {
		profile.Complexity = ComplexityLow
		profile.Capabilities = []string{"chat", "simple"}
		profile.Hints = append(profile.Hints, "simple greeting / short reply")
	}

	return profile
}

func containsAny(hay string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(hay, n) {
			return true
		}
	}
	return false
}

func wordCount(s string) int {
	n := 0
	in := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			in = false
			continue
		}
		if !in {
			n++
			in = true
		}
	}
	return n
}

func hasCap(caps []string, want string) bool {
	for _, c := range caps {
		if strings.EqualFold(c, want) {
			return true
		}
	}
	return false
}
