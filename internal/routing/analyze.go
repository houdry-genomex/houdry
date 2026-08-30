package routing

import (
	"fmt"
	"strings"
	"unicode"
)

// signal is one weighted routing cue found in a prompt.
type signal struct {
	label  string
	weight int
}

// signalSet accumulates matched cues for one dimension (capability/complexity).
type signalSet struct {
	score   int
	reasons []string
}

func (s *signalSet) add(label string, weight int) {
	s.score += weight
	s.reasons = append(s.reasons, fmt.Sprintf("%s (+%d)", label, weight))
}

// keywordGroup scans for any of the needles; the first hit adds one signal.
func (s *signalSet) keywordGroup(lower, label string, weight int, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(lower, n) {
			s.add(label, weight)
			return true
		}
	}
	return false
}

// Complexity thresholds for the 0–100 score. Kept as constants so tests can
// pin the boundaries and operators can reason about routing behavior.
//
// Calibration: "low" is reserved for genuinely trivial traffic (greetings,
// one-liners) — that is the only tier where a 1–2B model should ever win.
// Any substantive question or work request must clear medium so it lands on
// a model that can actually answer well.
const (
	complexityMediumAt = 20
	complexityHighAt   = 60
)

// Analyze derives a TaskProfile from a user prompt using weighted, dependency-
// free heuristics: routing must work before any model is selected, so there is
// deliberately no LLM-as-judge here. Every contribution is recorded in Hints
// with its weight, so a routing decision can always be explained.
func Analyze(prompt string) TaskProfile {
	p := strings.TrimSpace(prompt)
	lower := strings.ToLower(p)
	words := wordCount(p)
	lines := strings.Count(p, "\n") + 1

	profile := TaskProfile{
		Modality:     ModalityText,
		Capabilities: []string{"chat"},
	}
	var cx signalSet

	// ── Modality: vision / document first — they route to dedicated pipelines.
	if containsAny(lower, "ocr", "scanned pdf", "scan this", "screenshot", "image of", "picture of", "photo of", "this drawing", "p&id") {
		profile.Modality = ModalityVision
		profile.Capabilities = []string{"vision", "ocr"}
		cx.add("vision/OCR task", 45)
		profile.Score = clampScore(cx.score)
		profile.Complexity = complexityFor(profile.Score)
		profile.Hints = cx.reasons
		return profile
	}
	if containsAny(lower, ".pdf", "pdf document", "invoice", "form fields") {
		profile.Modality = ModalityDocument
		profile.Capabilities = []string{"document"}
		cx.add("document pipeline task", 45)
		profile.Score = clampScore(cx.score)
		profile.Complexity = complexityFor(profile.Score)
		profile.Hints = cx.reasons
		return profile
	}

	// ── Code signals.
	codeSignals := []signal{
		{"code fence", 25},
		{"stack trace / compile error", 25},
		{"implementation request", 15},
		{"programming language named", 12},
		{"code-review vocabulary", 12},
	}
	code := 0
	if strings.Contains(p, "```") {
		cx.add(codeSignals[0].label, codeSignals[0].weight)
		code++
	}
	if containsAny(lower, "stack trace", "traceback", "compile error", "segfault", "exception", "panic:") {
		cx.add(codeSignals[1].label, codeSignals[1].weight)
		code++
	}
	if containsAny(lower, "implement ", "write a program", "write a function", "fix this code", "refactor", "unit test", "debug ") {
		cx.add(codeSignals[2].label, codeSignals[2].weight)
		code++
	}
	if containsAny(lower, "python", "golang", " go ", "typescript", "javascript", "rust", "c++", "java ", "sql") {
		cx.add(codeSignals[3].label, codeSignals[3].weight)
		code++
	}
	if containsAny(lower, "pull request", "codebase", "func ", "function ", "class ", "regex") {
		cx.add(codeSignals[4].label, codeSignals[4].weight)
		code++
	}
	if code > 0 {
		profile.Modality = ModalityCode
		profile.Capabilities = appendCap(profile.Capabilities, "code")
	}

	// ── Reasoning / math signals.
	var rs signalSet
	rs.keywordGroup(lower, "research/deep-dive requested", 45,
		"research", "deep dive", "deep-dive", "in-depth", "in depth", "detailed analysis",
		"comprehensive", "thorough", "investigate", "deep analysis", "literature",
		"state of the art", "feasibility", "detailed report", "white paper", "root cause")
	proof := rs.keywordGroup(lower, "proof/derivation language", 35, "prove ", "derive ", "theorem", "induction", "irrational", "contradiction")
	steps := rs.keywordGroup(lower, "multi-step reasoning requested", 18, "step by step", "step-by-step", "chain of thought", "show your work", "show the steps")
	comparison := rs.keywordGroup(lower, "comparison/evaluation requested", 15, "compare", "trade-off", "tradeoff", "evaluate", "pros and cons", "which is better")
	explanation := rs.keywordGroup(lower, "explanation requested", 12, "explain why", "explain how", "why does", "analyze", "design ", "architecture")
	rs.keywordGroup(lower, "calculation requested", 15, "calculate", "compute ", "solve ", "equation", "how many", "optimi")
	if proof && steps {
		// A proof that must show its steps is the hardest text tier.
		rs.add("layered reasoning demands", 15)
	}
	if (comparison || explanation) && words > 15 {
		rs.add("multi-clause analytical prompt", 10)
	}
	if rs.score > 0 {
		profile.Capabilities = appendCap(profile.Capabilities, "reasoning")
		cx.score += rs.score
		cx.reasons = append(cx.reasons, rs.reasons...)
	}

	// ── Substantive-request baselines: a real question or work order deserves
	// a capable model even when no specialist keyword fires. These are what
	// keep everyday prompts off the tiny-model tier.
	interrogative := strings.HasSuffix(strings.TrimRight(lower, " .!"), "?") ||
		containsAny(lower, "what ", "how ", "why ", "when ", "which ", "who ", "where ")
	if interrogative && words >= 4 {
		cx.add("question form", 20)
	}
	if containsAny(lower, "write ", "create ", "generate ", "draft ", "summarize", "summarise", "translate", "list the", "make a", "give me a", "explain") {
		cx.add("work request verb", 20)
	}
	if containsAny(lower, "story", "poem", "essay", "email", "letter", "slogan", "speech") {
		cx.add("composition requested", 12)
	}

	// ── Structured output raises the bar: schema-following needs a better model.
	var ss signalSet
	ss.keywordGroup(lower, "structured output (JSON/schema)", 12, " json", "json ", "yaml", "schema", "csv")
	ss.keywordGroup(lower, "tabular output", 8, "as a table", "in a table", "markdown table")
	cx.score += ss.score
	cx.reasons = append(cx.reasons, ss.reasons...)

	// ── Multi-step task shape (numbered plans, sequencing words).
	if containsAny(lower, "first,", "then ", "after that", "finally,", "1.", "step 1") && words > 25 {
		cx.add("multi-step task shape", 10)
	}

	// ── Length / structure bumps: long context needs a stronger model.
	switch {
	case words > 400 || lines > 40:
		cx.add(fmt.Sprintf("long prompt (%d words, %d lines)", words, lines), 30)
	case words > 150 || lines > 15:
		cx.add(fmt.Sprintf("substantial prompt (%d words)", words), 18)
	case words > 60:
		cx.add(fmt.Sprintf("medium prompt (%d words)", words), 8)
	}

	// ── Trivial-prompt dampener: greetings and one-liners go to the smallest
	// model even if a stray keyword matched. Applies only to short prompts.
	if words <= 12 && cx.score <= 20 {
		if containsAny(lower, "hello", "hi", "hey", "ping", "thanks", "one sentence", "one short", "say ") || cx.score == 0 {
			profile.Capabilities = appendCap(profile.Capabilities, "simple")
			cx.reasons = append(cx.reasons, "trivial one-liner (score floor)")
			cx.score = 0
		}
	}

	profile.Score = clampScore(cx.score)
	profile.Complexity = complexityFor(profile.Score)
	if profile.Complexity == ComplexityLow {
		profile.Capabilities = appendCap(profile.Capabilities, "simple")
	}
	profile.Hints = cx.reasons
	return profile
}

func complexityFor(score int) Complexity {
	switch {
	case score >= complexityHighAt:
		return ComplexityHigh
	case score >= complexityMediumAt:
		return ComplexityMedium
	default:
		return ComplexityLow
	}
}

func clampScore(score int) int {
	if score < 0 {
		return 0
	}
	if score > 100 {
		return 100
	}
	return score
}

func appendCap(caps []string, c string) []string {
	if hasCap(caps, c) {
		return caps
	}
	return append(caps, c)
}

func hasCap(caps []string, want string) bool {
	for _, c := range caps {
		if strings.EqualFold(c, want) {
			return true
		}
	}
	return false
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
