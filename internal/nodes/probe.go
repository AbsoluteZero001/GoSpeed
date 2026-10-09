package nodes

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/version"
)

// HealthStatus is the outcome of a node probe. A single failed attempt never
// means "permanently unavailable": the probe retries and records how it
// decided.
type HealthStatus string

const (
	HealthUnknown     HealthStatus = "unknown"
	HealthHealthy     HealthStatus = "healthy"
	HealthDegraded    HealthStatus = "degraded"
	HealthUnavailable HealthStatus = "unavailable"
)

// ServerLimits mirrors the limits a server advertises on GET /capabilities.
type ServerLimits struct {
	MaxConnectionsPerTest int   `json:"max_connections_per_test,omitempty"`
	MaxConcurrentTests    int   `json:"max_concurrent_tests,omitempty"`
	MaxDurationSeconds    int   `json:"max_duration_seconds,omitempty"`
	MaxDownloadBytes      int64 `json:"max_download_bytes,omitempty"`
	MaxUploadBytes        int64 `json:"max_upload_bytes,omitempty"`
}

// CapabilityInfo is the negotiated server description. Supported=false means
// the server either does not implement GET /capabilities (Error explains why)
// or could not be asked at all; it does not by itself mean the node is down.
type CapabilityInfo struct {
	Supported bool `json:"supported"`
	// Unsupported is true when the server answered 404/405: an older GoSpeed
	// without the endpoint. That is not a health failure.
	Unsupported     bool          `json:"unsupported_endpoint,omitempty"`
	ProtocolVersion int           `json:"protocol_version,omitempty"`
	ServerVersion   string        `json:"server_version,omitempty"`
	Capabilities    []string      `json:"capabilities,omitempty"`
	Limits          *ServerLimits `json:"limits,omitempty"`
	Error           string        `json:"error,omitempty"`
}

// Probe is the dynamic health information of one node. It is deliberately
// separate from Node: probes are measurements and must never be written back
// into the static configuration file.
type Probe struct {
	NodeID    string       `json:"node_id"`
	Status    HealthStatus `json:"status"`
	CheckedAt time.Time    `json:"checked_at"`
	// Attempts is how many attempts were needed, Successes how many succeeded.
	Attempts  int `json:"attempts"`
	Successes int `json:"successes"`
	// Stage durations of the first successful attempt. Zero means the stage was
	// not observed (for example TLS on plain HTTP, or DNS when the address is
	// an IP literal) and must be rendered as N/A.
	DNSDuration   time.Duration `json:"dns_duration_ns"`
	TCPDuration   time.Duration `json:"tcp_duration_ns"`
	TLSDuration   time.Duration `json:"tls_duration_ns"`
	HTTPRTT       time.Duration `json:"http_rtt_ns"`
	StatusCode    int           `json:"status_code,omitempty"`
	ServerVersion string        `json:"server_version,omitempty"`
	// LatencySamples are the extra /ping samples used by node selection.
	LatencySamples  []time.Duration `json:"latency_samples_ns,omitempty"`
	LatencyFailures int             `json:"latency_failures"`
	// LatencyBelowResolution counts samples that read as zero because the
	// platform clock cannot resolve the round trip. Zero means "N/A", not
	// "instantaneous".
	LatencyBelowResolution int            `json:"latency_below_resolution"`
	LatencyMedian          time.Duration  `json:"latency_median_ns"`
	Capabilities           CapabilityInfo `json:"capabilities"`
	Error                  string         `json:"error,omitempty"`
	AttemptErrors          []string       `json:"attempt_errors,omitempty"`
}

// SelectionLatency returns the best available latency estimate: the median of
// the extra ping samples when they exist, otherwise the health request RTT.
func (p Probe) SelectionLatency() (time.Duration, bool) {
	if p.LatencyMedian > 0 {
		return p.LatencyMedian, true
	}
	if p.HTTPRTT > 0 {
		return p.HTTPRTT, true
	}
	return 0, false
}

// ProbeOptions configures one node probe.
type ProbeOptions struct {
	// Attempts is the total number of attempts, including the first one. It is
	// clamped to 1..5; the default is 2.
	Attempts int
	// RetryDelay is the pause between attempts.
	RetryDelay time.Duration
	// Timeout bounds a single attempt.
	Timeout time.Duration
	// LatencySamples adds that many GET /ping samples after a successful health
	// check; selection uses their median. It is clamped to 0..10.
	LatencySamples int
}

// DefaultProbeOptions returns the probe defaults used by the CLI.
func DefaultProbeOptions() ProbeOptions {
	return ProbeOptions{
		Attempts:   2,
		RetryDelay: 250 * time.Millisecond,
		Timeout:    5 * time.Second,
	}
}

func (o ProbeOptions) withDefaults() ProbeOptions {
	if o.Attempts <= 0 {
		o.Attempts = 2
	}
	if o.Attempts > 5 {
		o.Attempts = 5
	}
	if o.RetryDelay < 0 {
		o.RetryDelay = 0
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Second
	}
	if o.LatencySamples < 0 {
		o.LatencySamples = 0
	}
	if o.LatencySamples > 10 {
		o.LatencySamples = 10
	}
	return o
}

// CheckOptions configures a multi node check.
type CheckOptions struct {
	ProbeOptions
	// EnabledOnly skips disabled nodes.
	EnabledOnly bool
	// Parallel bounds how many nodes are probed at the same time (1..8).
	Parallel int
}

// Probe performs a staged health check against one node.
//
// The returned error is only non-nil when the probe could not run at all (for
// example an unknown node ID); an unreachable node is reported inside the
// Probe with Status=unavailable so callers can keep checking other nodes.
func (m *Manager) Probe(ctx context.Context, id string, client *http.Client, options ProbeOptions) (Probe, error) {
	node, err := m.Get(id)
	if err != nil {
		return Probe{}, err
	}
	options = options.withDefaults()
	if client == nil {
		client = defaultProbeClient(options.Timeout)
	}
	probe := Probe{NodeID: node.ID, Status: HealthUnknown}

	reachable := false
	var lastErr error
	for attempt := 1; attempt <= options.Attempts; attempt++ {
		if attempt > 1 && options.RetryDelay > 0 {
			if err := sleepContext(ctx, options.RetryDelay); err != nil {
				lastErr = err
				break
			}
		}
		attemptCtx, cancel := context.WithTimeout(ctx, options.Timeout)
		outcome := probeOnce(attemptCtx, node, client)
		cancel()
		probe.Attempts = attempt
		if outcome.reachable {
			reachable = true
		}
		// Keep the timings of the most recent attempt, successful or not, so a
		// failed TLS handshake or a timeout is still diagnosable. HTTPRTT is
		// only a real HTTP round trip when a response actually arrived.
		probe.DNSDuration = outcome.dns
		probe.TCPDuration = outcome.tcp
		probe.TLSDuration = outcome.tls
		probe.HTTPRTT = 0
		if outcome.reachable {
			probe.HTTPRTT = outcome.rtt
		}
		probe.StatusCode = outcome.statusCode
		if outcome.err == nil && outcome.healthOK {
			probe.Successes++
			probe.ServerVersion = outcome.serverVersion
			probe.Capabilities = outcome.capabilities
			break
		}
		lastErr = outcome.err
		if outcome.err != nil {
			probe.AttemptErrors = append(probe.AttemptErrors, outcome.err.Error())
		}
		if ctx.Err() != nil {
			break
		}
	}

	probe.CheckedAt = time.Now().UTC()
	switch {
	case probe.Successes > 0 && probe.Attempts == 1 && !probe.capabilitiesFailed():
		probe.Status = HealthHealthy
	case probe.Successes > 0:
		probe.Status = HealthDegraded
	case reachable:
		// The host answered but the health endpoint did not confirm a working
		// server: degraded, not "permanently unavailable".
		probe.Status = HealthDegraded
	default:
		probe.Status = HealthUnavailable
	}
	if probe.Status != HealthHealthy {
		if lastErr != nil {
			probe.Error = lastErr.Error()
		} else if probe.Capabilities.Error != "" {
			probe.Error = probe.Capabilities.Error
		}
	}

	if options.LatencySamples > 0 && probe.Successes > 0 {
		samples, failures := pingSamples(ctx, client, node, options.LatencySamples, options.Timeout)
		probe.LatencySamples = samples
		probe.LatencyFailures = failures
		// A loopback round trip can be shorter than the platform clock
		// resolution and measure as zero. Those samples are counted and kept,
		// but the median is computed over the measurable ones instead of
		// claiming a 0.00 ms median.
		measurable := make([]time.Duration, 0, len(samples))
		for _, sample := range samples {
			if sample > 0 {
				measurable = append(measurable, sample)
			} else {
				probe.LatencyBelowResolution++
			}
		}
		if len(measurable) > 0 {
			probe.LatencyMedian = medianDuration(measurable)
		} else if len(samples) == 0 {
			if probe.Status == HealthHealthy {
				probe.Status = HealthDegraded
			}
			if probe.Error == "" {
				probe.Error = fmt.Sprintf("latency sampling failed (%d attempts)", failures)
			}
		}
	}
	return probe, nil
}

// CheckAll probes the configured nodes with bounded parallelism and returns
// the probes in node ID order. The error is only non-nil when ctx was
// cancelled; per node failures are part of each Probe.
func (m *Manager) CheckAll(ctx context.Context, client *http.Client, options CheckOptions) ([]Probe, error) {
	list := m.List()
	if options.EnabledOnly {
		filtered := make([]Node, 0, len(list))
		for _, node := range list {
			if node.Enabled {
				filtered = append(filtered, node)
			}
		}
		list = filtered
	}
	if client == nil {
		probeOptions := options.ProbeOptions.withDefaults()
		client = defaultProbeClient(probeOptions.Timeout)
	}
	parallel := options.Parallel
	if parallel <= 0 {
		parallel = 4
	}
	if parallel > 8 {
		parallel = 8
	}

	results := make([]Probe, len(list))
	semaphore := make(chan struct{}, parallel)
	var wait sync.WaitGroup
	for index, node := range list {
		wait.Add(1)
		semaphore <- struct{}{}
		go func(index int, node Node) {
			defer wait.Done()
			defer func() { <-semaphore }()
			probe, err := m.Probe(ctx, node.ID, client, options.ProbeOptions)
			if err != nil {
				probe = Probe{NodeID: node.ID, Status: HealthUnknown, Error: err.Error(), CheckedAt: time.Now().UTC()}
			}
			results[index] = probe
		}(index, node)
	}
	wait.Wait()
	if err := ctx.Err(); err != nil {
		return results, err
	}
	return results, nil
}

// probeOutcome is the raw result of one attempt.
type probeOutcome struct {
	dns           time.Duration
	tcp           time.Duration
	tls           time.Duration
	rtt           time.Duration
	statusCode    int
	serverVersion string
	capabilities  CapabilityInfo
	reachable     bool
	healthOK      bool
	err           error
}

// stageClock collects the httptrace timestamps of one attempt. Timestamps are
// monotonic offsets from origin, so a system clock adjustment cannot turn a
// measured stage into a negative or zero duration. Atomics keep the callbacks
// race free because httptrace may invoke them from the dialer goroutine rather
// than the caller.
type stageClock struct {
	origin       time.Time
	dnsStart     atomic.Int64
	dnsDone      atomic.Int64
	connectStart atomic.Int64
	connectDone  atomic.Int64
	tlsStart     atomic.Int64
	tlsDone      atomic.Int64
	firstByte    atomic.Int64
}

func (c *stageClock) mark(target *atomic.Int64) {
	target.CompareAndSwap(0, time.Since(c.origin).Nanoseconds())
}

func between(start, end *atomic.Int64) time.Duration {
	from, to := start.Load(), end.Load()
	if from == 0 || to == 0 || to < from {
		return 0
	}
	return time.Duration(to - from)
}

func probeOnce(ctx context.Context, node Node, client *http.Client) probeOutcome {
	var outcome probeOutcome
	clock := stageClock{origin: time.Now()}
	trace := &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { clock.mark(&clock.dnsStart) },
		DNSDone:              func(httptrace.DNSDoneInfo) { clock.mark(&clock.dnsDone) },
		ConnectStart:         func(string, string) { clock.mark(&clock.connectStart) },
		ConnectDone:          func(string, string, error) { clock.mark(&clock.connectDone) },
		TLSHandshakeStart:    func() { clock.mark(&clock.tlsStart) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { clock.mark(&clock.tlsDone) },
		GotFirstResponseByte: func() { clock.mark(&clock.firstByte) },
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, node.BaseURL+"/health", nil)
	if err != nil {
		outcome.err = fmt.Errorf("build request: %w", err)
		return outcome
	}
	request.Header.Set("Cache-Control", "no-store")
	request.Header.Set("User-Agent", version.UserAgent())
	request = request.WithContext(httptrace.WithClientTrace(ctx, trace))

	response, err := client.Do(request)
	// Stage timings are populated on both outcomes: a failed TLS handshake or a
	// timeout must still be diagnosable.
	outcome.dns = between(&clock.dnsStart, &clock.dnsDone)
	outcome.tcp = between(&clock.connectStart, &clock.connectDone)
	outcome.tls = between(&clock.tlsStart, &clock.tlsDone)
	outcome.rtt = time.Duration(clock.firstByte.Load())
	if outcome.rtt <= 0 {
		outcome.rtt = time.Since(clock.origin)
	}
	if err != nil {
		outcome.err = err
		return outcome
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
	}()
	outcome.reachable = true
	outcome.statusCode = response.StatusCode
	if response.StatusCode != http.StatusOK {
		outcome.err = fmt.Errorf("unexpected health status: %s", response.Status)
		return outcome
	}
	var health struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&health); err != nil {
		outcome.err = fmt.Errorf("decode health response: %w", err)
		return outcome
	}
	outcome.serverVersion = health.Version
	if health.Status != "ok" {
		outcome.err = fmt.Errorf("health status %q", health.Status)
		return outcome
	}
	outcome.healthOK = true
	outcome.capabilities = fetchCapabilities(ctx, client, node)
	return outcome
}

// fetchCapabilities asks an optional endpoint. A 404/405 means the server is
// an older GoSpeed that does not implement negotiation; that is recorded but
// never treated as a failure.
func fetchCapabilities(ctx context.Context, client *http.Client, node Node) CapabilityInfo {
	info := CapabilityInfo{}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, node.BaseURL+"/capabilities", nil)
	if err != nil {
		info.Error = fmt.Sprintf("build request: %v", err)
		return info
	}
	request.Header.Set("Cache-Control", "no-store")
	request.Header.Set("User-Agent", version.UserAgent())
	response, err := client.Do(request)
	if err != nil {
		info.Error = fmt.Sprintf("request failed: %v", err)
		return info
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
	}()
	switch response.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		info.Unsupported = true
		info.Error = "server does not implement GET /capabilities"
		return info
	case http.StatusOK:
	default:
		info.Error = fmt.Sprintf("unexpected capabilities status: %s", response.Status)
		return info
	}
	var payload struct {
		ProtocolVersion int           `json:"protocol_version"`
		ServerVersion   string        `json:"server_version"`
		Capabilities    []string      `json:"capabilities"`
		Limits          *ServerLimits `json:"limits"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload); err != nil {
		info.Error = fmt.Sprintf("invalid capabilities response: %v", err)
		return info
	}
	info.Supported = true
	info.ProtocolVersion = payload.ProtocolVersion
	info.ServerVersion = payload.ServerVersion
	info.Capabilities = payload.Capabilities
	info.Limits = payload.Limits
	return info
}

// capabilitiesFailed reports whether the capability exchange found a real
// problem (invalid response, unexpected status, network error). A legacy server
// without the endpoint is not a failure.
func (p Probe) capabilitiesFailed() bool {
	if p.Capabilities.Supported || p.Capabilities.Unsupported {
		return false
	}
	return p.Capabilities.Error != ""
}

// pingSamples collects HTTP RTT samples for node selection.
func pingSamples(ctx context.Context, client *http.Client, node Node, count int, timeout time.Duration) ([]time.Duration, int) {
	samples := make([]time.Duration, 0, count)
	failures := 0
	for index := 0; index < count; index++ {
		sampleCtx, cancel := context.WithTimeout(ctx, timeout)
		sample, err := pingOnce(sampleCtx, client, node)
		cancel()
		if err != nil {
			failures++
			if ctx.Err() != nil {
				break
			}
			continue
		}
		samples = append(samples, sample)
	}
	return samples, failures
}

func pingOnce(ctx context.Context, client *http.Client, node Node) (time.Duration, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, node.BaseURL+"/ping", nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Cache-Control", "no-store")
	request.Header.Set("User-Agent", version.UserAgent())
	started := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
	}()
	rtt := time.Since(started)
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		return 0, fmt.Errorf("unexpected ping status: %s", response.Status)
	}
	return rtt, nil
}

func medianDuration(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}

func defaultProbeClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	// A dedicated transport keeps probe timings independent of connections
	// pooled by unrelated requests, and guarantees the first health request of
	// a probe actually dials (so TCP/TLS stages are measurable).
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   timeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
