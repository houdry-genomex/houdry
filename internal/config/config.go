package config

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const dirName = ".houdry"

type Config struct {
	Server string `json:"server,omitempty"`
	Token  string `json:"token,omitempty"`
	NodeID string `json:"node_id"`
}

func Dir() string {
	if v := os.Getenv("HOODRY_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, dirName)
}

func Path() string {
	return filepath.Join(Dir(), "config.json")
}

func Load() (*Config, error) {
	b, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", Path(), err)
	}
	return &c, nil
}

func (c *Config) Save() error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}

func (c *Config) EnsureNodeID() error {
	if c.NodeID != "" {
		return nil
	}
	c.NodeID = newNodeID()
	return c.Save()
}

func newNodeID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("node-%d", os.Getpid())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func BinDir() string {
	return filepath.Join(Dir(), "bin")
}
