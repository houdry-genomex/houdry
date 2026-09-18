// Package enrollment stores one-time control-plane join tokens.
package enrollment

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const fileName = "enrollment.json"
const tokenPrefix = "HDRY_"

type Record struct {
	ID         string    `json:"id"`
	TokenHash  string    `json:"token_hash"`
	Expires    time.Time `json:"expires"`
	Uses       int       `json:"uses"`
	CreatedAt  time.Time `json:"created_at"`
	ConsumedAt time.Time `json:"consumed_at,omitempty"`
	NodeID     string    `json:"node_id,omitempty"`
}

type Issued struct {
	ID      string
	Token   string
	Expires time.Time
	Uses    int
}

type Store struct {
	mu      sync.Mutex
	path    string
	records []Record
	ttl     time.Duration
	uses    int
}

func NewStore(dataDir string, ttl time.Duration, uses int) (*Store, error) {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if uses <= 0 {
		uses = 1
	}
	s := &Store{path: filepath.Join(dataDir, fileName), ttl: ttl, uses: uses}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	if err := s.loadLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Create() (Issued, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Issued{}, err
	}
	idRaw := make([]byte, 8)
	if _, err := rand.Read(idRaw); err != nil {
		return Issued{}, err
	}
	token := tokenPrefix + hex.EncodeToString(raw)
	rec := Record{
		ID:        hex.EncodeToString(idRaw),
		TokenHash: hashToken(token),
		Expires:   time.Now().UTC().Add(s.ttl),
		Uses:      s.uses,
		CreatedAt: time.Now().UTC(),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.loadLocked()
	s.records = append(s.records, rec)
	if err := s.saveLocked(); err != nil {
		return Issued{}, err
	}
	return Issued{ID: rec.ID, Token: token, Expires: rec.Expires, Uses: rec.Uses}, nil
}

// Consume validates and spends one use of token. nodeID is recorded on success.
func (s *Store) Consume(token, nodeID string) error {
	if token == "" {
		return fmt.Errorf("enrollment token is required")
	}
	want := hashToken(token)
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.loadLocked()
	for i := range s.records {
		r := &s.records[i]
		if r.TokenHash != want {
			continue
		}
		if now.After(r.Expires) {
			return fmt.Errorf("enrollment token expired")
		}
		if r.Uses <= 0 {
			return fmt.Errorf("enrollment token already used")
		}
		r.Uses--
		r.NodeID = nodeID
		if r.Uses == 0 {
			r.ConsumedAt = now
		}
		return s.saveLocked()
	}
	return fmt.Errorf("enrollment token is invalid")
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Store) loadLocked() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(b, &s.records)
}

func (s *Store) saveLocked() error {
	b, err := json.MarshalIndent(s.records, "", "  ")
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
