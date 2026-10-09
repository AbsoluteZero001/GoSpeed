package speedtest

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
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

// Measurement window definitions. They are part of the result because a rate
// is only reproducible when the window it was measured over is known.
const (
	WindowDownload = "first_response_byte_to_last_body_byte"
	WindowUpload   = "request_body_write_start_to_server_confirmation"
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
	return t, nil
}

// TargetInfo is the target as recorded in a result.
type TargetInfo struct {
	ServerID      string   `json:"server_id"`
	ServerName    string   `json:"server_name"`
	ServerAddress string   `json:"server_address"`
	Protocol      Protocol `json:"protocol"`
	Local         bool     `json:"local"`
}

// SettingsSnapshot records the configuration a result was produced with so the
// measurement can be reproduced and audited later.
type SettingsSnapshot struct {
	Phases          []Phase       `json:"phases"`
	DurationNs      time.Duration `json:"duration_ns"`
	TimeoutNs       time.Duration `json:"timeout_ns"`
	MaxBytes        int64         `json:"max_bytes"`
	Connections     int           `json:"connections"`
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
	Bytes             int64         `json:"bytes"`
	DurationNs        time.Duration `json:"duration_ns"`
	Mbps              float64       `json:"mbps"`
	MBPerSecond       float64       `json:"mb_per_second"`
	Connections       int           `json:"connections"`
	MeasurementWindow string        `json:"measurement_window"`
	StopReason        StopReason    `json:"stop_reason"`
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
	TestID       string           `json:"test_id"`
	Timestamp    time.Time        `json:"timestamp"`
	Status       Status           `json:"status"`
	ErrorMessage string           `json:"error_message,omitempty"`
	Target       TargetInfo       `json:"target"`
	Settings     SettingsSnapshot `json:"settings"`
	Latency      *LatencyResult   `json:"latency,omitempty"`
	Download     *TransferResult  `json:"download,omitempty"`
	Upload       *UploadResult    `json:"upload,omitempty"`
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
