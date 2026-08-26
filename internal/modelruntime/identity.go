package modelruntime

import "strings"

// Identity is the runtime-agnostic model key used by the scheduler and jobs.
//
//	Model
//	├── name      (e.g. "qwen2", "tinyllama")
//	├── tag       (e.g. "0.5b", "latest")
//	└── runtime   (e.g. "ollama", "vllm") — optional on the job; required on node inventory
//
// The same logical model can be served by different runtimes without changing
// job/scheduler shape:
//
//	model=qwen2 tag=7b runtime=ollama
//	model=qwen2 tag=7b runtime=vllm
type Identity struct {
	Name    string `json:"name"`
	Tag     string `json:"tag,omitempty"`
	Runtime string `json:"runtime,omitempty"`
}

// ParseRef splits "name", "name:tag", or "name:tag:extra" into Identity.
// Runtime is never inferred from the ref — set it separately.
func ParseRef(ref string) Identity {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Identity{}
	}
	// HuggingFace-style "org/model:tag" — keep org/model as Name.
	lastColon := strings.LastIndex(ref, ":")
	if lastColon <= 0 {
		return Identity{Name: ref}
	}
	// Avoid treating "http://" or drive letters as tags; only split when
	// the suffix looks like a tag (no slashes).
	tag := ref[lastColon+1:]
	name := ref[:lastColon]
	if tag == "" || strings.Contains(tag, "/") {
		return Identity{Name: ref}
	}
	return Identity{Name: name, Tag: tag}
}

// Ref returns the canonical "name" or "name:tag" form used when talking to backends.
func (id Identity) Ref() string {
	name := strings.TrimSpace(id.Name)
	tag := strings.TrimSpace(id.Tag)
	if name == "" {
		return ""
	}
	if tag == "" {
		return name
	}
	return name + ":" + tag
}

// EqualNameTag reports whether name/tag match (runtime ignored).
// Empty want.Tag matches any tag on the candidate (e.g. job "tinyllama" ↔ "tinyllama:latest").
func (want Identity) EqualNameTag(have Identity) bool {
	if normalize(want.Name) == "" || normalize(have.Name) == "" {
		return false
	}
	if normalize(want.Name) != normalize(have.Name) {
		return false
	}
	if want.Tag == "" {
		return true
	}
	return normalize(want.Tag) == normalize(have.Tag)
}

// Matches reports whether have satisfies want, including optional runtime filter.
func (want Identity) Matches(have Identity) bool {
	if !want.EqualNameTag(have) {
		return false
	}
	if want.Runtime != "" && normalize(want.Runtime) != normalize(have.Runtime) {
		return false
	}
	return true
}

func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ModelFromRef builds a Model inventory row from a backend ref + runtime.
func ModelFromRef(ref, runtime, state string) Model {
	id := ParseRef(ref)
	return Model{
		Name:    id.Name,
		Tag:     id.Tag,
		Runtime: runtime,
		State:   state,
	}
}
