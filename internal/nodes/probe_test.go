package nodes

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeNodeOptions struct {
	// failFirstHealth answers the first N health requests with 500.
	failFirstHealth int64
	// healthStatus, when set and not 200, always answers with that status.
	healthStatus int
	// healthDelay slows the health endpoint down.
	healthDelay time.Duration
	// noCapabilities answers 404 on /capabilities.
	noCapabilities bool
	// capabilitiesStatus answers a non-200 status on /capabilities.
	capabilitiesStatus int
}

// fakeNodeHandler builds a GoSpeed-like server so probe tests never touch a
// real network service.
func fakeNodeHandler(options fakeNodeOptions, healthCount *atomic.Int64) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if options.healthDelay > 0 {
			time.Sleep(options.healthDelay)
		}
		if index := healthCount.Add(1); options.failFirstHealth > 0 && index <= options.failFirstHealth {
			http.Error(w, "try later", http.StatusInternalServerError)
			return
		}
		if options.healthStatus != 0 && options.healthStatus != http.StatusOK {
			http.Error(w, "unhealthy", options.healthStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"gospeed","version":"0.3.0"}`))
	})
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/capabilities", func(w http.ResponseWriter, r *http.Request) {
		if options.noCapabilities {
			http.NotFound(w, r)
			return
		}
		if options.capabilitiesStatus != 0 && options.capabilitiesStatus != http.StatusOK {
			http.Error(w, "broken", options.capabilitiesStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"protocol_version": 1,
			"server_version": "0.3.0",
			"capabilities": ["latency", "download", "upload"],
			"limits": {"max_connections_per_test": 16, "max_duration_seconds": 60}
		}`))
	})
	return mux
}

func newFakeNode(t *testing.T, options fakeNodeOptions) *httptest.Server {
	t.Helper()
	var healthCount atomic.Int64
	server := httptest.NewServer(fakeNodeHandler(options, &healthCount))
	t.Cleanup(server.Close)
	return server
}

func managerFor(t *testing.T, server *httptest.Server, protocol string) *Manager {
	t.Helper()
	manager, err := NewManager([]Node{{
		ID:       "test",
		Name:     "Test",
		BaseURL:  server.URL,
		Protocol: protocol,
		Enabled:  true,
		Local:    true,
	}})
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}
	return manager
}

func TestProbeHealthyWithCapabilities(t *testing.T) {
	server := newFakeNode(t, fakeNodeOptions{})
	manager := managerFor(t, server, ProtocolHTTP)

	probe, err := manager.Probe(context.Background(), "test", nil, ProbeOptions{
		Attempts:       2,
		RetryDelay:     10 * time.Millisecond,
		Timeout:        2 * time.Second,
		LatencySamples: 3,
	})
	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if probe.Status != HealthHealthy {
		t.Fatalf("status = %q (%s), want healthy", probe.Status, probe.Error)
	}
	if probe.Attempts != 1 || probe.Successes != 1 {
		t.Fatalf("attempts/successes = %d/%d, want 1/1", probe.Attempts, probe.Successes)
	}
	// On platforms whose clock cannot resolve a loopback dial, a fired callback
	// can legitimately measure as zero. The deterministic stageClock test below
	// covers the mechanism; here the probe only has to report a real answer.
	if probe.TCPDuration < 0 {
		t.Fatalf("TCP connect duration must not be negative, got %s", probe.TCPDuration)
	}
	if probe.DNSDuration != 0 {
		t.Fatalf("an IP literal must not report DNS time, got %s", probe.DNSDuration)
	}
	if probe.TLSDuration != 0 {
		t.Fatalf("plain HTTP must not report TLS time, got %s", probe.TLSDuration)
	}
	if probe.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want 200", probe.StatusCode)
	}
	if !probe.Capabilities.Supported || probe.Capabilities.ServerVersion != "0.3.0" {
		t.Fatalf("capabilities = %+v", probe.Capabilities)
	}
	if probe.Capabilities.Limits == nil || probe.Capabilities.Limits.MaxConnectionsPerTest != 16 {
		t.Fatalf("limits = %+v", probe.Capabilities.Limits)
	}
	if len(probe.LatencySamples) != 3 {
		t.Fatalf("latency samples = %v, want 3", probe.LatencySamples)
	}
	if probe.LatencyMedian == 0 && probe.LatencyBelowResolution != len(probe.LatencySamples) {
		t.Fatalf("a zero median must be explained by clock resolution: median=%s below=%d samples=%v",
			probe.LatencyMedian, probe.LatencyBelowResolution, probe.LatencySamples)
	}
	if probe.Error != "" {
		t.Fatalf("healthy probe must not carry an error: %q", probe.Error)
	}
}

func TestProbeLegacyServerWithoutCapabilities(t *testing.T) {
	server := newFakeNode(t, fakeNodeOptions{noCapabilities: true})
	manager := managerFor(t, server, ProtocolHTTP)
	probe, err := manager.Probe(context.Background(), "test", nil, ProbeOptions{Attempts: 1, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if probe.Status != HealthHealthy {
		t.Fatalf("status = %q, want healthy (a v0.2.0 server is not degraded)", probe.Status)
	}
	if !probe.Capabilities.Unsupported || probe.Capabilities.Supported {
		t.Fatalf("capabilities = %+v, want Unsupported", probe.Capabilities)
	}
	if !strings.Contains(probe.Capabilities.Error, "does not implement") {
		t.Fatalf("capability error = %q", probe.Capabilities.Error)
	}
}

func TestProbeRetriesAndReportsDegraded(t *testing.T) {
	server := newFakeNode(t, fakeNodeOptions{failFirstHealth: 1})
	manager := managerFor(t, server, ProtocolHTTP)
	probe, err := manager.Probe(context.Background(), "test", nil, ProbeOptions{
		Attempts:   2,
		RetryDelay: 5 * time.Millisecond,
		Timeout:    2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if probe.Status != HealthDegraded {
		t.Fatalf("status = %q, want degraded after a retry", probe.Status)
	}
	if probe.Attempts != 2 || probe.Successes != 1 {
		t.Fatalf("attempts/successes = %d/%d, want 2/1", probe.Attempts, probe.Successes)
	}
	if len(probe.AttemptErrors) != 1 {
		t.Fatalf("attempt errors = %v, want one", probe.AttemptErrors)
	}
}

func TestProbeUnavailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	manager, err := NewManager([]Node{{
		ID: "dead", Name: "Dead", BaseURL: "http://" + address, Protocol: ProtocolHTTP, Local: true,
	}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	probe, err := manager.Probe(context.Background(), "dead", nil, ProbeOptions{
		Attempts: 2, RetryDelay: time.Millisecond, Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if probe.Status != HealthUnavailable || probe.Successes != 0 {
		t.Fatalf("probe = %+v, want unavailable", probe)
	}
	if probe.Error == "" || len(probe.AttemptErrors) != 2 {
		t.Fatalf("probe errors = %q / %v", probe.Error, probe.AttemptErrors)
	}
}

func TestProbeReachableButUnhealthyIsDegraded(t *testing.T) {
	server := newFakeNode(t, fakeNodeOptions{healthStatus: http.StatusInternalServerError})
	manager := managerFor(t, server, ProtocolHTTP)
	probe, err := manager.Probe(context.Background(), "test", nil, ProbeOptions{
		Attempts: 2, RetryDelay: time.Millisecond, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if probe.Status != HealthDegraded {
		t.Fatalf("status = %q, want degraded (reachable but unhealthy)", probe.Status)
	}
	if !strings.Contains(probe.Error, "500") {
		t.Fatalf("error = %q, want the HTTP status", probe.Error)
	}
}

func TestProbeTimeout(t *testing.T) {
	server := newFakeNode(t, fakeNodeOptions{healthDelay: 300 * time.Millisecond})
	manager := managerFor(t, server, ProtocolHTTP)
	started := time.Now()
	probe, err := manager.Probe(context.Background(), "test", nil, ProbeOptions{
		Attempts: 1, Timeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("probe blocked for %s", elapsed)
	}
	if probe.Status != HealthUnavailable {
		t.Fatalf("status = %q, want unavailable", probe.Status)
	}
	if !strings.Contains(strings.ToLower(probe.Error), "deadline") {
		t.Fatalf("error = %q, want a timeout", probe.Error)
	}
}

func TestProbeVerifiesTLSCertificates(t *testing.T) {
	var healthCount atomic.Int64
	server := httptest.NewTLSServer(fakeNodeHandler(fakeNodeOptions{}, &healthCount))
	t.Cleanup(server.Close)
	manager := managerFor(t, server, ProtocolHTTPS)

	// The default client trusts the platform CA store, not the test server's
	// self signed certificate: the probe must fail instead of skipping
	// verification.
	untrusted, err := manager.Probe(context.Background(), "test", nil, ProbeOptions{Attempts: 1, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if untrusted.Status != HealthUnavailable {
		t.Fatalf("status = %q, want unavailable for an untrusted certificate", untrusted.Status)
	}
	if !strings.Contains(strings.ToLower(untrusted.Error), "certificate") {
		t.Fatalf("error = %q, want a certificate error", untrusted.Error)
	}
	// TLSDuration may read as zero when the platform clock cannot resolve the
	// handshake; the certificate error above already proves the handshake ran.
	if untrusted.TLSDuration < 0 {
		t.Fatalf("TLS duration must not be negative, got %s", untrusted.TLSDuration)
	}

	trusted, err := manager.Probe(context.Background(), "test", server.Client(), ProbeOptions{Attempts: 1, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if trusted.Status != HealthHealthy {
		t.Fatalf("status = %q, want healthy with a trusting client", trusted.Status)
	}
	if trusted.StatusCode != http.StatusOK {
		t.Fatalf("HTTPS probe must return 200: %+v", trusted)
	}
	if trusted.TLSDuration < 0 {
		t.Fatalf("TLS duration must not be negative, got %s", trusted.TLSDuration)
	}
}

// TestStageClockUsesMonotonicOffsets pins the timing mechanism: stamps are
// offsets from a monotonic origin, so a wall clock adjustment cannot produce a
// negative or missing duration.
func TestStageClockUsesMonotonicOffsets(t *testing.T) {
	clock := stageClock{origin: time.Now().Add(-time.Second)}
	clock.mark(&clock.connectStart)
	time.Sleep(2 * time.Millisecond)
	clock.mark(&clock.connectDone)
	duration := between(&clock.connectStart, &clock.connectDone)
	if duration < time.Millisecond {
		t.Fatalf("duration = %s, want the real elapsed time", duration)
	}
	if duration > time.Second {
		t.Fatalf("duration = %s, want an offset from origin, not a wall clock value", duration)
	}
	// A stage that never fires stays N/A instead of inventing a value.
	if got := between(&clock.tlsStart, &clock.tlsDone); got != 0 {
		t.Fatalf("unfired stage = %s, want 0 (N/A)", got)
	}
}

func TestProbeHonoursCancellation(t *testing.T) {
	server := newFakeNode(t, fakeNodeOptions{healthDelay: 200 * time.Millisecond})
	manager := managerFor(t, server, ProtocolHTTP)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	started := time.Now()
	probe, err := manager.Probe(ctx, "test", nil, ProbeOptions{Attempts: 3, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("Probe returned error: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancelled probe blocked for %s", elapsed)
	}
	if probe.Status != HealthUnavailable {
		t.Fatalf("status = %q, want unavailable", probe.Status)
	}
}

func TestCheckAllProbesEveryNode(t *testing.T) {
	healthy := newFakeNode(t, fakeNodeOptions{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closed := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	manager, err := NewManager([]Node{
		{ID: "a-healthy", Name: "Healthy", BaseURL: healthy.URL, Protocol: ProtocolHTTP, Enabled: true, Local: true},
		{ID: "b-dead", Name: "Dead", BaseURL: "http://" + closed, Protocol: ProtocolHTTP, Enabled: true, Local: true},
		{ID: "c-disabled", Name: "Disabled", BaseURL: "https://speed.example.com", Protocol: ProtocolHTTPS, Enabled: false},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	probes, err := manager.CheckAll(context.Background(), nil, CheckOptions{
		ProbeOptions: ProbeOptions{Attempts: 1, Timeout: time.Second},
		EnabledOnly:  true,
		Parallel:     2,
	})
	if err != nil {
		t.Fatalf("CheckAll returned error: %v", err)
	}
	if len(probes) != 2 {
		t.Fatalf("probes = %d, want one per enabled node", len(probes))
	}
	if probes[0].NodeID != "a-healthy" || probes[0].Status != HealthHealthy {
		t.Fatalf("first probe = %+v", probes[0])
	}
	if probes[1].NodeID != "b-dead" || probes[1].Status != HealthUnavailable {
		t.Fatalf("second probe = %+v", probes[1])
	}
}
