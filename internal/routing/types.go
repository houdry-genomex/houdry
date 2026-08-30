// Package routing implements Phase 5 intelligent model & resource routing.
//
//	User request → task analysis → model requirements → GPU/model fit → execute
//
// It does not hardcode Ollama or a single model. Catalog entries declare
// capabilities; the router scores (model, node) pairs. OCR/vision pipelines
// are detected but deferred to later phases.
package routing

// Modality is the primary input/output kind for a request.
type Modality string

const (
	ModalityText     Modality = "text"
	ModalityCode     Modality = "code"
	ModalityVision   Modality = "vision"
	ModalityDocument Modality = "document"
)

// Complexity is a coarse estimate of how capable a model must be.
type Complexity string

const (
	ComplexityLow    Complexity = "low"
	ComplexityMedium Complexity = "medium"
	ComplexityHigh   Complexity = "high"
)

func ComplexityRank(c Complexity) int {
	switch c {
	case ComplexityLow:
		return 1
	case ComplexityMedium:
		return 2
	case ComplexityHigh:
		return 3
	default:
		return 1
	}
}

// TaskProfile is the analyzer output — framework-agnostic.
type TaskProfile struct {
	Modality     Modality   `json:"modality"`
	Complexity   Complexity `json:"complexity"`
	Capabilities []string   `json:"capabilities"` // e.g. chat, code, reasoning
	Hints        []string   `json:"hints,omitempty"`
	// Score is the 0–100 weighted complexity estimate behind Complexity, kept
	// so interfaces can show *how* hard the analyzer thinks a task is rather
	// than only the coarse tier.
	Score int `json:"score"`
}

// CatalogEntry describes a logical model the router may choose.
// Runtime is optional preference; empty means any Model Runtime that has it.
type CatalogEntry struct {
	Name          string     `json:"name"`
	Tag           string     `json:"tag,omitempty"`
	Runtime       string     `json:"runtime,omitempty"`
	Capabilities  []string   `json:"capabilities"`
	MaxComplexity Complexity `json:"max_complexity"`
	MinVRAMBytes  uint64     `json:"min_vram_bytes,omitempty"`
	Priority      int        `json:"priority,omitempty"` // higher = preferred among equals
	Description   string     `json:"description,omitempty"`
}

// Ref returns name or name:tag.
func (e CatalogEntry) Ref() string {
	if e.Tag == "" {
		return e.Name
	}
	return e.Name + ":" + e.Tag
}

// Candidate is a scored (model, node) pair.
type Candidate struct {
	Entry   CatalogEntry `json:"entry"`
	NodeID  string       `json:"node_id"`
	Host    string       `json:"host,omitempty"`
	Score   int          `json:"score"`
	Loaded  bool         `json:"loaded"`
	Present bool         `json:"present"`
	Reasons []string     `json:"reasons,omitempty"`
}

// Decision is the router's choice (or an explanation why none).
type Decision struct {
	Profile    TaskProfile `json:"profile"`
	Selected   *Candidate  `json:"selected,omitempty"`
	Candidates []Candidate `json:"candidates,omitempty"`
	Deferred   bool        `json:"deferred,omitempty"`
	Message    string      `json:"message,omitempty"`
}
