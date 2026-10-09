package nodes

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveFileAndLoadRoundTrip(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "nodes.json")
	list := []Node{
		{ID: "local", Name: "Local", BaseURL: "http://127.0.0.1:8080", Protocol: ProtocolHTTP, Enabled: true, Local: true},
		{ID: "remote", Name: "Remote", BaseURL: "https://speed.example.com", Protocol: ProtocolHTTPS, Enabled: false},
	}
	if err := SaveFile(path, list); err != nil {
		t.Fatalf("SaveFile returned error: %v", err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile returned error: %v", err)
	}
	if len(loaded) != 2 || loaded[0].ID != "local" || loaded[1].BaseURL != "https://speed.example.com" {
		t.Fatalf("unexpected round trip: %+v", loaded)
	}
	matches, err := filepath.Glob(filepath.Join(directory, "*.tmp-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v", matches)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		t.Fatal("saved configuration must end with a newline")
	}
	var file File
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("saved configuration is not valid JSON: %v", err)
	}
}

func TestSaveFileRejectsUnsafeTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	unsafe := []Node{{ID: "meta", Name: "Metadata", BaseURL: "http://169.254.169.254", Protocol: ProtocolHTTP}}
	if err := SaveFile(path, unsafe); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("error = %v, want ErrUnsafeTarget", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an unsafe configuration must not be written")
	}
}

func TestStoreMutationsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	store, err := OpenOrCreateStore(path)
	if err != nil {
		t.Fatalf("OpenOrCreateStore returned error: %v", err)
	}
	if list := store.Manager().List(); len(list) != 0 {
		t.Fatalf("new store should start empty, got %+v", list)
	}

	local := Node{ID: "local", Name: "Local", BaseURL: "http://127.0.0.1:8080", Protocol: ProtocolHTTP, Enabled: true, Local: true}
	if err := store.Add(local); err != nil {
		t.Fatalf("Add(local) returned error: %v", err)
	}
	if err := store.Add(local); !errors.Is(err, ErrDuplicateNodeID) {
		t.Fatalf("duplicate Add error = %v, want ErrDuplicateNodeID", err)
	}
	remote := Node{ID: "remote", Name: "Remote", BaseURL: "https://speed.example.com", Protocol: ProtocolHTTPS, Enabled: true}
	if err := store.Add(remote); err != nil {
		t.Fatalf("Add(remote) returned error: %v", err)
	}
	if err := store.SetEnabled("remote", false); err != nil {
		t.Fatalf("SetEnabled returned error: %v", err)
	}
	remote.Name = "Renamed"
	remote.Enabled = false
	if err := store.Update(remote); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if err := store.Remove("local"); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}
	if err := store.Remove("missing"); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("Remove(missing) error = %v, want ErrNodeNotFound", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	list := reopened.Manager().List()
	if len(list) != 1 {
		t.Fatalf("persisted nodes = %+v, want only remote", list)
	}
	if list[0].ID != "remote" || list[0].Name != "Renamed" || list[0].Enabled {
		t.Fatalf("persisted node = %+v", list[0])
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v", matches)
	}
}

func TestLoadFileRejectsLoopbackWithoutLocalFlag(t *testing.T) {
	path := writeConfig(t, `{"nodes":[{"id":"sneaky","name":"Sneaky","base_url":"http://127.0.0.1:8080","protocol":"http"}]}`)
	if _, err := LoadFile(path); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("error = %v, want ErrUnsafeTarget", err)
	}
}

func TestStoreAllowsEmptyConfigurationAfterRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	store, err := OpenOrCreateStore(path)
	if err != nil {
		t.Fatalf("OpenOrCreateStore: %v", err)
	}
	if err := store.Add(Node{ID: "remote", Name: "Remote", BaseURL: "https://speed.example.com", Protocol: ProtocolHTTPS}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.Remove("remote"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("an empty configuration must stay loadable: %v", err)
	}
	if list := reopened.Manager().List(); len(list) != 0 {
		t.Fatalf("nodes = %+v, want none", list)
	}
}
