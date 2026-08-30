package routing

import (
	"fmt"
	"strings"

	"houdry/internal/modelruntime"
)

// DefaultCatalog is a starter registry of logical models.
// Operators can replace this via the control plane catalog file.
// Entries are runtime-agnostic; Runtime is only a preference hint.
func DefaultCatalog() []CatalogEntry {
	return []CatalogEntry{
		{
			Name:          "tinyllama",
			Tag:           "latest",
			Capabilities:  []string{"chat", "simple"},
			MaxComplexity: ComplexityLow,
			MinVRAMBytes:  1 << 30,
			Priority:      10,
			Description:   "Very small chat model for simple prompts (fits ~4GB GPUs)",
		},
		{
			Name:          "qwen2.5",
			Tag:           "0.5b",
			Capabilities:  []string{"chat", "simple", "reasoning"},
			MaxComplexity: ComplexityLow,
			MinVRAMBytes:  1 << 30,
			Priority:      20,
			Description:   "Small general model",
		},
		{
			Name:          "qwen2.5-coder",
			Tag:           "1.5b",
			Capabilities:  []string{"code", "chat", "tools"},
			MaxComplexity: ComplexityMedium,
			MinVRAMBytes:  2 << 30,
			Priority:      40,
			Description:   "Small coding-oriented model (tool-capable)",
		},
		{
			Name:          "llama3.2",
			Tag:           "3b",
			Capabilities:  []string{"chat", "reasoning", "code", "tools"},
			MaxComplexity: ComplexityMedium,
			MinVRAMBytes:  3 << 30,
			Priority:      30,
			Description:   "General mid-size model",
		},
		{
			Name:          "qwen2.5-coder",
			Tag:           "7b",
			Capabilities:  []string{"code", "chat", "reasoning", "tools"},
			MaxComplexity: ComplexityHigh,
			MinVRAMBytes:  6 << 30,
			Priority:      50,
			Description:   "Stronger coding model (needs more VRAM)",
		},
	}
}

// EntrySupportsTools reports whether a catalog entry can accept OpenAI-style tools.
// Uses the "tools" capability when present; otherwise applies a conservative
// name heuristic so older on-disk catalogs still route correctly.
func EntrySupportsTools(e CatalogEntry) bool {
	if hasCap(e.Capabilities, "tools") {
		return true
	}
	n := strings.ToLower(e.Name)
	switch {
	case strings.Contains(n, "tinyllama"):
		return false
	case strings.Contains(n, "coder"):
		return true
	case strings.HasPrefix(n, "llama3.1"), strings.HasPrefix(n, "llama3.2"), strings.HasPrefix(n, "llama3.3"):
		return true
	case strings.HasPrefix(n, "qwen3"):
		return true
	default:
		return false
	}
}

// EntrySupports reports whether the catalog entry can serve the profile.
func EntrySupports(e CatalogEntry, profile TaskProfile) bool {
	if ComplexityRank(e.MaxComplexity) < ComplexityRank(profile.Complexity) {
		return false
	}
	// Vision/document tasks require a model that can see the input.
	if profile.Modality == ModalityVision || profile.Modality == ModalityDocument {
		return hasCap(e.Capabilities, "vision")
	}
	if hasCap(profile.Capabilities, "tools") && !EntrySupportsTools(e) {
		return false
	}
	if profile.Modality == ModalityCode {
		return hasCap(e.Capabilities, "code") || hasCap(e.Capabilities, "chat")
	}
	// Prefer at least one overlapping capability; allow chat as universal text fallback.
	for _, need := range profile.Capabilities {
		if need == "tools" {
			continue // already enforced above
		}
		if hasCap(e.Capabilities, need) {
			return true
		}
	}
	return hasCap(e.Capabilities, "chat")
}

// familyMeta captures what the router should assume about a model family when
// it builds a catalog from live runtime inventory instead of a curated file.
type familyMeta struct {
	match        []string // substring match on the normalized model name
	capabilities []string
	tools        bool
	description  string
}

// knownFamilies is ordered: the first match wins, so more specific families
// (coder, vision) come before their general parents.
var knownFamilies = []familyMeta{
	{match: []string{"qwen2.5vl", "qwen-vl", "llava", "moondream", "minicpm-v"},
		capabilities: []string{"vision", "ocr", "chat"}, tools: false,
		description: "vision-language model"},
	{match: []string{"coder", "codellama", "codegemma", "starcoder"},
		capabilities: []string{"code", "chat", "reasoning"}, tools: true,
		description: "code-specialized model"},
	{match: []string{"deepseek-r1", "qwq", "thinking", "o1", "marco-o1"},
		capabilities: []string{"reasoning", "chat", "code"}, tools: false,
		description: "reasoning-first model (thinks before answering)"},
	{match: []string{"llama3", "llama-3"},
		capabilities: []string{"chat", "reasoning", "code"}, tools: true,
		description: "general instruction model"},
	{match: []string{"qwen"},
		capabilities: []string{"chat", "reasoning", "code"}, tools: true,
		description: "general instruction model"},
	{match: []string{"mistral", "mixtral", "gemma", "phi"},
		capabilities: []string{"chat", "reasoning"}, tools: false,
		description: "general instruction model"},
	{match: []string{"tinyllama", "smollm"},
		capabilities: []string{"chat", "simple"}, tools: false,
		description: "very small chat model"},
}

// CatalogFromModels builds a routing catalog from the models actually present
// on a runtime (e.g. the local Ollama daemon). Capability and tier metadata
// come from the family table plus the parameter count parsed from the name/tag,
// so newly pulled models are routable with zero configuration.
func CatalogFromModels(models []modelruntime.Model) []CatalogEntry {
	entries := make([]CatalogEntry, 0, len(models))
	for _, m := range models {
		entries = append(entries, entryForModel(m))
	}
	return entries
}

func entryForModel(m modelruntime.Model) CatalogEntry {
	ref := strings.ToLower(strings.TrimSpace(m.Name + ":" + m.Tag))
	entry := CatalogEntry{
		Name:         m.Name,
		Tag:          m.Tag,
		Runtime:      m.Runtime,
		Capabilities: []string{"chat"},
		Description:  "auto-cataloged from runtime inventory",
	}

	for _, fam := range knownFamilies {
		for _, needle := range fam.match {
			if strings.Contains(ref, needle) {
				entry.Capabilities = append([]string{}, fam.capabilities...)
				if fam.tools {
					entry.Capabilities = append(entry.Capabilities, "tools")
				}
				entry.Description = fam.description
				goto sized
			}
		}
	}

sized:
	params := parseParamBillions(ref, m.ParameterSize)
	switch {
	// Priority is a TIEBREAKER among equals, deliberately small: it must
	// never outvote right-sizing (a 40-point spread) or a capability match.
	case params > 0 && params < 2:
		entry.MaxComplexity = ComplexityLow
		entry.Capabilities = appendCap(entry.Capabilities, "simple")
		entry.Priority = 2
	case params >= 2 && params <= 8:
		entry.MaxComplexity = ComplexityMedium
		entry.Priority = 6
	case params > 8:
		entry.MaxComplexity = ComplexityHigh
		entry.Priority = 10
	default:
		// Unknown size: assume medium so the model is usable but never the
		// automatic pick for hard tasks.
		entry.MaxComplexity = ComplexityMedium
		entry.Priority = 4
	}

	// VRAM floor ≈ on-disk weight size; a sane gate when the node reports VRAM.
	if m.SizeBytes > 0 {
		entry.MinVRAMBytes = m.SizeBytes
	}
	return entry
}

// parseParamBillions extracts a parameter count in billions from strings like
// "deepseek-r1:14b", "lfm2.5-thinking:1.2b", or Ollama's ParameterSize "14.8B".
func parseParamBillions(ref, parameterSize string) float64 {
	if v := paramsFrom(strings.ToLower(parameterSize)); v > 0 {
		return v
	}
	return paramsFrom(ref)
}

func paramsFrom(s string) float64 {
	for i := 0; i < len(s); i++ {
		if s[i] != 'b' {
			continue
		}
		// walk back over digits and at most one dot
		j := i
		dot := false
		for j > 0 {
			c := s[j-1]
			if c >= '0' && c <= '9' {
				j--
				continue
			}
			if c == '.' && !dot {
				dot = true
				j--
				continue
			}
			break
		}
		if j == i {
			continue
		}
		// reject "...Xb" that is part of a longer word (e.g. "webb")
		if i+1 < len(s) && (s[i+1] >= 'a' && s[i+1] <= 'z') {
			continue
		}
		var v float64
		if _, err := fmt.Sscanf(s[j:i], "%f", &v); err == nil && v > 0 && v < 2000 {
			return v
		}
	}
	return 0
}
