package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNodeNotFound is returned when a lookup by ID fails.
var ErrNodeNotFound = errors.New("nodes: node not found")

// ErrDuplicateNodeID is returned when a configuration repeats an ID.
var ErrDuplicateNodeID = errors.New("nodes: duplicate node id")

// Manager owns the configured nodes and answers selection queries.
// It is safe for concurrent use.
type Manager struct {
	mu    sync.RWMutex
	nodes []Node
	byID  map[string]Node
}

// NewManager validates the nodes and returns a manager. Duplicate IDs are
// rejected because results are keyed by node ID.
func NewManager(list []Node) (*Manager, error) {
	manager := &Manager{byID: make(map[string]Node, len(list))}
	for _, node := range list {
		if err := node.Validate(); err != nil {
			return nil, err
		}
		if _, exists := manager.byID[node.ID]; exists {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateNodeID, node.ID)
		}
		node.BaseURL = trimTrailingSlash(node.BaseURL)
		node.Protocol = normalizeProtocol(node.Protocol)
		manager.byID[node.ID] = node
		manager.nodes = append(manager.nodes, node)
	}
	sort.Slice(manager.nodes, func(i, j int) bool { return manager.nodes[i].ID < manager.nodes[j].ID })
	return manager, nil
}

// List returns every configured node ordered by ID.
func (m *Manager) List() []Node {
	m.mu.RLock()
	defer m.mu.RUnlock()
	listed := make([]Node, len(m.nodes))
	copy(listed, m.nodes)
	return listed
}

// Enabled returns the enabled nodes ordered by ID.
func (m *Manager) Enabled() []Node {
	m.mu.RLock()
	defer m.mu.RUnlock()
	enabled := make([]Node, 0, len(m.nodes))
	for _, node := range m.nodes {
		if node.Enabled {
			enabled = append(enabled, node)
		}
	}
	return enabled
}

// Get looks a node up by ID.
func (m *Manager) Get(id string) (Node, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	node, ok := m.byID[id]
	if !ok {
		return Node{}, fmt.Errorf("%w: %q", ErrNodeNotFound, id)
	}
	return node, nil
}

// Default returns the first enabled node ordered by ID.
func (m *Manager) Default() (Node, error) {
	enabled := m.Enabled()
	if len(enabled) == 0 {
		return Node{}, fmt.Errorf("%w: no enabled node is configured", ErrNodeNotFound)
	}
	return enabled[0], nil
}

// SetEnabled changes the enabled flag of one node.
func (m *Manager) SetEnabled(id string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	node, ok := m.byID[id]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNodeNotFound, id)
	}
	node.Enabled = enabled
	m.byID[id] = node
	for index := range m.nodes {
		if m.nodes[index].ID == id {
			m.nodes[index].Enabled = enabled
			break
		}
	}
	return nil
}

// Availability records the outcome of one node health check.
type Availability struct {
	NodeID     string        `json:"node_id"`
	Available  bool          `json:"available"`
	CheckedAt  time.Time     `json:"checked_at"`
	LatencyNs  time.Duration `json:"latency_ns"`
	StatusCode int           `json:"status_code,omitempty"`
	Error      string        `json:"error,omitempty"`
}

// Check performs a GET /health request against one node. It always returns a
// populated Availability; the error is non-nil when the node is unreachable or
// unhealthy so callers can stop on the first failure or keep checking others.
func (m *Manager) Check(ctx context.Context, id string, client *http.Client) (Availability, error) {
	node, err := m.Get(id)
	if err != nil {
		return Availability{}, err
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	availability := Availability{NodeID: node.ID}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, node.BaseURL+"/health", nil)
	if err != nil {
		availability.CheckedAt = time.Now().UTC()
		availability.Error = err.Error()
		return availability, err
	}
	start := time.Now()
	response, err := client.Do(request)
	if err != nil {
		availability.CheckedAt = time.Now().UTC()
		availability.Error = err.Error()
		return availability, fmt.Errorf("nodes: check %s: %w", node.ID, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
	}()
	availability.CheckedAt = time.Now().UTC()
	availability.LatencyNs = time.Since(start)
	availability.StatusCode = response.StatusCode
	if response.StatusCode != http.StatusOK {
		availability.Error = fmt.Sprintf("unexpected status %s", response.Status)
		return availability, fmt.Errorf("nodes: check %s: unexpected status %s", node.ID, response.Status)
	}
	var health struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&health); err != nil {
		availability.Error = err.Error()
		return availability, fmt.Errorf("nodes: check %s: decode health response: %w", node.ID, err)
	}
	if health.Status != "ok" {
		availability.Error = fmt.Sprintf("health status %q", health.Status)
		return availability, fmt.Errorf("nodes: check %s: health status %q", node.ID, health.Status)
	}
	availability.Available = true
	return availability, nil
}

func trimTrailingSlash(value string) string {
	for len(value) > 1 && value[len(value)-1] == '/' {
		value = value[:len(value)-1]
	}
	return value
}

func normalizeProtocol(value string) string {
	return strings.ToLower(value)
}
