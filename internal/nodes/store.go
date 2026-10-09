package nodes

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"sync"
)

// Store owns a node configuration file and keeps the in-memory manager in sync
// with what is on disk. Every mutation is written atomically (temporary file +
// rename) so an interrupted process cannot leave a half written configuration.
type Store struct {
	path    string
	mu      sync.Mutex
	manager *Manager
}

// OpenStore loads an existing configuration file.
func OpenStore(path string) (*Store, error) {
	list, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	manager, err := NewManager(list)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, manager: manager}, nil
}

// OpenOrCreateStore loads a configuration file or starts an empty node list
// when the file does not exist yet.
func OpenOrCreateStore(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("nodes: stat %s: %w", path, err)
		}
		manager, err := NewManager(nil)
		if err != nil {
			return nil, err
		}
		return &Store{path: path, manager: manager}, nil
	}
	return OpenStore(path)
}

// Path returns the configuration file this store writes to.
func (s *Store) Path() string {
	return s.path
}

// Manager returns the in-memory node manager.
func (s *Store) Manager() *Manager {
	return s.manager
}

// Add validates and appends a node, then persists the new configuration.
func (s *Store) Add(node Node) error {
	return s.mutate(func(list []Node) ([]Node, error) {
		for _, existing := range list {
			if existing.ID == node.ID {
				return nil, fmt.Errorf("%w: %s", ErrDuplicateNodeID, node.ID)
			}
		}
		return append(list, node), nil
	})
}

// Update replaces an existing node with the same ID.
func (s *Store) Update(node Node) error {
	return s.mutate(func(list []Node) ([]Node, error) {
		for index, existing := range list {
			if existing.ID == node.ID {
				updated := make([]Node, len(list))
				copy(updated, list)
				updated[index] = node
				return updated, nil
			}
		}
		return nil, fmt.Errorf("%w: %q", ErrNodeNotFound, node.ID)
	})
}

// Remove deletes a node by ID.
func (s *Store) Remove(id string) error {
	return s.mutate(func(list []Node) ([]Node, error) {
		kept := make([]Node, 0, len(list))
		found := false
		for _, node := range list {
			if node.ID == id {
				found = true
				continue
			}
			kept = append(kept, node)
		}
		if !found {
			return nil, fmt.Errorf("%w: %q", ErrNodeNotFound, id)
		}
		return kept, nil
	})
}

// SetEnabled enables or disables one node.
func (s *Store) SetEnabled(id string, enabled bool) error {
	return s.mutate(func(list []Node) ([]Node, error) {
		for index, node := range list {
			if node.ID == id {
				updated := make([]Node, len(list))
				copy(updated, list)
				updated[index].Enabled = enabled
				return updated, nil
			}
		}
		return nil, fmt.Errorf("%w: %q", ErrNodeNotFound, id)
	})
}

// mutate applies a change to a copy of the node list, validates the result,
// persists it and only then swaps the in-memory state. A failed write leaves
// the previous configuration untouched.
func (s *Store) mutate(change func([]Node) ([]Node, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.manager.List()
	next, err := change(current)
	if err != nil {
		return err
	}
	if err := SaveFile(s.path, next); err != nil {
		return err
	}
	manager, err := NewManager(next)
	if err != nil {
		return err
	}
	s.manager = manager
	return nil
}

// SaveFile writes a node configuration atomically. The file is validated with
// the same rules as LoadFile, including the address policy, so a store can
// never persist a node it would refuse to load.
func SaveFile(path string, list []Node) error {
	seen := make(map[string]bool, len(list))
	for _, node := range list {
		if err := node.Validate(); err != nil {
			return err
		}
		if err := node.ValidateTarget(); err != nil {
			return err
		}
		if seen[node.ID] {
			return fmt.Errorf("%w: %s", ErrDuplicateNodeID, node.ID)
		}
		seen[node.ID] = true
	}
	data, err := json.MarshalIndent(File{Nodes: list}, "", "  ")
	if err != nil {
		return fmt.Errorf("nodes: encode %s: %w", path, err)
	}
	data = append(data, '\n')

	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("nodes: create temporary file in %s: %w", directory, err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if _, err := temporary.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("nodes: write %s: %w", temporaryPath, err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("nodes: sync %s: %w", temporaryPath, err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("nodes: close %s: %w", temporaryPath, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("nodes: replace %s: %w", path, err)
	}
	return nil
}
