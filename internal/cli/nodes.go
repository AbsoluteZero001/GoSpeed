package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
)

const (
	defaultProbeTimeout  = 5 * time.Second
	defaultProbeParallel = 4
)

// runNodes dispatches the node subcommands and keeps the v0.2.0 flag-only form
// (`gospeed nodes --check --json`) working unchanged.
func (a *App) runNodes(ctx context.Context, args []string) int {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		subcommand, rest := args[0], args[1:]
		switch subcommand {
		case "list":
			return a.runNodesList(rest)
		case "check":
			return a.runNodesCheck(ctx, rest)
		case "auto":
			return a.runNodesAuto(ctx, rest)
		case "add":
			return a.runNodesAdd(rest)
		case "remove":
			return a.runNodesRemove(rest)
		case "enable":
			return a.runNodesSetEnabled(rest, true)
		case "disable":
			return a.runNodesSetEnabled(rest, false)
		default:
			return a.fail("unknown nodes subcommand %q; expected list, check, auto, add, remove, enable or disable", subcommand)
		}
	}
	return a.runNodesLegacy(ctx, args)
}

// runNodesLegacy implements the v0.2.0 surface: list by default, --check for
// the health check.
func (a *App) runNodesLegacy(ctx context.Context, args []string) int {
	set := a.newFlagSet("gospeed nodes")
	configPath := set.String("config", "", "node configuration file (defaults to configs/nodes.json, then configs/nodes.example.json)")
	jsonOut := set.Bool("json", false, "write output as JSON")
	check := set.Bool("check", false, "request /health from every node (same as: gospeed nodes check)")
	timeout := set.Duration("timeout", defaultProbeTimeout, "timeout of one node attempt")
	if err := set.Parse(args); err != nil {
		return 2
	}
	if *check {
		return a.nodesCheck(ctx, *configPath, *jsonOut, *timeout, 0, 2, false)
	}
	return a.nodesList(*configPath, *jsonOut)
}

func (a *App) runNodesList(args []string) int {
	set := a.newFlagSet("gospeed nodes list")
	configPath := set.String("config", "", "node configuration file")
	jsonOut := set.Bool("json", false, "write the node list as JSON")
	if err := set.Parse(args); err != nil {
		return 2
	}
	return a.nodesList(*configPath, *jsonOut)
}

func (a *App) nodesList(configPath string, jsonOut bool) int {
	manager, path, err := a.loadNodes(configPath)
	if err != nil {
		return a.fail("%v", err)
	}
	list := manager.List()
	if jsonOut {
		return a.encodeJSON(list)
	}
	a.renderNodes(path, list)
	return 0
}

func (a *App) runNodesCheck(ctx context.Context, args []string) int {
	set := a.newFlagSet("gospeed nodes check")
	configPath := set.String("config", "", "node configuration file")
	jsonOut := set.Bool("json", false, "write the probes as JSON")
	timeout := set.Duration("timeout", defaultProbeTimeout, "timeout of one attempt")
	attempts := set.Int("attempts", 2, "attempts per node (1..5, a retry never means the node is permanently down)")
	samples := set.Int("samples", 0, "extra /ping samples per node (0..10)")
	enabledOnly := set.Bool("enabled-only", false, "skip disabled nodes")
	if err := set.Parse(args); err != nil {
		return 2
	}
	return a.nodesCheck(ctx, *configPath, *jsonOut, *timeout, *samples, *attempts, *enabledOnly)
}

func (a *App) nodesCheck(ctx context.Context, configPath string, jsonOut bool, timeout time.Duration, samples, attempts int, enabledOnly bool) int {
	if attempts < 1 || attempts > 5 {
		return a.fail("--attempts must be between 1 and 5")
	}
	if samples < 0 || samples > 10 {
		return a.fail("--samples must be between 0 and 10")
	}
	manager, path, err := a.loadNodes(configPath)
	if err != nil {
		return a.fail("%v", err)
	}
	list := manager.List()
	if enabledOnly {
		list = enabledNodes(list)
	}
	probes, err := manager.CheckAll(ctx, nil, nodes.CheckOptions{
		ProbeOptions: nodes.ProbeOptions{
			Attempts:       attempts,
			Timeout:        timeout,
			LatencySamples: samples,
		},
		Parallel: defaultProbeParallel,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return 130
		}
		return a.fail("%v", err)
	}
	if enabledOnly {
		probes = filterProbes(probes, list)
	}
	if jsonOut {
		a.encodeJSON(probes)
	} else {
		a.renderProbes(path, list, probes)
	}
	// Exit non-zero unless every probed node is confirmed healthy: a degraded
	// node is reachable but must not be reported as a clean check.
	for _, probe := range probes {
		if probe.Status != nodes.HealthHealthy {
			return 1
		}
	}
	return 0
}

func (a *App) runNodesAuto(ctx context.Context, args []string) int {
	set := a.newFlagSet("gospeed nodes auto")
	configPath := set.String("config", "", "node configuration file")
	jsonOut := set.Bool("json", false, "write the selection as JSON")
	timeout := set.Duration("timeout", defaultProbeTimeout, "timeout of one attempt")
	attempts := set.Int("attempts", 2, "attempts per node (1..5)")
	samples := set.Int("samples", 3, "HTTP RTT samples per node used for ranking (0..10)")
	all := set.Bool("all", false, "include disabled nodes")
	if err := set.Parse(args); err != nil {
		return 2
	}
	if *attempts < 1 || *attempts > 5 {
		return a.fail("--attempts must be between 1 and 5")
	}
	if *samples < 0 || *samples > 10 {
		return a.fail("--samples must be between 0 and 10")
	}
	manager, path, err := a.loadNodes(*configPath)
	if err != nil {
		return a.fail("%v", err)
	}
	list := manager.List()
	if !*all {
		list = enabledNodes(list)
	}
	if len(list) == 0 {
		return a.fail("no enabled node is configured; enable one with \"gospeed nodes enable <id>\"")
	}
	probes, err := manager.CheckAll(ctx, nil, nodes.CheckOptions{
		ProbeOptions: nodes.ProbeOptions{
			Attempts:       *attempts,
			Timeout:        *timeout,
			LatencySamples: *samples,
		},
		Parallel: defaultProbeParallel,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return 130
		}
		return a.fail("%v", err)
	}
	probes = filterProbes(probes, list)
	selection, err := nodes.SelectAuto(list, probes)
	if err != nil {
		if !*jsonOut {
			a.renderProbes(path, list, probes)
		}
		return a.fail("%v", err)
	}
	if *jsonOut {
		return a.encodeJSON(selection)
	}
	a.renderProbes(path, list, probes)
	fmt.Fprintln(a.Out)
	a.renderSelection(selection)
	return 0
}

func (a *App) runNodesAdd(args []string) int {
	set := a.newFlagSet("gospeed nodes add")
	configPath := set.String("config", "", "node configuration file (defaults to configs/nodes.json)")
	id := set.String("id", "", "unique node id")
	name := set.String("name", "", "human readable node name")
	rawURL := set.String("url", "", "absolute base URL, for example https://speed.example.com")
	provider := set.String("provider", "", "optional provider name")
	country := set.String("country", "", "optional country")
	region := set.String("region", "", "optional region")
	city := set.String("city", "", "optional city")
	description := set.String("description", "", "optional description")
	local := set.Bool("local", false, "mark the node as a loopback/local target")
	disabled := set.Bool("disabled", false, "add the node disabled")
	jsonOut := set.Bool("json", false, "write the updated node list as JSON")
	if err := set.Parse(args); err != nil {
		return 2
	}
	if *id == "" || *name == "" || *rawURL == "" {
		return a.fail("--id, --name and --url are required")
	}
	protocol, err := schemeOf(*rawURL)
	if err != nil {
		return a.fail("%v", err)
	}
	node := nodes.Node{
		ID:          *id,
		Name:        *name,
		BaseURL:     *rawURL,
		Protocol:    protocol,
		Enabled:     !*disabled,
		Provider:    *provider,
		Country:     *country,
		Region:      *region,
		City:        *city,
		Description: *description,
		Local:       *local,
	}
	store, err := a.writableNodeStore(*configPath)
	if err != nil {
		return a.fail("%v", err)
	}
	if err := store.Add(node); err != nil {
		return a.fail("%v", err)
	}
	if *jsonOut {
		return a.encodeJSON(store.Manager().List())
	}
	fmt.Fprintf(a.Out, "Added node %q to %s\n", node.ID, store.Path())
	return 0
}

func (a *App) runNodesRemove(args []string) int {
	set := a.newFlagSet("gospeed nodes remove")
	configPath := set.String("config", "", "node configuration file")
	jsonOut := set.Bool("json", false, "write the updated node list as JSON")
	if err := set.Parse(args); err != nil {
		return 2
	}
	id, code := a.requireNodeID(set, "remove")
	if code != 0 {
		return code
	}
	store, err := a.writableNodeStore(*configPath)
	if err != nil {
		return a.fail("%v", err)
	}
	if err := store.Remove(id); err != nil {
		return a.fail("%v", err)
	}
	if *jsonOut {
		return a.encodeJSON(store.Manager().List())
	}
	fmt.Fprintf(a.Out, "Removed node %q from %s\n", id, store.Path())
	return 0
}

func (a *App) runNodesSetEnabled(args []string, enabled bool) int {
	action := "enable"
	if !enabled {
		action = "disable"
	}
	set := a.newFlagSet("gospeed nodes " + action)
	configPath := set.String("config", "", "node configuration file")
	jsonOut := set.Bool("json", false, "write the updated node list as JSON")
	if err := set.Parse(args); err != nil {
		return 2
	}
	id, code := a.requireNodeID(set, action)
	if code != 0 {
		return code
	}
	store, err := a.writableNodeStore(*configPath)
	if err != nil {
		return a.fail("%v", err)
	}
	if err := store.SetEnabled(id, enabled); err != nil {
		return a.fail("%v", err)
	}
	if *jsonOut {
		return a.encodeJSON(store.Manager().List())
	}
	message := "Enabled"
	if !enabled {
		message = "Disabled"
	}
	fmt.Fprintf(a.Out, "%s node %q in %s\n", message, id, store.Path())
	return 0
}

// requireNodeID reads the positional node ID. Flags must come before it,
// because that is how the standard flag package parses a command line.
func (a *App) requireNodeID(set *flag.FlagSet, action string) (string, int) {
	positional := set.Args()
	if len(positional) != 1 {
		fmt.Fprintf(a.Err, "Usage: gospeed nodes %s [flags] <node-id>\n", action)
		return "", 2
	}
	return positional[0], 0
}

func (a *App) renderNodes(configPath string, list []nodes.Node) {
	if configPath == "" {
		configPath = "(built-in local node; no configuration file found)"
	}
	fmt.Fprintf(a.Out, "Config: %s\n\n", configPath)
	fmt.Fprintf(a.Out, "%-14s %-24s %-30s %-8s %-8s %s\n",
		"ID", "NAME", "BASE URL", "PROTOCOL", "ENABLED", "LOCATION")
	for _, node := range list {
		fmt.Fprintf(a.Out, "%-14s %-24s %-30s %-8s %-8t %s\n",
			node.ID, node.Name, node.BaseURL, node.Protocol, node.Enabled, locationOf(node))
	}
}

func (a *App) renderProbes(configPath string, list []nodes.Node, probes []nodes.Probe) {
	if configPath == "" {
		configPath = "(built-in local node; no configuration file found)"
	}
	byID := make(map[string]nodes.Probe, len(probes))
	for _, probe := range probes {
		byID[probe.NodeID] = probe
	}
	fmt.Fprintf(a.Out, "Config: %s\n\n", configPath)
	fmt.Fprintf(a.Out, "%-14s %-12s %-12s %-12s %-12s %-12s %-14s %s\n",
		"ID", "STATUS", "HTTP RTT", "DNS", "TCP", "TLS", "CAPABILITIES", "DETAIL")
	for _, node := range list {
		probe := byID[node.ID]
		detail := probe.Error
		if detail == "" && probe.Attempts > 1 {
			detail = fmt.Sprintf("needed %d attempts", probe.Attempts)
		}
		if probe.LatencyBelowResolution > 0 {
			note := fmt.Sprintf("%d sample(s) below clock resolution", probe.LatencyBelowResolution)
			if detail == "" {
				detail = note
			} else {
				detail += "; " + note
			}
		}
		fmt.Fprintf(a.Out, "%-14s %-12s %-12s %-12s %-12s %-12s %-14s %s\n",
			node.ID,
			probe.Status,
			stageDuration(probe.HTTPRTT),
			stageDuration(probe.DNSDuration),
			stageDuration(probe.TCPDuration),
			stageDuration(probe.TLSDuration),
			capabilityLabel(probe.Capabilities),
			detail)
	}
}

func (a *App) renderSelection(selection nodes.Selection) {
	fmt.Fprintf(a.Out, "Selected: %s (%s)\n", selection.Node.Name, selection.Node.ID)
	fmt.Fprintf(a.Out, "Reason:   %s\n", selection.Reason)
	fmt.Fprintf(a.Out, "Note:     %s\n", selection.Note)
	if len(selection.Candidates) == 0 {
		return
	}
	fmt.Fprintln(a.Out)
	fmt.Fprintf(a.Out, "%-4s %-14s %-12s %-16s %s\n", "RANK", "ID", "STATUS", "HTTP RTT (MEDIAN)", "REASON")
	for _, candidate := range selection.Candidates {
		latency := "N/A"
		if candidate.LatencyNs > 0 {
			latency = nodes.FormatDuration(candidate.LatencyNs)
		}
		fmt.Fprintf(a.Out, "%-4d %-14s %-12s %-16s %s\n",
			candidate.Rank, candidate.Node.ID, candidate.Probe.Status, latency, candidate.Reason)
	}
}

func enabledNodes(list []nodes.Node) []nodes.Node {
	enabled := make([]nodes.Node, 0, len(list))
	for _, node := range list {
		if node.Enabled {
			enabled = append(enabled, node)
		}
	}
	return enabled
}

// filterProbes keeps only the probes of the requested nodes, in list order.
func filterProbes(probes []nodes.Probe, list []nodes.Node) []nodes.Probe {
	byID := make(map[string]nodes.Probe, len(probes))
	for _, probe := range probes {
		byID[probe.NodeID] = probe
	}
	filtered := make([]nodes.Probe, 0, len(list))
	for _, node := range list {
		if probe, ok := byID[node.ID]; ok {
			filtered = append(filtered, probe)
			continue
		}
		filtered = append(filtered, nodes.Probe{NodeID: node.ID, Status: nodes.HealthUnknown})
	}
	return filtered
}

func stageDuration(value time.Duration) string {
	if value <= 0 {
		return "N/A"
	}
	return nodes.FormatDuration(value)
}

func capabilityLabel(info nodes.CapabilityInfo) string {
	if info.Unsupported {
		return "legacy"
	}
	if !info.Supported {
		return "no"
	}
	version := info.ServerVersion
	if version == "" {
		version = fmt.Sprintf("protocol %d", info.ProtocolVersion)
	}
	return "yes (" + version + ")"
}

// schemeOf derives the protocol from a base URL so a node added through the
// CLI cannot declare a protocol that contradicts its URL.
func schemeOf(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return "", fmt.Errorf("--url must be an absolute http(s) URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != nodes.ProtocolHTTP && scheme != nodes.ProtocolHTTPS {
		return "", fmt.Errorf("--url scheme %q is not supported", scheme)
	}
	return scheme, nil
}

func locationOf(node nodes.Node) string {
	parts := make([]string, 0, 3)
	for _, part := range []string{node.Country, node.Region, node.City} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		if node.Local {
			return "local"
		}
		return "N/A"
	}
	return strings.Join(parts, "/")
}
