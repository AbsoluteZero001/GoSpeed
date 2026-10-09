package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

func newTestApp() (*App, *bytes.Buffer, *bytes.Buffer) {
	var stdout, stderr bytes.Buffer
	return NewApp(&stdout, &stderr), &stdout, &stderr
}

func TestRunVersion(t *testing.T) {
	app, stdout, _ := newTestApp()
	if code := app.Run(context.Background(), []string{"version"}); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "GoSpeed 0.1.0") {
		t.Fatalf("version output = %q", stdout.String())
	}
}

func TestRunHelpAndUnknownCommand(t *testing.T) {
	app, stdout, _ := newTestApp()
	if code := app.Run(context.Background(), []string{"help"}); code != 0 {
		t.Fatalf("help exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "gospeed test") {
		t.Fatalf("help output = %q", stdout.String())
	}

	app, _, stderr := newTestApp()
	if code := app.Run(context.Background(), []string{"nope"}); code != 2 {
		t.Fatalf("unknown command exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q", stderr.String())
	}

	app, _, stderr = newTestApp()
	if code := app.Run(context.Background(), nil); code != 2 {
		t.Fatalf("no-argument exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestNodesCommandJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	content := `{"nodes":[{"id":"local","name":"Local Test Server","base_url":"http://127.0.0.1:8080","protocol":"http","enabled":true,"local":true}]}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	app, stdout, stderr := newTestApp()
	code := app.Run(context.Background(), []string{"nodes", "--config", path, "--json"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr=%s)", code, stderr.String())
	}
	var list []nodes.Node
	if err := json.Unmarshal(stdout.Bytes(), &list); err != nil {
		t.Fatalf("decode node list: %v", err)
	}
	if len(list) != 1 || list[0].ID != "local" {
		t.Fatalf("node list = %+v", list)
	}
}

func TestTestCommandReportsFailureWithoutServer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	path := filepath.Join(t.TempDir(), "nodes.json")
	content := fmt.Sprintf(`{"nodes":[{"id":"dead","name":"Dead Node","base_url":"http://%s","protocol":"http","enabled":true,"local":true}]}`, address)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	app, stdout, stderr := newTestApp()
	code := app.Run(context.Background(), []string{
		"test", "--config", path, "--json",
		"--duration", "1s", "--timeout", "2s",
		"--latency-samples", "1", "--latency-interval", "1ms",
		"--warmup=false",
	})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (stderr=%s)", code, stderr.String())
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result JSON: %v (stdout=%q)", err, stdout.String())
	}
	if result["status"] != "failed" {
		t.Fatalf("status = %v, want failed", result["status"])
	}
	if message, _ := result["error_message"].(string); message == "" {
		t.Fatal("failed result must carry an error message")
	}
	target, _ := result["target"].(map[string]any)
	if target["server_id"] != "dead" {
		t.Fatalf("target = %v", target)
	}
}

func TestTestCommandRejectsUnknownNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nodes.json")
	content := `{"nodes":[{"id":"local","name":"Local Test Server","base_url":"http://127.0.0.1:8080","protocol":"http","enabled":true}]}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	app, _, stderr := newTestApp()
	if code := app.Run(context.Background(), []string{"test", "--config", path, "--server", "missing"}); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "available nodes: local") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:       "0 B",
		512:     "512 B",
		1536:    "1.50 KiB",
		1 << 20: "1.00 MiB",
	}
	for input, want := range cases {
		if got := humanBytes(input); got != want {
			t.Fatalf("humanBytes(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestNodeTargetAndURLLoopbackDetection(t *testing.T) {
	target := nodeTarget(nodes.Node{
		ID:       "local",
		Name:     "Local Test Server",
		BaseURL:  "http://127.0.0.1:8080",
		Protocol: nodes.ProtocolHTTP,
	})
	if !target.Local {
		t.Fatal("loopback node must be marked local even when the flag is missing")
	}
	fromURL, err := targetFromURL("https://example.com/")
	if err != nil {
		t.Fatalf("targetFromURL returned error: %v", err)
	}
	if fromURL.Protocol != speedtest.ProtocolHTTPS || fromURL.Local {
		t.Fatalf("targetFromURL = %+v", fromURL)
	}
	if _, err := targetFromURL("ftp://example.com"); err == nil {
		t.Fatal("unsupported scheme must be rejected")
	}
}
