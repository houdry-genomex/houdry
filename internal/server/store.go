package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"houdry/internal/gpu"
	"houdry/internal/host"
	"houdry/internal/modelruntime"
)

// Node status values used by the control plane and agents.
const (
	StatusJoined   = "JOINED"   // inventory-only snapshot (Phase 1 gpu join)
	StatusReady    = "READY"    // agent heartbeating, idle
	StatusBusy     = "BUSY"     // agent running a job
	StatusDraining = "DRAINING" // no new jobs; finish current then leave
	StatusOffline  = "OFFLINE"  // heartbeat timed out or left
)

// Node is a machine registered with the Houdry control plane.
type Node struct {
	gpu.Inventory
	Status        string               `json:"status"`
	AgentVersion  string               `json:"agent_version,omitempty"`
	CurrentJobID  string               `json:"current_job_id,omitempty"`
	Resources     ResourceProfile      `json:"resources,omitempty"`
	Runtimes      []string             `json:"runtimes,omitempty"` // GPU runtimes (nvidia, inventory, …)
	ModelRuntimes []string             `json:"model_runtimes,omitempty"`
	Models        []modelruntime.Model `json:"models,omitempty"`
	JoinedAt      time.Time            `json:"joined_at"`
	LastSeen      time.Time            `json:"last_seen"`
	RemoteIP      string               `json:"remote_ip,omitempty"`
}

type Store struct {
	mu      sync.RWMutex
	path    string
	nodes   map[string]Node
	offline time.Duration
}

func NewStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		path:    filepath.Join(dataDir, "nodes.json"),
		nodes:   map[string]Node{},
		offline: 20 * time.Second,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Upsert(n Node) Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.nodes[n.NodeID]; ok {
		n.JoinedAt = existing.JoinedAt
		if n.Status == "" {
			n.Status = existing.Status
		}
		if n.AgentVersion == "" {
			n.AgentVersion = existing.AgentVersion
		}
		if n.CurrentJobID == "" && n.Status != StatusReady {
			n.CurrentJobID = existing.CurrentJobID
		}
		if len(n.Runtimes) == 0 {
			n.Runtimes = existing.Runtimes
		}
		if len(n.ModelRuntimes) == 0 {
			n.ModelRuntimes = existing.ModelRuntimes
		}
		if n.Models == nil {
			n.Models = existing.Models
		}
	} else if n.JoinedAt.IsZero() {
		n.JoinedAt = time.Now().UTC()
	}
	if n.Status == "" {
		n.Status = StatusJoined
	}
	n.LastSeen = time.Now().UTC()
	s.nodes[n.NodeID] = n
	_ = s.saveLocked()
	return n
}

func (s *Store) Heartbeat(n Node) (Node, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.nodes[n.NodeID]
	if !ok {
		return Node{}, false
	}
	existing.Inventory = n.Inventory
	if n.Resources.Static.CPUCores != 0 || len(n.Resources.Static.GPUs) > 0 {
		existing.Resources = n.Resources
	}
	if len(n.Runtimes) > 0 {
		existing.Runtimes = n.Runtimes
	}
	if len(n.ModelRuntimes) > 0 {
		existing.ModelRuntimes = n.ModelRuntimes
	}
	if n.Models != nil {
		existing.Models = n.Models
	}

	incoming := n.Status
	switch {
	case existing.Status == StatusDraining:
		// Stay draining until leave/timeout; keep CurrentJobID while finishing.
		if incoming == StatusOffline {
			existing.Status = StatusOffline
			existing.CurrentJobID = ""
		} else {
			existing.Status = StatusDraining
			existing.CurrentJobID = n.CurrentJobID
		}
	case incoming != "":
		existing.Status = incoming
		if incoming == StatusReady {
			existing.CurrentJobID = ""
		} else if n.CurrentJobID != "" {
			existing.CurrentJobID = n.CurrentJobID
		}
	case existing.Status == StatusOffline || existing.Status == StatusJoined:
		existing.Status = StatusReady
	}

	if n.AgentVersion != "" {
		existing.AgentVersion = n.AgentVersion
	}
	if n.RemoteIP != "" {
		existing.RemoteIP = n.RemoteIP
	}
	existing.LastSeen = time.Now().UTC()
	s.nodes[n.NodeID] = existing
	_ = s.saveLocked()
	return existing, true
}

func (s *Store) SetStatus(id, status, jobID string) (Node, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[id]
	if !ok {
		return Node{}, false
	}
	// Finishing a job while draining returns to DRAINING, not READY.
	if status == StatusReady && n.Status == StatusDraining {
		status = StatusDraining
	}
	n.Status = status
	n.CurrentJobID = jobID
	n.LastSeen = time.Now().UTC()
	s.nodes[id] = n
	_ = s.saveLocked()
	return n, true
}

func (s *Store) SetDrain(id string) (Node, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[id]
	if !ok {
		return Node{}, false
	}
	if n.Status == StatusBusy {
		n.Status = StatusDraining
		// keep CurrentJobID
	} else {
		n.Status = StatusDraining
		n.CurrentJobID = ""
	}
	n.LastSeen = time.Now().UTC()
	s.nodes[id] = n
	_ = s.saveLocked()
	return n, true
}

func (s *Store) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[id]; !ok {
		return false
	}
	delete(s.nodes, id)
	_ = s.saveLocked()
	return true
}

// MarkStaleOffline returns node IDs that newly went OFFLINE.
func (s *Store) MarkStaleOffline() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().UTC().Add(-s.offline)
	var changed []string
	for id, n := range s.nodes {
		if n.AgentVersion == "" {
			continue
		}
		if n.Status == StatusOffline {
			continue
		}
		if n.LastSeen.Before(cutoff) {
			n.Status = StatusOffline
			n.CurrentJobID = ""
			s.nodes[id] = n
			changed = append(changed, id)
		}
	}
	if len(changed) > 0 {
		_ = s.saveLocked()
	}
	return changed
}

func (s *Store) List() []Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		out = append(out, n)
	}
	return out
}

func (s *Store) Get(id string) (Node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.nodes[id]
	return n, ok
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var nodes []Node
	if err := json.Unmarshal(b, &nodes); err != nil {
		return err
	}
	for _, n := range nodes {
		if n.NodeID == "" {
			continue
		}
		if n.Status == "" {
			n.Status = StatusJoined
		}
		if len(n.Resources.Static.GPUs) == 0 && len(n.GPUs) > 0 {
			n.Resources = BuildProfile(n.Inventory, host.Resources{Arch: n.Host.Arch}, 0)
		}
		s.nodes[n.NodeID] = n
	}
	return nil
}

func (s *Store) saveLocked() error {
	nodes := make([]Node, 0, len(s.nodes))
	for _, n := range s.nodes {
		nodes = append(nodes, n)
	}
	b, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
