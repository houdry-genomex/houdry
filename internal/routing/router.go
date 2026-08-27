package routing

import (
	"fmt"
	"sort"
	"strings"

	"houdry/internal/modelruntime"
)

// NodeView is the minimal node info the router needs (keeps routing free of server types).
type NodeView struct {
	NodeID        string
	Host          string
	Status        string // must be READY to be selected
	ModelRuntimes []string
	Models        []modelruntime.Model
	VRAMTotal     uint64 // best-effort; 0 = unknown
	VRAMAvailable uint64
}

// RouteRequest is the input to the router.
type RouteRequest struct {
	Prompt           string
	Catalog          []CatalogEntry
	Nodes            []NodeView
	PreferLoaded     bool
	AllowPull        bool
	RequirePresent   bool // only models already on a node
	PreferredRuntime string
	// RequireTools restricts selection to models that accept OpenAI-style tools
	// (Ollama rejects tools on models like tinyllama).
	RequireTools bool
}

// Route picks the best (model, node) pair for a prompt.
func Route(req RouteRequest) Decision {
	profile := Analyze(req.Prompt)
	if req.RequireTools {
		if !hasCap(profile.Capabilities, "tools") {
			profile.Capabilities = append(profile.Capabilities, "tools")
		}
		// Tool-using agents need at least a mid-tier model; tinyllama is out.
		if ComplexityRank(profile.Complexity) < ComplexityRank(ComplexityMedium) {
			profile.Complexity = ComplexityMedium
		}
		profile.Hints = append(profile.Hints, "tools requested → tool-capable model required")
	}
	d := Decision{Profile: profile}

	if profile.Modality == ModalityVision || profile.Modality == ModalityDocument {
		d.Deferred = true
		d.Message = fmt.Sprintf("%s modality requires a later pipeline (OCR/vision); not routed in Phase 5", profile.Modality)
		return d
	}

	catalog := req.Catalog
	if len(catalog) == 0 {
		catalog = DefaultCatalog()
	}

	var cands []Candidate
	for _, entry := range catalog {
		if req.RequireTools && !EntrySupportsTools(entry) {
			continue
		}
		if !EntrySupports(entry, profile) {
			continue
		}
		if req.PreferredRuntime != "" && entry.Runtime != "" && entry.Runtime != req.PreferredRuntime {
			continue
		}
		for _, node := range req.Nodes {
			if !strings.EqualFold(node.Status, "READY") {
				continue
			}
			c, ok := scorePair(entry, node, profile, req)
			if ok {
				cands = append(cands, c)
			}
		}
	}

	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Score != cands[j].Score {
			return cands[i].Score > cands[j].Score
		}
		if cands[i].Entry.Priority != cands[j].Entry.Priority {
			return cands[i].Entry.Priority > cands[j].Entry.Priority
		}
		return cands[i].NodeID < cands[j].NodeID
	})
	d.Candidates = cands
	if len(cands) == 0 {
		// Best-effort fallback: prefer any present chat model on a READY node
		// so a single-laptop cluster with only tinyllama still routes.
		// Never fall back to a non-tool model when tools were requested.
		if fb := fallbackPresent(profile, catalog, req); fb != nil {
			d.Candidates = []Candidate{*fb}
			d.Selected = fb
			d.Message = fmt.Sprintf("fallback %s on %s (no ideal catalog match)", fb.Entry.Ref(), label(*fb))
			d.Profile.Hints = append(d.Profile.Hints, "used best-effort fallback")
			return d
		}
		if req.RequireTools {
			d.Message = "no tool-capable model+node pair; install a tools-supporting model (e.g. qwen2.5-coder)"
			return d
		}
		d.Message = "no suitable model+node pair; install a matching model or relax requirements"
		return d
	}
	sel := cands[0]
	d.Selected = &sel
	d.Message = fmt.Sprintf("selected %s on %s (score %d)", sel.Entry.Ref(), label(sel), sel.Score)
	return d
}

func fallbackPresent(profile TaskProfile, catalog []CatalogEntry, req RouteRequest) *Candidate {
	var best *Candidate
	for _, entry := range catalog {
		if req.RequireTools && !EntrySupportsTools(entry) {
			continue
		}
		if !hasCap(entry.Capabilities, "chat") && !hasCap(entry.Capabilities, "simple") {
			continue
		}
		for _, node := range req.Nodes {
			if !strings.EqualFold(node.Status, "READY") {
				continue
			}
			id := modelruntime.Identity{Name: entry.Name, Tag: entry.Tag, Runtime: entry.Runtime}
			if !modelruntime.HasModel(node.Models, id) {
				continue
			}
			c := Candidate{
				Entry:   entry,
				NodeID:  node.NodeID,
				Host:    node.Host,
				Present: true,
				Loaded:  modelruntime.IsLoaded(node.Models, id),
				Score:   10 + entry.Priority,
				Reasons: []string{"fallback: present on node", fmt.Sprintf("requested complexity=%s", profile.Complexity)},
			}
			if best == nil || c.Score > best.Score {
				cp := c
				best = &cp
			}
		}
	}
	return best
}

func label(c Candidate) string {
	if c.Host != "" {
		return c.Host
	}
	return c.NodeID
}

func scorePair(entry CatalogEntry, node NodeView, profile TaskProfile, req RouteRequest) (Candidate, bool) {
	id := modelruntime.Identity{
		Name:    entry.Name,
		Tag:     entry.Tag,
		Runtime: firstNonEmpty(req.PreferredRuntime, entry.Runtime),
	}

	if id.Runtime != "" && !hasRuntime(node.ModelRuntimes, id.Runtime) {
		return Candidate{}, false
	}
	if id.Runtime == "" && len(node.ModelRuntimes) == 0 {
		return Candidate{}, false
	}

	// VRAM gate
	if entry.MinVRAMBytes > 0 {
		avail := node.VRAMAvailable
		if avail == 0 {
			avail = node.VRAMTotal
		}
		if avail > 0 && avail < entry.MinVRAMBytes {
			return Candidate{}, false
		}
	}

	present := modelruntime.HasModel(node.Models, id)
	loaded := modelruntime.IsLoaded(node.Models, id)
	if req.RequirePresent && !present {
		return Candidate{}, false
	}
	if !req.AllowPull && !present {
		return Candidate{}, false
	}

	c := Candidate{
		Entry:   entry,
		NodeID:  node.NodeID,
		Host:    node.Host,
		Present: present,
		Loaded:  loaded,
	}
	score := 0
	var reasons []string

	// Prefer the smallest model class that can handle the task (avoid overkill).
	diff := ComplexityRank(entry.MaxComplexity) - ComplexityRank(profile.Complexity)
	switch diff {
	case 0:
		score += 40
		reasons = append(reasons, "complexity match")
	case 1:
		score += 15
		reasons = append(reasons, "one tier above needed")
	default:
		score += 5
		reasons = append(reasons, "larger than needed")
	}

	// Capability overlap
	overlap := 0
	for _, need := range profile.Capabilities {
		if hasCap(entry.Capabilities, need) {
			overlap++
		}
	}
	score += overlap * 12
	if overlap > 0 {
		reasons = append(reasons, fmt.Sprintf("capability overlap=%d", overlap))
	}
	if profile.Modality == ModalityCode && hasCap(entry.Capabilities, "code") {
		score += 25
		reasons = append(reasons, "coding model")
	}
	if profile.Complexity == ComplexityLow && hasCap(entry.Capabilities, "simple") {
		score += 20
		reasons = append(reasons, "simple-capable model")
	}
	if req.RequireTools && EntrySupportsTools(entry) {
		score += 35
		reasons = append(reasons, "tool-capable model")
	}

	if loaded {
		score += 80
		reasons = append(reasons, "model LOADED")
	} else if present {
		score += 25
		reasons = append(reasons, "model AVAILABLE")
	} else if req.AllowPull {
		score += 5
		reasons = append(reasons, "would pull")
	}

	if req.PreferLoaded && !loaded {
		score -= 10
	}

	score += entry.Priority
	c.Score = score
	c.Reasons = reasons
	return c, true
}

func hasRuntime(runtimes []string, want string) bool {
	for _, r := range runtimes {
		if r == want {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
