package cli

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// fakeNodeServer mimics a v0.3.0 GoSpeed server for CLI tests: health, ping,
// capability negotiation and both transfer endpoints. Tests never touch a real
// network service.
func fakeNodeServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"gospeed","version":"0.3.0"}`))
	})
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/capabilities", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"protocol_version": 1,
			"server_version":   "0.3.0",
			"capabilities":     []string{"latency", "download", "upload"},
			"limits": map[string]any{
				"max_connections_per_test": 16,
				"max_duration_seconds":     60,
				"max_download_bytes":       int64(1 << 30),
				"max_upload_bytes":         int64(1 << 30),
			},
		})
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		block := make([]byte, 32<<10)
		flusher, _ := w.(http.Flusher)
		for {
			if r.Context().Err() != nil {
				return
			}
			if _, err := w.Write(block); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Millisecond):
			}
		}
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		received, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":         "ok",
			"bytes_received": received,
			"duration_ns":    int64(1),
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func closedAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return address
}

func writeNodeConfig(t *testing.T, list []nodes.Node) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nodes.json")
	if err := nodes.SaveFile(path, list); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}
	return path
}

func TestNodesListAndCheckSubcommands(t *testing.T) {
	server := fakeNodeServer(t)
	path := writeNodeConfig(t, []nodes.Node{{
		ID: "local", Name: "Local Test Server", BaseURL: server.URL, Protocol: nodes.ProtocolHTTP,
		Enabled: true, Local: true,
	}})

	app, stdout, stderr := newTestApp()
	if code := app.Run(context.Background(), []string{"nodes", "list", "--config", path}); code != 0 {
		t.Fatalf("nodes list exit = %d (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "local") || !strings.Contains(stdout.String(), "Config:") {
		t.Fatalf("nodes list output = %q", stdout.String())
	}

	app, stdout, stderr = newTestApp()
	if code := app.Run(context.Background(), []string{"nodes", "list", "--config", path, "--json"}); code != 0 {
		t.Fatalf("nodes list --json exit = %d (stderr=%s)", code, stderr.String())
	}
	var list []nodes.Node
	if err := json.Unmarshal(stdout.Bytes(), &list); err != nil {
		t.Fatalf("decode node list: %v", err)
	}
	if len(list) != 1 || list[0].ID != "local" {
		t.Fatalf("node list = %+v", list)
	}

	app, stdout, stderr = newTestApp()
	if code := app.Run(context.Background(), []string{"nodes", "check", "--config", path, "--json", "--samples", "2"}); code != 0 {
		t.Fatalf("nodes check exit = %d (stderr=%s)", code, stderr.String())
	}
	var probes []nodes.Probe
	if err := json.Unmarshal(stdout.Bytes(), &probes); err != nil {
		t.Fatalf("decode probes: %v", err)
	}
	if len(probes) != 1 || probes[0].Status != nodes.HealthHealthy {
		t.Fatalf("probes = %+v", probes)
	}
	if !probes[0].Capabilities.Supported || probes[0].Capabilities.ServerVersion != "0.3.0" {
		t.Fatalf("capabilities = %+v", probes[0].Capabilities)
	}

	app, stdout, stderr = newTestApp()
	if code := app.Run(context.Background(), []string{"nodes", "check", "--config", path}); code != 0 {
		t.Fatalf("nodes check text exit = %d (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "healthy") || !strings.Contains(stdout.String(), "0.3.0") {
		t.Fatalf("nodes check output = %q", stdout.String())
	}
}

func TestNodesCheckReportsUnavailableNode(t *testing.T) {
	path := writeNodeConfig(t, []nodes.Node{{
		ID: "dead", Name: "Dead", BaseURL: "http://" + closedAddress(t), Protocol: nodes.ProtocolHTTP,
		Enabled: true, Local: true,
	}})
	app, stdout, stderr := newTestApp()
	code := app.Run(context.Background(), []string{"nodes", "check", "--config", path, "--json", "--timeout", "1s"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr=%s)", code, stderr.String())
	}
	var probes []nodes.Probe
	if err := json.Unmarshal(stdout.Bytes(), &probes); err != nil {
		t.Fatalf("decode probes: %v", err)
	}
	if len(probes) != 1 || probes[0].Status != nodes.HealthUnavailable {
		t.Fatalf("probes = %+v", probes)
	}
}

// A host that answers TCP/HTTP but does not confirm GoSpeed health must be
// reported as degraded (not unavailable, not healthy) and must fail the check
// exit code.
func TestNodesCheckReportsDegradedNode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	path := writeNodeConfig(t, []nodes.Node{{
		ID: "not-gospeed", Name: "Not GoSpeed", BaseURL: server.URL, Protocol: nodes.ProtocolHTTP,
		Enabled: true, Local: true,
	}})
	app, stdout, stderr := newTestApp()
	code := app.Run(context.Background(), []string{"nodes", "check", "--config", path, "--json", "--timeout", "1s"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr=%s)", code, stderr.String())
	}
	var probes []nodes.Probe
	if err := json.Unmarshal(stdout.Bytes(), &probes); err != nil {
		t.Fatalf("decode probes: %v", err)
	}
	if len(probes) != 1 || probes[0].Status != nodes.HealthDegraded {
		t.Fatalf("probes = %+v, want one degraded probe", probes)
	}
	if probes[0].StatusCode != http.StatusNotFound {
		t.Fatalf("status code = %d, want 404", probes[0].StatusCode)
	}
	if !strings.Contains(probes[0].Error, "404") {
		t.Fatalf("error = %q, want the HTTP status", probes[0].Error)
	}
}

func TestNodesAutoSelectsHealthyNode(t *testing.T) {
	server := fakeNodeServer(t)
	path := writeNodeConfig(t, []nodes.Node{
		{ID: "a-healthy", Name: "Healthy", BaseURL: server.URL, Protocol: nodes.ProtocolHTTP, Enabled: true, Local: true},
		{ID: "b-dead", Name: "Dead", BaseURL: "http://" + closedAddress(t), Protocol: nodes.ProtocolHTTP, Enabled: true, Local: true},
		{ID: "c-disabled", Name: "Disabled", BaseURL: "https://speed.example.com", Protocol: nodes.ProtocolHTTPS, Enabled: false},
	})
	app, stdout, stderr := newTestApp()
	code := app.Run(context.Background(), []string{"nodes", "auto", "--config", path, "--json", "--timeout", "1s", "--samples", "2"})
	if code != 0 {
		t.Fatalf("exit = %d (stderr=%s)", code, stderr.String())
	}
	var selection nodes.Selection
	if err := json.Unmarshal(stdout.Bytes(), &selection); err != nil {
		t.Fatalf("decode selection: %v", err)
	}
	if selection.Node.ID != "a-healthy" || selection.Method != nodes.SelectionAuto {
		t.Fatalf("selection = %+v", selection)
	}
	if !strings.Contains(selection.Reason, "healthy") {
		t.Fatalf("reason = %q", selection.Reason)
	}
	// Unavailable nodes are probed and reported, but excluded from the
	// candidate ranking.
	if len(selection.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want only the usable node", selection.Candidates)
	}
	if selection.Candidates[0].Rank != 1 || selection.Candidates[0].Node.ID != "a-healthy" {
		t.Fatalf("first candidate = %+v", selection.Candidates[0])
	}

	app, stdout, stderr = newTestApp()
	if code := app.Run(context.Background(), []string{"nodes", "auto", "--config", path, "--timeout", "1s"}); code != 0 {
		t.Fatalf("text exit = %d (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Selected:") || !strings.Contains(stdout.String(), "Reason:") {
		t.Fatalf("auto output = %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "b-dead") || !strings.Contains(stdout.String(), "unavailable") {
		t.Fatalf("auto output must still show the failed node: %q", stdout.String())
	}
}

func TestNodesAutoFailsWhenEveryNodeIsUnavailable(t *testing.T) {
	path := writeNodeConfig(t, []nodes.Node{{
		ID: "dead", Name: "Dead", BaseURL: "http://" + closedAddress(t), Protocol: nodes.ProtocolHTTP,
		Enabled: true, Local: true,
	}})
	app, _, stderr := newTestApp()
	if code := app.Run(context.Background(), []string{"nodes", "auto", "--config", path, "--timeout", "1s"}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "no node is available") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestNodesMutationsPersist(t *testing.T) {
	path := writeNodeConfig(t, []nodes.Node{{
		ID: "local", Name: "Local", BaseURL: "http://127.0.0.1:8080", Protocol: nodes.ProtocolHTTP,
		Enabled: true, Local: true,
	}})
	run := func(args ...string) string {
		t.Helper()
		app, stdout, stderr := newTestApp()
		if code := app.Run(context.Background(), args); code != 0 {
			t.Fatalf("%v exit = %d (stderr=%s)", args, code, stderr.String())
		}
		return stdout.String()
	}

	run("nodes", "disable", "--config", path, "local")
	loaded, err := nodes.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Enabled {
		t.Fatalf("disable did not persist: %+v", loaded)
	}

	run("nodes", "enable", "--config", path, "local")
	loaded, err = nodes.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if !loaded[0].Enabled {
		t.Fatalf("enable did not persist: %+v", loaded)
	}

	run("nodes", "add", "--config", path, "--id", "extra", "--name", "Extra", "--url", "https://speed.example.com", "--provider", "Example")
	loaded, err = nodes.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(loaded) != 2 || loaded[1].ID != "extra" || loaded[1].Protocol != nodes.ProtocolHTTPS {
		t.Fatalf("add did not persist: %+v", loaded)
	}

	run("nodes", "remove", "--config", path, "extra")
	loaded, err = nodes.LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != "local" {
		t.Fatalf("remove did not persist: %+v", loaded)
	}

	// Duplicate IDs and unknown IDs are rejected with a non-zero exit code.
	app, _, stderr := newTestApp()
	if code := app.Run(context.Background(), []string{"nodes", "add", "--config", path, "--id", "local", "--name", "Dup", "--url", "https://speed.example.com"}); code != 1 {
		t.Fatalf("duplicate add exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "duplicate") {
		t.Fatalf("duplicate add stderr = %q", stderr.String())
	}
	app, _, stderr = newTestApp()
	if code := app.Run(context.Background(), []string{"nodes", "remove", "--config", path, "missing"}); code != 1 {
		t.Fatalf("missing remove exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Fatalf("missing remove stderr = %q", stderr.String())
	}
}

func TestNodesMutationRefusesExampleConfiguration(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	if err := os.MkdirAll(filepath.Join("configs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	example := filepath.Join("configs", "nodes.example.json")
	if err := nodes.SaveFile(example, []nodes.Node{{
		ID: "local", Name: "Local", BaseURL: "http://127.0.0.1:8080", Protocol: nodes.ProtocolHTTP,
		Enabled: true, Local: true,
	}}); err != nil {
		t.Fatalf("SaveFile: %v", err)
	}
	app, _, stderr := newTestApp()
	if code := app.Run(context.Background(), []string{"nodes", "disable", "--config", example, "local"}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "read-only example") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	// The example file must be untouched.
	loaded, err := nodes.LoadFile(example)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if !loaded[0].Enabled {
		t.Fatal("the example configuration was modified")
	}
}

func TestTestCommandRepeatJSON(t *testing.T) {
	server := fakeNodeServer(t)
	app, stdout, stderr := newTestApp()
	code := app.Run(context.Background(), []string{
		"test", "--server", server.URL, "--json",
		"--repeat", "2", "--max-bytes", "8388608",
		"--duration", "5s", "--timeout", "15s",
		"--latency-samples", "1", "--latency-interval", "1ms",
	})
	if code != 0 {
		t.Fatalf("exit = %d (stderr=%s)", code, stderr.String())
	}
	var batch speedtest.BatchResult
	if err := json.Unmarshal(stdout.Bytes(), &batch); err != nil {
		t.Fatalf("decode batch: %v (stdout=%q)", err, stdout.String())
	}
	if batch.Summary.Runs != 2 || batch.Summary.Completed != 2 {
		t.Fatalf("summary counts = %+v", batch.Summary)
	}
	if len(batch.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(batch.Results))
	}
	for _, result := range batch.Results {
		if result.Status != speedtest.StatusCompleted {
			t.Fatalf("run status = %q", result.Status)
		}
		if result.Download == nil || result.Download.Bytes != 8<<20 {
			t.Fatalf("download = %+v", result.Download)
		}
	}
	if batch.Summary.DownloadMbps.Samples != 2 {
		t.Fatalf("download statistics samples = %d, want 2", batch.Summary.DownloadMbps.Samples)
	}
}

func TestTestCommandRejectsInvalidRepeat(t *testing.T) {
	app, _, stderr := newTestApp()
	if code := app.Run(context.Background(), []string{"test", "--repeat", "0"}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "--repeat") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestTestCommandAutoSelectsNode(t *testing.T) {
	server := fakeNodeServer(t)
	path := writeNodeConfig(t, []nodes.Node{
		{ID: "a-healthy", Name: "Healthy", BaseURL: server.URL, Protocol: nodes.ProtocolHTTP, Enabled: true, Local: true},
		{ID: "b-dead", Name: "Dead", BaseURL: "http://" + closedAddress(t), Protocol: nodes.ProtocolHTTP, Enabled: true, Local: true},
	})
	app, stdout, stderr := newTestApp()
	code := app.Run(context.Background(), []string{
		"test", "--auto", "--config", path, "--json",
		"--max-bytes", "8388608", "--duration", "5s", "--timeout", "15s",
		"--latency-samples", "1", "--latency-interval", "1ms",
	})
	if code != 0 {
		t.Fatalf("exit = %d (stderr=%s)", code, stderr.String())
	}
	var result speedtest.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v (stdout=%q)", err, stdout.String())
	}
	if result.Target.ServerID != "a-healthy" {
		t.Fatalf("target = %+v, want the healthy node", result.Target)
	}
	if result.Target.SelectionMethod != string(nodes.SelectionAuto) {
		t.Fatalf("selection method = %q", result.Target.SelectionMethod)
	}
	if !strings.Contains(result.Target.SelectionReason, "healthy") {
		t.Fatalf("selection reason = %q", result.Target.SelectionReason)
	}
	if result.Status != speedtest.StatusCompleted {
		t.Fatalf("status = %q", result.Status)
	}
}
