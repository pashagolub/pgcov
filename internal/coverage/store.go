package coverage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Store handles persistence of coverage data
type Store struct {
	filePath string
}

// NewStore creates a new coverage store
func NewStore(filePath string) *Store {
	return &Store{
		filePath: filePath,
	}
}

// Save writes coverage data to disk as JSON
func (s *Store) Save(coverage *Coverage) error {
	// Ensure directory exists
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Marshal coverage data to JSON
	data, err := json.MarshalIndent(coverage, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal coverage data: %w", err)
	}

	// Write to file
	if err := os.WriteFile(s.filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write coverage file: %w", err)
	}

	return nil
}

// Load reads coverage data from disk
func (s *Store) Load() (*Coverage, error) {
	// Check if file exists
	if _, err := os.Stat(s.filePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("coverage file not found: %s", s.filePath)
	}

	// Read file
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read coverage file: %w", err)
	}

	// Unmarshal JSON
	var coverage Coverage
	if err := json.Unmarshal(data, &coverage); err != nil {
		return nil, fmt.Errorf("failed to parse coverage file: %w", err)
	}

	// Reject data written by an incompatible build before any caller can act
	// on it. Doing this here covers every reader -- report, merge and any
	// future consumer -- rather than each of them remembering to check.
	if err := coverage.ValidateVersion(); err != nil {
		return nil, fmt.Errorf("%s: %w", s.filePath, err)
	}

	// Ensure Positions map is initialized
	if coverage.Positions == nil {
		coverage.Positions = make(map[string]PositionHits)
	}

	return &coverage, nil
}

// Exists checks if the coverage file exists
func (s *Store) Exists() bool {
	_, err := os.Stat(s.filePath)
	return err == nil
}

// Delete removes the coverage file
func (s *Store) Delete() error {
	if !s.Exists() {
		return nil
	}
	return os.Remove(s.filePath)
}

// Path returns the file path where coverage data is stored
func (s *Store) Path() string {
	return s.filePath
}


