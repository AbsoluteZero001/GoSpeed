// Package cli implements the gospeed command line client.
//
// The CLI is a thin presentation layer: it parses flags, selects a node and
// hands the measurement to internal/speedtest. No measurement logic lives
// here.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
	"github.com/AbsoluteZero001/GoSpeed/internal/version"
)

const usageText = `GoSpeed - cross platform network speed test

Usage:
  gospeed test [flags]      run a speed test against a node
  gospeed server [flags]    start the local test server
  gospeed nodes [flags]     list configured nodes
  gospeed version           print version information
  gospeed help              print this help

Run "gospeed <command> --help" for the flags of a command.
`

// defaultNodeConfigPaths are searched in order when --config is not given.
// configs/nodes.json is meant for local, untracked overrides.
var defaultNodeConfigPaths = []string{"configs/nodes.json", "configs/nodes.example.json"}

// App is the command line application. It writes to explicit writers so tests
// can capture the output.
type App struct {
	Out io.Writer
	Err io.Writer
}

// NewApp returns an App writing to out and err. Nil writers are discarded.
func NewApp(out, err io.Writer) *App {
	if out == nil {
		out = io.Discard
	}
	if err == nil {
		err = io.Discard
	}
	return &App{Out: out, Err: err}
}

// Run dispatches one command line and returns the process exit code.
func (a *App) Run(ctx context.Context, args []string) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(args) == 0 {
		fmt.Fprint(a.Err, usageText)
		return 2
	}
	switch args[0] {
	case "test":
		return a.runTest(ctx, args[1:])
	case "server":
		return a.runServer(ctx, args[1:])
	case "nodes":
		return a.runNodes(ctx, args[1:])
	case "version", "--version", "-v":
		fmt.Fprintf(a.Out, "GoSpeed %s\n", version.String())
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(a.Out, usageText)
		return 0
	default:
		fmt.Fprintf(a.Err, "Error: unknown command %q\n\n%s", args[0], usageText)
		return 2
	}
}

func (a *App) newFlagSet(name string) *flag.FlagSet {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(a.Err)
	set.Usage = func() {
		fmt.Fprintf(a.Err, "Usage of %s:\n", name)
		set.PrintDefaults()
	}
	return set
}

// fail prints a consistent error line and returns exit code 1.
func (a *App) fail(format string, args ...any) int {
	fmt.Fprintf(a.Err, "Error: "+format+"\n", args...)
	return 1
}

func (a *App) encodeJSON(value any) int {
	encoder := json.NewEncoder(a.Out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return a.fail("encode json: %v", err)
	}
	return 0
}

// loadNodeManager loads a node configuration. Without an explicit path it
// tries the local override, then the checked-in example, and finally falls
// back to the built-in loopback node so the CLI works in a fresh checkout.
func (a *App) loadNodeManager(path string) (*nodes.Manager, error) {
	if path != "" {
		list, err := nodes.LoadFile(path)
		if err != nil {
			return nil, err
		}
		return nodes.NewManager(list)
	}
	for _, candidate := range defaultNodeConfigPaths {
		if _, err := os.Stat(candidate); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("nodes: stat %s: %w", candidate, err)
		}
		list, err := nodes.LoadFile(candidate)
		if err != nil {
			return nil, err
		}
		return nodes.NewManager(list)
	}
	return nodes.NewManager([]nodes.Node{nodes.LocalDefault()})
}

// resolveTarget turns a --server value (node ID or absolute URL) into the
// target the engine expects.
func (a *App) resolveTarget(reference, configPath string) (speedtest.Target, error) {
	reference = strings.TrimSpace(reference)
	if strings.Contains(reference, "://") {
		return targetFromURL(reference)
	}
	manager, err := a.loadNodeManager(configPath)
	if err != nil {
		return speedtest.Target{}, err
	}
	var node nodes.Node
	if reference == "" {
		node, err = manager.Default()
		if err != nil {
			return speedtest.Target{}, err
		}
	} else {
		node, err = manager.Get(reference)
		if err != nil {
			return speedtest.Target{}, fmt.Errorf("%w (available nodes: %s)", err, nodeIDList(manager.List()))
		}
	}
	return nodeTarget(node), nil
}

func nodeIDList(list []nodes.Node) string {
	if len(list) == 0 {
		return "none"
	}
	ids := make([]string, 0, len(list))
	for _, node := range list {
		ids = append(ids, node.ID)
	}
	return strings.Join(ids, ", ")
}

// nodeTarget maps configuration onto the engine's minimal target type.
func nodeTarget(node nodes.Node) speedtest.Target {
	protocol := speedtest.ProtocolHTTP
	if strings.EqualFold(node.Protocol, nodes.ProtocolHTTPS) {
		protocol = speedtest.ProtocolHTTPS
	}
	return speedtest.Target{
		ID:       node.ID,
		Name:     node.Name,
		BaseURL:  node.BaseURL,
		Protocol: protocol,
		Local:    node.Local || isLoopbackHost(urlHost(node.BaseURL)),
	}
}

func targetFromURL(raw string) (speedtest.Target, error) {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return speedtest.Target{}, fmt.Errorf("%w: %q is not an absolute http(s) url", speedtest.ErrInvalidTarget, raw)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != string(speedtest.ProtocolHTTP) && scheme != string(speedtest.ProtocolHTTPS) {
		return speedtest.Target{}, fmt.Errorf("%w: %q", speedtest.ErrUnsupportedProtocol, scheme)
	}
	return speedtest.Target{
		ID:       "custom",
		Name:     parsed.Host,
		BaseURL:  strings.TrimRight(raw, "/"),
		Protocol: speedtest.Protocol(scheme),
		Local:    isLoopbackHost(parsed.Hostname()),
	}, nil
}

func urlHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// isLoopbackHost reports whether a host name or literal address is loopback.
func isLoopbackHost(host string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(host))
	if trimmed == "" {
		return false
	}
	if trimmed == "localhost" {
		return true
	}
	if address := net.ParseIP(trimmed); address != nil {
		return address.IsLoopback()
	}
	return false
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		return false
	}
	return isLoopbackHost(host)
}

// humanBytes formats a byte count with binary units, which are the correct
// units for file sizes. Rates are reported in Mbps and decimal MB/s instead.
func humanBytes(value int64) string {
	if value < 0 {
		return "N/A"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	size := float64(value)
	index := 0
	for size >= 1024 && index < len(units)-1 {
		size /= 1024
		index++
	}
	if index == 0 {
		return fmt.Sprintf("%d %s", value, units[index])
	}
	return fmt.Sprintf("%.2f %s", size, units[index])
}
