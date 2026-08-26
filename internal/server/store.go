package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"houdry/internal/gpu"
)

// Node is a machine that has joined the Houdry server.
type Node struct {
	gpu.Inventory
	JoinedAt time.Time `json:"joined_at"`
	LastSeen time.Time `json:"last_seen"`
	RemoteIP string    `json:"remote_ip,omitempty"`
}

type Store struct {
	mu    sync.RWMutex
	path  string
	nodes map[string]Node
}

func NewStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		path:  filepath.Join(dataDir, "nodes.json"),
		nodes: map[string]Node{},
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
	} else if n.JoinedAt.IsZero() {
		n.JoinedAt = time.Now().UTC()
	}
	n.LastSeen = time.Now().UTC()
	s.nodes[n.NodeID] = n
	_ = s.saveLocked()
	return n
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
		if n.NodeID != "" {
			s.nodes[n.NodeID] = n
		}
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
