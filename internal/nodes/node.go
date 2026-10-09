// Package nodes loads and validates GoSpeed test nodes.
//
// A node is a server that speaks the GoSpeed HTTP test API. v0.1.0 ships only a
// local node because it does not claim any public test server address that has
// not been verified.
package nodes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

// Protocol values a node may declare. Both are HTTP; https is HTTP over TLS.
const (
	ProtocolHTTP  = "http"
	ProtocolHTTPS = "https"
)

// Capability names a measurement a node supports.
type Capability string

const (
	CapabilityLatency  Capability = "latency"
	CapabilityDownload Capability = "download"
	CapabilityUpload   Capability = "upload"
)

// Node describes one test server.
type Node struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Country  string `json:"country,omitempty"`
	Region   string `json:"region,omitempty"`
	City     string `json:"city,omitempty"`
	BaseURL  string `json:"base_url"`
	Protocol string `json:"protocol"`
	Enabled  bool   `json:"enabled"`
	// Provider and Description are informational; they must stay accurate.
	Provider     string       `json:"provider,omitempty"`
	Description  string       `json:"description,omitempty"`
	Capabilities []Capability `json:"capabilities,omitempty"`
	// Local marks loopback test servers. The CLI labels their results as local
	// loopback measurements.
	Local bool `json:"local,omitempty"`
}

// Validate checks that a node is usable before it is registered or contacted.
func (n Node) Validate() error {
	if strings.TrimSpace(n.ID) == "" {
		return fmt.Errorf("node: missing id")
	}
	if strings.ContainsAny(n.ID, " \t\r\n") {
		return fmt.Errorf("node %q: id must not contain whitespace", n.ID)
	}
	if strings.TrimSpace(n.Name) == "" {
		return fmt.Errorf("node %q: missing name", n.ID)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(n.BaseURL), "/")
	if baseURL == "" {
		return fmt.Errorf("node %q: missing base_url", n.ID)
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("node %q: parse base_url: %w", n.ID, err)
	}
	if parsed.Host == "" || !parsed.IsAbs() {
		return fmt.Errorf("node %q: base_url %q must be absolute", n.ID, n.BaseURL)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("node %q: base_url must not contain a query or fragment", n.ID)
	}
	if parsed.User != nil {
		return fmt.Errorf("node %q: base_url must not embed credentials", n.ID)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != ProtocolHTTP && scheme != ProtocolHTTPS {
		return fmt.Errorf("node %q: unsupported url scheme %q", n.ID, parsed.Scheme)
	}
	protocol := strings.ToLower(strings.TrimSpace(n.Protocol))
	if protocol == "" {
		return fmt.Errorf("node %q: missing protocol", n.ID)
	}
	if protocol != ProtocolHTTP && protocol != ProtocolHTTPS {
		return fmt.Errorf("node %q: unsupported protocol %q", n.ID, n.Protocol)
	}
	if protocol != scheme {
		return fmt.Errorf("node %q: protocol %q does not match base_url scheme %q", n.ID, protocol, scheme)
	}
	for _, capability := range n.Capabilities {
		switch capability {
		case CapabilityLatency, CapabilityDownload, CapabilityUpload:
		default:
			return fmt.Errorf("node %q: unknown capability %q", n.ID, capability)
		}
	}
	return nil
}

// LocalDefault is the built-in loopback node used when no configuration file
// exists. It points at the server started by "gospeed server".
func LocalDefault() Node {
	return Node{
		ID:           "local",
		Name:         "Local Test Server",
		BaseURL:      "http://127.0.0.1:8080",
		Protocol:     ProtocolHTTP,
		Enabled:      true,
		Provider:     "GoSpeed",
		Description:  "Loopback test server started with: gospeed server",
		Capabilities: []Capability{CapabilityLatency, CapabilityDownload, CapabilityUpload},
		Local:        true,
	}
}

// File is the on-disk node configuration format.
type File struct {
	Nodes []Node `json:"nodes"`
}

// LoadFile reads and validates a node configuration file. Unknown fields are
// rejected so a typo cannot silently disable a configured limit.
func LoadFile(path string) ([]Node, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("nodes: read %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var file File
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("nodes: parse %s: %w", path, err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, fmt.Errorf("nodes: parse %s: %w", path, err)
	}
	if len(file.Nodes) == 0 {
		return nil, fmt.Errorf("nodes: %s does not define any nodes", path)
	}
	for _, node := range file.Nodes {
		if err := node.Validate(); err != nil {
			return nil, fmt.Errorf("nodes: %s: %w", path, err)
		}
	}
	return file.Nodes, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected data after the top level object")
		}
		return err
	}
	return nil
}
