package routing

import (
	"strings"
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
	// Vision/document: Phase 5 catalog is text/code only.
	if profile.Modality == ModalityVision || profile.Modality == ModalityDocument {
		return false
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
