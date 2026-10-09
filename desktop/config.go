package main

import (
	"os"
	"path/filepath"

	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
)

// Node configuration sources reported to the UI.
const (
	configSourceFile    = "config"
	configSourceBuiltin = "builtin"
)

// configSearchPaths returns the candidate configuration files in priority
// order: first relative to the current working directory, then relative to the
// executable, so both "run from the repository" and a portable layout
// (GoSpeed.exe plus configs/nodes.json) resolve the same file.
func configSearchPaths() []string {
	roots := make([]string, 0, 2)
	if cwd, err := os.Getwd(); err == nil {
		roots = append(roots, cwd)
	}
	if executable, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Dir(executable))
	}
	seen := make(map[string]bool, len(roots)*len(nodes.DefaultConfigPaths))
	paths := make([]string, 0, len(roots)*len(nodes.DefaultConfigPaths))
	for _, root := range roots {
		for _, relative := range nodes.DefaultConfigPaths {
			candidate := filepath.Join(root, relative)
			if seen[candidate] {
				continue
			}
			seen[candidate] = true
			paths = append(paths, candidate)
		}
	}
	return paths
}

// loadNodeManager loads the node configuration exactly like the CLI does and
// falls back to the built-in loopback node when no file exists. It never
// invents a node or starts a server.
func loadNodeManager() (*nodes.Manager, string, error) {
	manager, path, found, err := nodes.LoadFirst(configSearchPaths())
	if err != nil {
		return nil, "", err
	}
	if found {
		return manager, path, nil
	}
	manager, err = nodes.NewManager([]nodes.Node{nodes.LocalDefault()})
	if err != nil {
		return nil, "", err
	}
	return manager, "", nil
}

func configSource(path string) string {
	if path == "" {
		return configSourceBuiltin
	}
	return configSourceFile
}
