package cli

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
)

func (a *App) runNodes(ctx context.Context, args []string) int {
	set := a.newFlagSet("gospeed nodes")
	configPath := set.String("config", "", "node configuration file (defaults to configs/nodes.json, then configs/nodes.example.json)")
	jsonOut := set.Bool("json", false, "write the node list as JSON")
	check := set.Bool("check", false, "request /health from every node")
	timeout := set.Duration("timeout", 5*time.Second, "timeout of one node health check")
	if err := set.Parse(args); err != nil {
		return 2
	}

	manager, err := a.loadNodeManager(*configPath)
	if err != nil {
		return a.fail("%v", err)
	}
	list := manager.List()
	if !*check {
		if *jsonOut {
			return a.encodeJSON(list)
		}
		a.renderNodes(list)
		return 0
	}

	client := &http.Client{Timeout: *timeout}
	availability := make([]nodes.Availability, 0, len(list))
	exitCode := 0
	for _, node := range list {
		result, checkErr := manager.Check(ctx, node.ID, client)
		availability = append(availability, result)
		if checkErr != nil {
			exitCode = 1
		}
	}
	if *jsonOut {
		return a.encodeJSON(availability)
	}
	a.renderNodeChecks(list, availability)
	return exitCode
}

func (a *App) renderNodes(list []nodes.Node) {
	fmt.Fprintf(a.Out, "%-14s %-24s %-28s %-8s %-7s %s\n",
		"ID", "NAME", "BASE URL", "PROTOCOL", "ENABLED", "LOCATION")
	for _, node := range list {
		fmt.Fprintf(a.Out, "%-14s %-24s %-28s %-8s %-7t %s\n",
			node.ID, node.Name, node.BaseURL, node.Protocol, node.Enabled, locationOf(node))
	}
}

func (a *App) renderNodeChecks(list []nodes.Node, availability []nodes.Availability) {
	byID := make(map[string]nodes.Availability, len(availability))
	for _, item := range availability {
		byID[item.NodeID] = item
	}
	fmt.Fprintf(a.Out, "%-14s %-24s %-28s %-10s %-12s %s\n",
		"ID", "NAME", "BASE URL", "HEALTH", "LATENCY", "DETAIL")
	for _, node := range list {
		item, ok := byID[node.ID]
		health := "N/A"
		latency := "N/A"
		detail := "not checked"
		if ok {
			detail = item.Error
			if item.Available {
				health = "ok"
				detail = ""
			} else {
				health = "unreachable"
			}
			if item.LatencyNs > 0 {
				latency = fmt.Sprintf("%.2f ms", float64(item.LatencyNs)/float64(time.Millisecond))
			}
		}
		fmt.Fprintf(a.Out, "%-14s %-24s %-28s %-10s %-12s %s\n",
			node.ID, node.Name, node.BaseURL, health, latency, detail)
	}
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
	joined := parts[0]
	for _, part := range parts[1:] {
		joined += "/" + part
	}
	return joined
}
