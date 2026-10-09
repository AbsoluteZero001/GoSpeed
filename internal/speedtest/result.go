package speedtest

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// Status describes how far a test got. Failed and cancelled tests are never
// reported as completed, and a failure is never reported as 0 Mbps.
type Status string

const (
	StatusNotStarted Status = "not_started"
	StatusRunning    Status = "running"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
)

// RunState is the unified state machine the engine reports through Progress
// events. It separates the running phase (latency_testing, download_testing,
// upload_testing) from the terminal states so a GUI or CLI can render progress
// without reinterpreting phase names.
type RunState string

const (
	StateIdle            RunState = "idle"
	StatePreparing       RunState = "preparing"
	StateLatencyTesting  RunState = "latency_testing"
	StateDownloadTesting RunState = "download_testing"
	StateUploadTesting   RunState = "upload_testing"
	StateCompleted       RunState = "completed"
	StateFailed          RunState = "failed"
	StateCancelled       RunState = "cancelled"
)

// StateForPhase maps a measurement phase onto its running state.
func StateForPhase(phase Phase) RunState {
	switch phase {
	case PhaseLatency:
		return StateLatencyTesting
	case PhaseDownload:
		return StateDownloadTesting
	case PhaseUpload:
		return StateUploadTesting
	default:
		return StateIdle
	}
}

// Phase names one measurement stage of a test run.
type Phase string

const (
	PhaseLatency  Phase = "latency"
	PhaseDownload Phase = "download"
	PhaseUpload   Phase = "upload"
)

// LatencyType labels which kind of latency a result contains. HTTP RTT is not
// ICMP ping and must never be presented as one.
type LatencyType string

// LatencyHTTPRTT is the time from sending an HTTP request to receiving the
// first response byte.
const LatencyHTTPRTT LatencyType = "http_rtt"

// Protocol is the transport protocol a target speaks. Both values are HTTP in
// the sense of net/http, one of them wrapped in TLS.
type Protocol string

const (
	ProtocolHTTP  Protocol = "http"
	ProtocolHTTPS Protocol = "https"
)

// NetworkScope classifies where a target is reachable. It never guesses:
// literal addresses are classified by their IP range, and host names stay
// unknown because DNS could resolve anywhere.
type NetworkScope string

const (
	ScopeLocal   NetworkScope = "local"
	ScopeLAN     NetworkScope = "lan"
	ScopeRemote  NetworkScope = "remote"
	ScopeUnknown NetworkScope = "unknown"
)

// Measurement window definitions. They are part of the result because a rate
// is only reproducible when the window it was measured over is known.
const (
	// WindowDownload is the single connection download window: from the first
	// response byte until the transfer stops.
	WindowDownload = "first_response_byte_to_last_body_byte"
	// WindowUpload is the single connection upload window: from the first body
	// write until the server confirmation has been read. It deliberately
	// includes the confirmation round trip.
	WindowUpload = "request_body_write_start_to_server_confirmation"
	// WindowDownloadMulti is the shared download window of a multi connection
	// run: from the first response byte of the first connection until the
	// aggregate transfer stops.
	WindowDownloadMulti = "shared_window_first_response_byte_to_last_body_byte"
	// WindowUploadMulti is the shared upload window of a multi connection run:
	// from the first body write of the first connection until the last server
	// confirmation has been read.
	WindowUploadMulti = "shared_window_first_body_write_to_last_server_confirmation"
)

// StopReason records why a transfer stopped.
type StopReason string

const (
	StopReasonRequestedBytes StopReason = "requested_bytes_reached"
	StopReasonDuration       StopReason = "duration_elapsed"
	StopReasonServerEOF      StopReason = "server_eof"
)

// Target is the minimal description of a server the engine can test. Node
// management, availability checks and discovery live in internal/nodes; the
// engine only needs an address it is allowed to contact.
type Target struct {
	ID       string
	Name     string
	BaseURL  string
	Protocol Protocol
	// Local marks loopback targets. Their results must be labelled as local
	// loopback measurements and never be presented as internet bandwidth.
	Local bool
	// SelectionMethod and HealthStatus are recorded by the caller (for example
	// the CLI) after node selection. The engine does not probe nodes itself.
	SelectionMethod string
	SelectionReason string
	HealthStatus    string
	HealthLatency   time.Duration
	// scope is derived from base_url during normalization.
	scope NetworkScope
}

// normalized validates the target and returns a canonical copy: the base URL
// has no trailing slash and the protocol always matches the URL scheme.
func (t Target) normalized() (Target, error) {
	t.ID = strings.TrimSpace(t.ID)
	t.Name = strings.TrimSpace(t.Name)
	t.BaseURL = strings.TrimRight(strings.TrimSpace(t.BaseURL), "/")
	if t.ID == "" {
		return t, fmt.Errorf("%w: missing id", ErrInvalidTarget)
	}
	if t.Name == "" {
		t.Name = t.ID
	}
	if t.BaseURL == "" {
		return t, fmt.Errorf("%w: missing base url", ErrInvalidTarget)
	}
	parsed, err := url.Parse(t.BaseURL)
	if err != nil {
		return t, fmt.Errorf("%w: parse base url %q: %v", ErrInvalidTarget, t.BaseURL, err)
	}
	if parsed.Host == "" || !parsed.IsAbs() {
		return t, fmt.Errorf("%w: base url %q must be absolute", ErrInvalidTarget, t.BaseURL)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return t, fmt.Errorf("%w: base url %q must not contain a query or fragment", ErrInvalidTarget, t.BaseURL)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != string(ProtocolHTTP) && scheme != string(ProtocolHTTPS) {
		return t, fmt.Errorf("%w: %q", ErrUnsupportedProtocol, scheme)
	}
	if t.Protocol == "" {
		t.Protocol = Protocol(scheme)
	}
	if string(t.Protocol) != scheme {
		return t, fmt.Errorf("%w: protocol %q does not match base url scheme %q", ErrInvalidTarget, t.Protocol, scheme)
	}
	t.scope = scopeForHost(parsed.Hostname())
	if t.scope == ScopeLocal {
		t.Local = true
	}
	return t, nil
}

// NetworkScope returns the derived scope of the target address. It is local,
// lan, remote or unknown (host names are unknown because DNS could resolve
// anywhere).
func (t Target) NetworkScope() NetworkScope {
	if t.scope == "" {
		return ScopeUnknown
	}
	return t.scope
}

// scopeForHost classifies a host without contacting DNS.
func scopeForHost(host string) NetworkScope {
	trimmed := strings.ToLower(strings.TrimSpace(host))
	if trimmed == "" {
		return ScopeUnknown
	}
	if trimmed == "localhost" {
		return ScopeLocal
	}
	address := net.ParseIP(trimmed)
	if address == nil {
		return ScopeUnknown
	}
	switch {
	case address.IsLoopback():
		return ScopeLocal
	case address.IsPrivate(), address.IsLinkLocalUnicast(), address.IsLinkLocalMulticast():
		return ScopeLAN
	default:
		return ScopeRemote
	}
}

// TargetInfo is the target as recorded in a result.
type TargetInfo struct {
	ServerID      string   `json:"server_id"`
	ServerName    string   `json:"server_name"`
	ServerAddress string   `json:"server_address"`
	Protocol      Protocol `json:"protocol"`
	Local         bool     `json:"local"`
	// NetworkScope is local, lan, remote or unknown. It is derived from the
	// configured address, not from a guess about the internet path.
	NetworkScope NetworkScope `json:"network_scope"`
	// SelectionMethod records how the node was chosen (auto/manual/default).
	SelectionMethod string `json:"selection_method,omitempty"`
	// SelectionReason explains the choice in plain language; it is only set
	// for automatic selection.
	SelectionReason string `json:"selection_reason,omitempty"`
	// HealthStatus and HealthLatencyNs carry the last probe result when the
	// caller ran one before the test.
	HealthStatus    string        `json:"health_status,omitempty"`
	HealthLatencyNs time.Duration `json:"health_latency_ns,omitempty"`
	// Capabilities is filled by capability negotiation when the server
	// implements GET /capabilities.
	Capabilities *CapabilityInfo `json:"capabilities,omitempty"`
}

// SettingsSnapshot records the configuration a result was produced with so the
// measurement can be reproduced and audited later.
type SettingsSnapshot struct {
	Phases          []Phase       `json:"phases"`
	DurationNs      time.Duration `json:"duration_ns"`
	TimeoutNs       time.Duration `json:"timeout_ns"`
	MaxBytes        int64         `json:"max_bytes"`
	Connections     int           `json:"connections"`
	SampleInterval  time.Duration `json:"sample_interval_ns"`
	LatencySamples  int           `json:"latency_samples"`
	LatencyInterval time.Duration `json:"latency_interval_ns"`
	Warmup          bool          `json:"warmup"`
}

// LatencyResult holds HTTP RTT statistics. All durations are serialized as
// integer nanoseconds so no precision is lost.
type LatencyResult struct {
	// Type names the measured quantity; in v0.1.0 this is always http_rtt.
	Type LatencyType `json:"type"`
	// Attempts is the number of requests that were started.
	Attempts int `json:"attempts"`
	// SuccessfulSamples is len(SamplesNs).
	SuccessfulSamples int `json:"successful_samples"`
	// FailedSamples counts requests that did not produce a successful sample.
	FailedSamples int           `json:"failed_samples"`
	MinNs         time.Duration `json:"min_ns"`
	AverageNs     time.Duration `json:"average_ns"`
	MaxNs         time.Duration `json:"max_ns"`
	// JitterNs is the mean absolute difference between consecutive samples and
	// is null when fewer than two samples were collected.
	JitterNs *time.Duration `json:"jitter_ns"`
	// SamplesNs keeps the raw samples for later re-analysis.
	SamplesNs []time.Duration `json:"samples_ns"`
	// PacketLossPercent is null in v0.1.0 because the engine does not run a
	// packet level loss test. It must not be filled with the HTTP failure rate.
	PacketLossPercent *float64 `json:"packet_loss_percent"`
	// Errors keeps a bounded number of sample failures for diagnostics.
	Errors []string `json:"errors,omitempty"`
}

// TransferResult describes one measured transfer.
type TransferResult struct {
	Bytes int64 `json:"bytes"`
	// DurationNs is the shared measurement window, never the sum of the
	// per connection windows.
	DurationNs  time.Duration `json:"duration_ns"`
	Mbps        float64       `json:"mbps"`
	MBPerSecond float64       `json:"mb_per_second"`
	// Connections is the requested connection count and is kept for v0.1.0
	// compatibility. ActiveConnections and FailedConnections describe the
	// connections that actually transferred data.
	Connections       int                `json:"connections"`
	ActiveConnections int                `json:"active_connections"`
	FailedConnections int                `json:"failed_connections"`
	MeasurementWindow string             `json:"measurement_window"`
	StopReason        StopReason         `json:"stop_reason"`
	Samples           []Sample           `json:"samples,omitempty"`
	Statistics        Statistics         `json:"statistics"`
	ConnectionReports []ConnectionReport `json:"connection_reports,omitempty"`
}

// ConnectionState describes how one connection of a transfer phase ended.
type ConnectionState string

const (
	// ConnectionCompleted means the connection transferred its data and, for
	// uploads, was confirmed by the server.
	ConnectionCompleted ConnectionState = "completed"
	// ConnectionFailed means the connection produced an error. Its bytes, if
	// any, are still reported so nothing is silently dropped.
	ConnectionFailed ConnectionState = "failed"
)

// ConnectionReport is the per connection evidence of a transfer phase. Every
// connection that was started is reported, successful or not, because the
// aggregate rate is only trustworthy when the connections behind it are
// visible.
type ConnectionReport struct {
	Index int             `json:"index"`
	State ConnectionState `json:"state"`
	Bytes int64           `json:"bytes"`
	// ServerConfirmedBytes is only set for uploads.
	ServerConfirmedBytes *int64        `json:"server_confirmed_bytes,omitempty"`
	DurationNs           time.Duration `json:"duration_ns"`
	ServerDurationNs     time.Duration `json:"server_duration_ns,omitempty"`
	Error                string        `json:"error,omitempty"`
}

// Sample is one point of the real time rate series of a transfer phase. Every
// value comes from atomic byte counters and monotonic clock reads; nothing is
// interpolated or simulated.
type Sample struct {
	Phase             Phase         `json:"phase"`
	Timestamp         time.Time     `json:"timestamp"`
	ElapsedNs         time.Duration `json:"elapsed_ns"`
	BytesTransferred  int64         `json:"bytes_transferred"`
	CurrentMbps       float64       `json:"current_mbps"`
	AverageMbps       float64       `json:"average_mbps"`
	ActiveConnections int           `json:"active_connections"`
}

// Statistics summarizes the instantaneous samples of one transfer phase.
// Optional values are null when the samples cannot support them, which is the
// JSON representation of N/A. The standard deviation is the sample standard
// deviation (n-1); the coefficient of variation is stddev/mean*100.
type Statistics struct {
	Samples                       int      `json:"samples"`
	MeanMbps                      *float64 `json:"mean_mbps"`
	MedianMbps                    *float64 `json:"median_mbps"`
	MinMbps                       *float64 `json:"min_mbps"`
	MaxMbps                       *float64 `json:"max_mbps"`
	StdDevMbps                    *float64 `json:"stddev_mbps"`
	CoefficientOfVariationPercent *float64 `json:"cv_percent"`
	StdDevKind                    string   `json:"stddev_kind,omitempty"`
}

// UploadResult adds the server side confirmation to a transfer result. The
// engine refuses to publish a rate when the two byte counts disagree.
type UploadResult struct {
	TransferResult
	ServerConfirmedBytes int64         `json:"server_confirmed_bytes"`
	ServerDurationNs     time.Duration `json:"server_duration_ns"`
}

// Result is the stable, JSON serializable outcome of one test run.
type Result struct {
	TestID       string    `json:"test_id"`
	Timestamp    time.Time `json:"timestamp"`
	Status       Status    `json:"status"`
	ErrorMessage string    `json:"error_message,omitempty"`
	// Warnings records non fatal problems such as a subset of the requested
	// connections failing. A completed test with warnings is not the same as a
	// clean test, and callers must not hide them.
	Warnings []string         `json:"warnings,omitempty"`
	Target   TargetInfo       `json:"target"`
	Settings SettingsSnapshot `json:"settings"`
	Latency  *LatencyResult   `json:"latency,omitempty"`
	Download *TransferResult  `json:"download,omitempty"`
	Upload   *UploadResult    `json:"upload,omitempty"`
}

// NewTestID returns a random identifier that makes results traceable without
// relying on wall-clock uniqueness.
func NewTestID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("speedtest: generate test id: %w", err)
	}
	return "gs-" + hex.EncodeToString(raw[:]), nil
}
