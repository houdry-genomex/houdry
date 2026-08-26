package server

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	JobQueued    = "queued"  // waiting for a suitable READY node
	JobPending   = "pending" // assigned to a node, awaiting claim
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"

	JobTypeGPUSmoke  = "gpu.smoke"
	JobTypeInference = "inference"
)

// Job is a unit of work. Requirements are framework-agnostic.
type Job struct {
	ID              string         `json:"id"`
	Type            string         `json:"type"`
	Status          string         `json:"status"`
	NodeID          string         `json:"node_id,omitempty"`
	PreferredNodeID string         `json:"preferred_node_id,omitempty"`
	Requirements    Requirements   `json:"requirements"`
	Payload         map[string]any `json:"payload,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	StartedAt       time.Time      `json:"started_at,omitempty"`
	FinishedAt      time.Time      `json:"finished_at,omitempty"`
	Result          map[string]any `json:"result,omitempty"`
	Error           string         `json:"error,omitempty"`
}

type JobStore struct {
	mu   sync.RWMutex
	path string
	jobs map[string]Job
}

func NewJobStore(dataDir string) (*JobStore, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	js := &JobStore{
		path: filepath.Join(dataDir, "jobs.json"),
		jobs: map[string]Job{},
	}
	if err := js.load(); err != nil {
		return nil, err
	}
	return js, nil
}

// DefaultRequirements returns baseline requirements for a job type.
func DefaultRequirements(jobType string) Requirements {
	switch jobType {
	case JobTypeGPUSmoke:
		return Requirements{GPURequired: true}
	case JobTypeInference:
		return Requirements{GPURequired: true}
	default:
		return Requirements{}
	}
}

func (js *JobStore) Create(jobType, preferredNodeID string, req Requirements, payload map[string]any) (Job, error) {
	switch jobType {
	case JobTypeGPUSmoke, JobTypeInference:
	default:
		return Job{}, fmt.Errorf("unsupported job type %q", jobType)
	}
	if jobType == JobTypeInference {
		req.NormalizeModelFields()
		if req.ModelIdentity().Name == "" {
			return Job{}, fmt.Errorf("inference jobs require requirements.model or model_name")
		}
		if payload == nil {
			payload = map[string]any{}
		}
		if prompt, _ := payload["prompt"].(string); prompt == "" {
			return Job{}, fmt.Errorf("inference jobs require payload.prompt")
		}
		if !req.GPURequired && req.MinVRAMBytes == 0 {
			req.GPURequired = true
		}
	} else {
		req.NormalizeModelFields()
	}
	if !req.GPURequired && req.MinVRAMBytes == 0 && req.ModelIdentity().Name == "" {
		req = DefaultRequirements(jobType)
	}
	j := Job{
		ID:              newJobID(),
		Type:            jobType,
		Status:          JobQueued,
		PreferredNodeID: preferredNodeID,
		Requirements:    req,
		Payload:         payload,
		CreatedAt:       time.Now().UTC(),
	}
	js.mu.Lock()
	defer js.mu.Unlock()
	js.jobs[j.ID] = j
	_ = js.saveLocked()
	return j, nil
}

// Assign pins a queued job to a node (pending claim).
func (js *JobStore) Assign(id, nodeID string) bool {
	js.mu.Lock()
	defer js.mu.Unlock()
	j, ok := js.jobs[id]
	if !ok || j.Status != JobQueued {
		return false
	}
	j.Status = JobPending
	j.NodeID = nodeID
	js.jobs[id] = j
	_ = js.saveLocked()
	return true
}

// Claim takes a pending job already assigned to this node.
func (js *JobStore) Claim(nodeID string) (Job, bool) {
	js.mu.Lock()
	defer js.mu.Unlock()
	var chosen *Job
	for _, j := range js.jobs {
		if j.Status != JobPending {
			continue
		}
		if j.NodeID != nodeID {
			continue
		}
		jj := j
		if chosen == nil || jj.CreatedAt.Before(chosen.CreatedAt) {
			chosen = &jj
		}
	}
	if chosen == nil {
		return Job{}, false
	}
	chosen.Status = JobRunning
	chosen.StartedAt = time.Now().UTC()
	js.jobs[chosen.ID] = *chosen
	_ = js.saveLocked()
	return *chosen, true
}

func (js *JobStore) Complete(id string, ok bool, result map[string]any, errMsg string) (Job, bool) {
	js.mu.Lock()
	defer js.mu.Unlock()
	j, exists := js.jobs[id]
	if !exists {
		return Job{}, false
	}
	if ok {
		j.Status = JobSucceeded
		j.Error = ""
	} else {
		j.Status = JobFailed
		j.Error = errMsg
	}
	j.Result = result
	j.FinishedAt = time.Now().UTC()
	js.jobs[id] = j
	_ = js.saveLocked()
	return j, true
}

func (js *JobStore) Get(id string) (Job, bool) {
	js.mu.RLock()
	defer js.mu.RUnlock()
	j, ok := js.jobs[id]
	return j, ok
}

func (js *JobStore) List() []Job {
	js.mu.RLock()
	defer js.mu.RUnlock()
	out := make([]Job, 0, len(js.jobs))
	for _, j := range js.jobs {
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool {
		return out[i].CreatedAt.Before(out[k].CreatedAt)
	})
	return out
}

func (js *JobStore) ListByStatus(status string) []Job {
	js.mu.RLock()
	defer js.mu.RUnlock()
	out := make([]Job, 0)
	for _, j := range js.jobs {
		if j.Status == status {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(i, k int) bool {
		return out[i].CreatedAt.Before(out[k].CreatedAt)
	})
	return out
}

func (js *JobStore) CountByStatus(status string) int {
	js.mu.RLock()
	defer js.mu.RUnlock()
	n := 0
	for _, j := range js.jobs {
		if j.Status == status {
			n++
		}
	}
	return n
}

// FailRunningForNode fails in-flight jobs when a node goes offline.
// Phase 3 does not migrate work.
func (js *JobStore) FailRunningForNode(nodeID, errMsg string) int {
	js.mu.Lock()
	defer js.mu.Unlock()
	n := 0
	now := time.Now().UTC()
	for id, j := range js.jobs {
		if j.NodeID != nodeID {
			continue
		}
		if j.Status != JobRunning && j.Status != JobPending {
			continue
		}
		j.Status = JobFailed
		j.Error = errMsg
		j.FinishedAt = now
		js.jobs[id] = j
		n++
	}
	if n > 0 {
		_ = js.saveLocked()
	}
	return n
}

func (js *JobStore) load() error {
	b, err := os.ReadFile(js.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var jobs []Job
	if err := json.Unmarshal(b, &jobs); err != nil {
		return err
	}
	for _, j := range jobs {
		if j.ID == "" {
			continue
		}
		if j.Status == JobPending && j.NodeID == "" {
			j.Status = JobQueued
		}
		js.jobs[j.ID] = j
	}
	return nil
}

func (js *JobStore) saveLocked() error {
	jobs := make([]Job, 0, len(js.jobs))
	for _, j := range js.jobs {
		jobs = append(jobs, j)
	}
	b, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := js.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, js.path)
}

func newJobID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("job-%x", b)
}
