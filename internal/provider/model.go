package provider

// MeasurementStatus is the terminal state of one measurement. A budget
// exceeded, cancelled or timed out measurement is never completed.
type MeasurementStatus string

const (
	StatusCompleted      MeasurementStatus = "completed"
	StatusFailed         MeasurementStatus = "failed"
	StatusCancelled      MeasurementStatus = "cancelled"
	StatusTimeout        MeasurementStatus = "timeout"
	StatusBudgetExceeded MeasurementStatus = "budget_exceeded"
	StatusConsentNeeded  MeasurementStatus = "consent_required"
)

// Completeness describes how much of the requested evidence exists. It is
// orthogonal to MeasurementStatus: a cancelled run has incomplete evidence
// even though the cancellation itself worked as intended.
type Completeness string

const (
	// Complete means every requested direction finished with server
	// confirmation.
	Complete Completeness = "complete"
	// Partial means some directions finished (download ok, upload failed).
	Partial Completeness = "partial"
	// Incomplete means the evidence is cut short by a budget stop, a timeout
	// or a cancellation.
	Incomplete Completeness = "incomplete"
)

// MetricType names the measured quantity of a latency metric. The type names
// the quantity only; the protocol and the derivation live in the Source
// field. Protocol names must never leak into the type (a TCP MinRTT stays a
// tcp_min_rtt no matter which protocol family produced it, and there is no
// "cloudflare_rtt" — Cloudflare's summary value is a summary_rtt with a
// named source).
type MetricType string

const (
	// MetricHTTPRTT is the time from sending an HTTP request to the first
	// response byte. It is not an ICMP ping and must never be shown as one.
	MetricHTTPRTT MetricType = "http_rtt"
	// MetricTCPMinRTT is the minimum RTT observed on a TCP connection (for
	// example the NDT7 tcp_info MinRTT).
	MetricTCPMinRTT MetricType = "tcp_min_rtt"
	// MetricSummaryRTT is a provider specific RTT summary (for example the
	// Cloudflare SDK's summary value). The Source field must state exactly
	// how it was derived.
	MetricSummaryRTT MetricType = "summary_rtt"
	// MetricICMPPing is a real ICMP echo round trip. Reserved for a future
	// provider that really speaks ICMP.
	MetricICMPPing MetricType = "icmp_ping"
)

// JitterType names the measured quantity of a jitter metric.
type JitterType string

const (
	// JitterConsecutiveDiff is the mean absolute difference of consecutive
	// latency samples (the GoSpeed HTTP jitter).
	JitterConsecutiveDiff JitterType = "consecutive_diff"
	// JitterRTP is the RTP style jitter of real packet level measurements
	// (RFC 3550). Reserved for a future provider that really speaks ICMP or
	// another packet protocol.
	JitterRTP JitterType = "rtp_jitter"
)

// Rate is one speed value with the evidence of the window it was measured
// over. Mbps is always real megabits per second (Mbit/s); conversions from
// kbit/s, bit/s or byte based units happen in the provider mapping, never in
// the display layer.
type Rate struct {
	// Mbps is null when the rate was not measured (N/A). It is never zero
	// as a placeholder for "unknown".
	Mbps *float64 `json:"mbps"`
	// ElapsedMs is the actual measurement window in milliseconds.
	ElapsedMs int64 `json:"elapsedMs"`
	// TransferredBytes is the payload bytes the window counted.
	TransferredBytes int64  `json:"transferredBytes"`
	Window           string `json:"window"`
	// StopReason states why the transfer stopped. The values reuse the
	// engine's stop reasons (requested_bytes_reached, duration_elapsed,
	// server_eof) plus provider specific values prefixed with the protocol.
	StopReason string `json:"stopReason,omitempty"`
}

// LatencyMetric carries a latency value with its measurement method. ValueMs
// is null when the metric was not measured.
type LatencyMetric struct {
	ValueMs *float64   `json:"valueMs"`
	Type    MetricType `json:"type"`
	// Source names protocol and derivation, for example
	// "http request to first response byte" or "ndt7 tcp_info.MinRTT".
	Source string `json:"source"`
}

// JitterMetric carries a jitter value with its measurement method. It stays
// completely null (the whole pointer nil) when the provider cannot measure
// jitter: NDT7 must not fill it, and a TCP RTTVar must never be used as a
// substitute — an RTTVar observation may only be noted in
// MethodRecord.Warnings.
type JitterMetric struct {
	ValueMs *float64   `json:"valueMs"`
	Type    JitterType `json:"type"`
	Source  string     `json:"source"`
}

// Quality is the three part quality statement prototyped in the M-Lab
// experiment.
type Quality struct {
	Completeness Completeness `json:"completeness"`
	// Stability is normal, degraded or unknown.
	Stability string `json:"stability"`
	// Trust is normal, verified or unknown.
	Trust string   `json:"trust"`
	Notes []string `json:"notes,omitempty"`
}

// MethodRecord states how the numbers were measured. Any aggregation across
// providers must group by Protocol: samples with different protocols never
// enter one statistical set.
type MethodRecord struct {
	// Protocol identifies the measurement method: gospeed-http, ndt7,
	// cloudflare-sdk or a future value.
	Protocol string `json:"protocol"`
	// NegotiatedCaps is the capability set that was actually confirmed for
	// this run. Exported so it serializes with the result.
	NegotiatedCaps CapabilitySet `json:"negotiatedCaps"`
	// Budget records the traffic budget that was in effect and, through the
	// provider's own documentation, on which layer it was counted.
	Budget BudgetLimits `json:"budget"`
	// ConsentVersion records the privacy policy version in effect for
	// providers that require consent.
	ConsentVersion string `json:"consentVersion,omitempty"`
	// Warnings records non fatal observations. This is the only place where
	// an NDT7 TCP RTTVar may appear (as an explicit "exists but is not
	// jitter" note).
	Warnings []string `json:"warnings,omitempty"`
}

// FinalResult is the unified terminal result every provider outputs. Pointer
// fields are null when a metric was not measured; they are never replaced by
// zero values or by a metric of a different kind.
type FinalResult struct {
	ProviderID ID                `json:"providerId"`
	ServerID   string            `json:"serverId"`
	Status     MeasurementStatus `json:"status"`
	// Download, Upload, Latency and Jitter are null when not measured.
	Download *Rate          `json:"download"`
	Upload   *Rate          `json:"upload"`
	Latency  *LatencyMetric `json:"latency"`
	Jitter   *JitterMetric  `json:"jitter"`
	// ElapsedMs is the wall time of the whole measurement, not a transfer
	// window (that lives in Rate.ElapsedMs).
	ElapsedMs        int64        `json:"elapsedMs"`
	TransferredBytes int64        `json:"transferredBytes"`
	ErrorCode        string       `json:"errorCode,omitempty"`
	ErrorMessage     string       `json:"errorMessage,omitempty"`
	Quality          Quality      `json:"quality"`
	Method           MethodRecord `json:"method"`
}
