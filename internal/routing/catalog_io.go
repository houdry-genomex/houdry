package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// LoadCatalog reads a JSON array of CatalogEntry from path.
// If the file is missing, DefaultCatalog is returned.
func LoadCatalog(path string) ([]CatalogEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultCatalog(), nil
		}
		return nil, err
	}
	var entries []CatalogEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return DefaultCatalog(), nil
	}
	return entries, nil
}

// SaveCatalog writes entries as indented JSON.
func SaveCatalog(path string, entries []CatalogEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

// EnsureCatalogFile writes the default catalog if none exists.
func EnsureCatalogFile(path string) ([]CatalogEntry, error) {
	if _, err := os.Stat(path); err == nil {
		return LoadCatalog(path)
	}
	entries := DefaultCatalog()
	if err := SaveCatalog(path, entries); err != nil {
		return entries, err
	}
	return entries, nil
}
