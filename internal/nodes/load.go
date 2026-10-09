package nodes

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// DefaultConfigPaths are searched in order when no explicit path is given.
// configs/nodes.json is meant for local, untracked overrides; the example file
// is the read-only checked-in default.
var DefaultConfigPaths = []string{"configs/nodes.json", "configs/nodes.example.json"}

// LoadFirst loads the first existing configuration file among paths. A path
// that does not exist is skipped; found reports whether any file was loaded.
// When found is false the caller is expected to fall back to LocalDefault
// rather than to invent a node.
func LoadFirst(paths []string) (manager *Manager, path string, found bool, err error) {
	for _, candidate := range paths {
		if candidate == "" {
			continue
		}
		if _, statErr := os.Stat(candidate); statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				continue
			}
			return nil, "", false, fmt.Errorf("nodes: stat %s: %w", candidate, statErr)
		}
		store, openErr := OpenStore(candidate)
		if openErr != nil {
			return nil, "", false, openErr
		}
		return store.Manager(), candidate, true, nil
	}
	return nil, "", false, nil
}

// LoadDefault loads a node configuration the way the CLI and the desktop GUI
// both do: an explicit path wins, otherwise DefaultConfigPaths are searched in
// order, and finally the built-in loopback node is used. The returned path is
// empty when the built-in node is in use.
func LoadDefault(explicit string) (*Manager, string, error) {
	if explicit != "" {
		store, err := OpenStore(explicit)
		if err != nil {
			return nil, "", err
		}
		return store.Manager(), explicit, nil
	}
	manager, path, found, err := LoadFirst(DefaultConfigPaths)
	if err != nil {
		return nil, "", err
	}
	if found {
		return manager, path, nil
	}
	manager, err = NewManager([]Node{LocalDefault()})
	if err != nil {
		return nil, "", err
	}
	return manager, "", nil
}
