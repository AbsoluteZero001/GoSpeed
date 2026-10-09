package nodes

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nodes.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadFileValid(t *testing.T) {
	path := writeConfig(t, `{
	  "nodes": [
	    {
	      "id": "local",
	      "name": "Local Test Server",
	      "base_url": "http://127.0.0.1:8080/",
	      "protocol": "http",
	      "enabled": true,
	      "capabilities": ["latency", "download", "upload"],
	      "local": true
	    }
	  ]
	}`)
	list, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile returned error: %v", err)
	}
	if len(list) != 1 || list[0].ID != "local" {
		t.Fatalf("unexpected node list: %+v", list)
	}
}

func TestLoadFileRejectsProblems(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"unknown field", `{"nodes":[{"id":"a","name":"A","base_url":"http://127.0.0.1","protocol":"http","typo":true}]}`},
		{"relative url", `{"nodes":[{"id":"a","name":"A","base_url":"127.0.0.1:8080","protocol":"http"}]}`},
		{"protocol mismatch", `{"nodes":[{"id":"a","name":"A","base_url":"http://127.0.0.1","protocol":"https"}]}`},
		{"unknown capability", `{"nodes":[{"id":"a","name":"A","base_url":"http://127.0.0.1","protocol":"http","capabilities":["icmp"]}]}`},
		{"trailing data", `{"nodes":[{"id":"a","name":"A","base_url":"http://127.0.0.1","protocol":"http"}]} {}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := LoadFile(writeConfig(t, testCase.content)); err == nil {
				t.Fatal("expected a configuration error")
			}
		})
	}
}

func TestLoadFileMissing(t *testing.T) {
	if _, err := LoadFile(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestManagerLookupAndDefault(t *testing.T) {
	manager, err := NewManager([]Node{
		{ID: "b", Name: "B", BaseURL: "http://127.0.0.1:9002", Protocol: ProtocolHTTP, Enabled: false},
		{ID: "a", Name: "A", BaseURL: "http://127.0.0.1:9001/", Protocol: ProtocolHTTP, Enabled: true},
	})
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}
	list := manager.List()
	if len(list) != 2 || list[0].ID != "a" || list[1].ID != "b" {
		t.Fatalf("List must be sorted by ID: %+v", list)
	}
	if list[0].BaseURL != "http://127.0.0.1:9001" {
		t.Fatalf("base url = %q, want the trailing slash removed", list[0].BaseURL)
	}
	enabled := manager.Enabled()
	if len(enabled) != 1 || enabled[0].ID != "a" {
		t.Fatalf("Enabled = %+v", enabled)
	}
	if _, err := manager.Default(); err != nil {
		t.Fatalf("Default returned error: %v", err)
	}
	if node, err := manager.Get("b"); err != nil || node.ID != "b" {
		t.Fatalf("Get(b) = %+v, %v", node, err)
	}
	if _, err := manager.Get("missing"); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrNodeNotFound", err)
	}
	if err := manager.SetEnabled("b", true); err != nil {
		t.Fatalf("SetEnabled returned error: %v", err)
	}
	if node, _ := manager.Get("b"); !node.Enabled {
		t.Fatal("SetEnabled did not update the node")
	}
	if err := manager.SetEnabled("missing", true); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("SetEnabled(missing) error = %v, want ErrNodeNotFound", err)
	}
}

func TestManagerRejectsDuplicatesAndInvalidNodes(t *testing.T) {
	duplicates := []Node{
		{ID: "a", Name: "A", BaseURL: "http://127.0.0.1:1", Protocol: ProtocolHTTP},
		{ID: "a", Name: "A again", BaseURL: "http://127.0.0.1:2", Protocol: ProtocolHTTP},
	}
	if _, err := NewManager(duplicates); !errors.Is(err, ErrDuplicateNodeID) {
		t.Fatalf("error = %v, want ErrDuplicateNodeID", err)
	}
	if _, err := NewManager([]Node{{ID: "a", Name: "A", BaseURL: "http://127.0.0.1:1", Protocol: "ftp"}}); err == nil {
		t.Fatal("unsupported protocol must be rejected")
	}
}

func TestNodeValidateRejectsProblems(t *testing.T) {
	cases := []struct {
		name string
		node Node
	}{
		{"missing id", Node{Name: "A", BaseURL: "http://127.0.0.1", Protocol: ProtocolHTTP}},
		{"id with whitespace", Node{ID: "a b", Name: "A", BaseURL: "http://127.0.0.1", Protocol: ProtocolHTTP}},
		{"missing name", Node{ID: "a", BaseURL: "http://127.0.0.1", Protocol: ProtocolHTTP}},
		{"missing url", Node{ID: "a", Name: "A", Protocol: ProtocolHTTP}},
		{"missing protocol", Node{ID: "a", Name: "A", BaseURL: "http://127.0.0.1"}},
		{"credentials in url", Node{ID: "a", Name: "A", BaseURL: "http://user:pass@127.0.0.1", Protocol: ProtocolHTTP}},
		{"query in url", Node{ID: "a", Name: "A", BaseURL: "http://127.0.0.1?x=1", Protocol: ProtocolHTTP}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.node.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestLocalDefault(t *testing.T) {
	node := LocalDefault()
	if err := node.Validate(); err != nil {
		t.Fatalf("LocalDefault must be valid: %v", err)
	}
	if !node.Local || !node.Enabled {
		t.Fatalf("LocalDefault = %+v, want an enabled local node", node)
	}
}

func TestManagerCheck(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"gospeed","version":"0.1.0"}`))
	}))
	defer healthy.Close()

	unhealthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer unhealthy.Close()

	closedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedAddress := closedListener.Addr().String()
	if err := closedListener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	manager, err := NewManager([]Node{
		{ID: "healthy", Name: "Healthy", BaseURL: healthy.URL, Protocol: ProtocolHTTP},
		{ID: "unhealthy", Name: "Unhealthy", BaseURL: unhealthy.URL, Protocol: ProtocolHTTP},
		{ID: "closed", Name: "Closed", BaseURL: "http://" + closedAddress, Protocol: ProtocolHTTP},
	})
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	availability, err := manager.Check(context.Background(), "healthy", client)
	if err != nil || !availability.Available {
		t.Fatalf("healthy check = %+v, %v", availability, err)
	}
	if availability.LatencyNs <= 0 {
		t.Fatalf("healthy check latency = %s, want positive", availability.LatencyNs)
	}
	if availability, err := manager.Check(context.Background(), "unhealthy", client); err == nil || availability.Available {
		t.Fatalf("unhealthy check = %+v, %v; want a failure", availability, err)
	}
	if availability, err := manager.Check(context.Background(), "closed", client); err == nil || availability.Available {
		t.Fatalf("closed check = %+v, %v; want a failure", availability, err)
	}
	if _, err := manager.Check(context.Background(), "missing", client); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("missing node error = %v, want ErrNodeNotFound", err)
	}
}
