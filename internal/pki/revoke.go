package pki

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

const revokedFile = "revoked.json"

// Revocation is a durable serial denylist for node certificates.
type Revocation struct {
	mu      sync.Mutex
	path    string
	serials map[string]string // serial hex -> node_id
}

func OpenRevocation(pkiDir string) (*Revocation, error) {
	r := &Revocation{path: filepath.Join(pkiDir, revokedFile), serials: map[string]string{}}
	if err := r.loadLocked(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Revocation) loadLocked() error {
	b, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var rec []struct {
		Serial string `json:"serial"`
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return err
	}
	r.serials = map[string]string{}
	for _, row := range rec {
		r.serials[row.Serial] = row.NodeID
	}
	return nil
}

func (r *Revocation) Revoke(serialHex, nodeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.loadLocked()
	r.serials[serialHex] = nodeID
	return r.saveLocked()
}

func (r *Revocation) IsRevoked(serialHex string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.loadLocked()
	_, ok := r.serials[serialHex]
	return ok
}

func (r *Revocation) saveLocked() error {
	type row struct {
		Serial string `json:"serial"`
		NodeID string `json:"node_id"`
	}
	out := make([]row, 0, len(r.serials))
	for s, n := range r.serials {
		out = append(out, row{Serial: s, NodeID: n})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}
