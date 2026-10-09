package nodes

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeNodeConfig(t *testing.T, path string, list []Node) {
	t.Helper()
	if err := SaveFile(path, list); err != nil {
		t.Fatalf("SaveFile(%s): %v", path, err)
	}
}

func TestLoadFirstSkipsMissingFiles(t *testing.T) {
	directory := t.TempDir()
	missing := filepath.Join(directory, "missing.json")
	present := filepath.Join(directory, "nodes.json")
	writeNodeConfig(t, present, []Node{LocalDefault()})

	manager, path, found, err := LoadFirst([]string{missing, present})
	if err != nil {
		t.Fatalf("LoadFirst: %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if path != present {
		t.Fatalf("path = %q, want %q", path, present)
	}
	if len(manager.List()) != 1 || manager.List()[0].ID != "local" {
		t.Fatalf("unexpected nodes: %+v", manager.List())
	}
}

func TestLoadFirstReportsNoConfig(t *testing.T) {
	directory := t.TempDir()
	manager, path, found, err := LoadFirst([]string{filepath.Join(directory, "missing.json")})
	if err != nil {
		t.Fatalf("LoadFirst: %v", err)
	}
	if found || manager != nil || path != "" {
		t.Fatalf("expected an empty result, got manager=%v path=%q found=%v", manager, path, found)
	}
}

func TestLoadDefaultFallsBackToLocalNode(t *testing.T) {
	directory := t.TempDir()
	manager, path, err := LoadDefault(filepath.Join(directory, "does-not-exist.json"))
	if err == nil {
		t.Fatalf("expected an error for a missing explicit config, got manager=%v path=%q", manager, path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want a not-exist error", err)
	}
}

func TestLoadDefaultExplicitPath(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "nodes.json")
	node := LocalDefault()
	node.ID = "lan"
	node.Name = "LAN server"
	node.BaseURL = "http://192.168.1.10:8080"
	node.Local = false
	writeNodeConfig(t, path, []Node{node})

	manager, loaded, err := LoadDefault(path)
	if err != nil {
		t.Fatalf("LoadDefault: %v", err)
	}
	if loaded != path {
		t.Fatalf("path = %q, want %q", loaded, path)
	}
	if len(manager.List()) != 1 || manager.List()[0].ID != "lan" {
		t.Fatalf("unexpected nodes: %+v", manager.List())
	}
}
