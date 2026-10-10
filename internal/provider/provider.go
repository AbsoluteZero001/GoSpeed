// Package provider defines the unified speed test provider interface and the
// shared measurement result model for GoSpeed.
//
// The package contains types only: no provider implementation, no network
// behaviour and no lifecycle state of its own. Implementations live in their
// own packages and adapt one measurement source (the GoSpeed HTTP engine,
// M-Lab NDT7, ...) to the Runner interface. Consumers negotiate capabilities
// up front and read results through FinalResult, so a provider that cannot
// measure a metric reports it as null instead of faking a value.
//
// Contracts every implementation must honour:
//   - Cancellation goes through context.Context only. A provider must unblock
//     its network I/O as soon as the run context is cancelled and must not
//     wait for protocol timeouts.
//   - Missing metrics are null (nil pointers / nil pointers in structs), never
//     zero values and never values from a different metric kind.
//   - Every metric keeps its measurement method: the metric type names the
//     quantity, the Source field names protocol and derivation. An NDT7 TCP
//     MinRTT must never be presented as an ICMP ping, and a TCP RTTVar must
//     never fill the jitter field.
package provider

import (
	"context"
	"errors"
	"time"
)

// ID uniquely identifies one provider implementation.
type ID string

const (
	// IDGoSpeedHTTP is the provider for self-hosted or user configured
	// GoSpeed HTTP nodes.
	IDGoSpeedHTTP ID = "gospeed-http"
	// IDMLabNDT7 is the provider for M-Lab NDT7 measurements. It stays an
	// opt-in experiment: every measurement needs an explicit per-run consent.
	IDMLabNDT7 ID = "mlab-ndt7"
	// IDCloudflare is reserved so the capability matrix stays expressible.
	// Cloudflare is a documented No-Go for productization.
	IDCloudflare ID = "cloudflare"
)

// Ownership states who operates a node. It drives the trust level and the
// wording a GUI may use ("local throughput" only for self hosted nodes).
type Ownership string

const (
	// OwnershipSelfHosted means the user runs or explicitly configured the
	// node itself.
	OwnershipSelfHosted Ownership = "self_hosted"
	// OwnershipThirdParty means a third party operates the node.
	OwnershipThirdParty Ownership = "third_party"
)

// DiscoveryOrigin states how a node became known to the client. It is kept
// separate from Ownership on purpose: who operates a node and how it was
// found are independent facts, and mixing them (a single "kind") makes the
// trust decision impossible to express.
type DiscoveryOrigin string

const (
	// DiscoveryConfigured means the node came from the user's configuration
	// or node file.
	DiscoveryConfigured DiscoveryOrigin = "configured"
	// DiscoveryService means a discovery service returned the node (for
	// example M-Lab Locate, after its own gate checks).
	DiscoveryService DiscoveryOrigin = "service"
)

// Capability is one thing a provider or node may be able to measure. A
// missing capability is not implemented, not faked, and the matching metric
// stays null in results.
type Capability string

const (
	CapDownload      Capability = "download"
	CapUpload        Capability = "upload"
	CapLatency       Capability = "latency"        // a latency of some stated kind, see MetricType
	CapJitter        Capability = "jitter"         // a jitter of some stated kind, see JitterType
	CapPacketLoss    Capability = "packet_loss"    // only real packet level measurements may claim this
	CapLoadedLatency Capability = "loaded_latency" // latency under load, reserved
)

// CapabilitySet is the set of capabilities confirmed for one measurement. The
// Notes map records the measurement method per capability (for example that
// latency is http_rtt or tcp_min_rtt); consumers must show the note together
// with the result.
type CapabilitySet struct {
	Supported map[Capability]bool   `json:"supported"`
	Notes     map[Capability]string `json:"notes,omitempty"`
}

// Supports reports whether a capability is part of the confirmed set. An
// absent entry counts as unsupported: negotiation is opt-in per capability.
func (c CapabilitySet) Supports(capability Capability) bool {
	return c.Supported[capability]
}

// Note returns the recorded measurement method for a capability, or "".
func (c CapabilitySet) Note(capability Capability) string {
	return c.Notes[capability]
}

// Scope classifies where a node is reachable. The values mirror the engine's
// NetworkScope; a string type keeps this package independent of the engine.
type Scope string

const (
	ScopeLocal   Scope = "local"
	ScopeLAN     Scope = "lan"
	ScopeRemote  Scope = "remote"
	ScopeUnknown Scope = "unknown"
)

// Direction selects a transfer direction of a measurement.
type Direction string

const (
	DirectionDownload Direction = "download"
	DirectionUpload   Direction = "upload"
)

// BudgetLimits is a connection layer traffic budget in bytes. Zero means the
// direction is unlimited. Which layer counted the bytes (socket or
// application) must be recorded with the result; a budget is never presented
// as a carrier billing limit.
type BudgetLimits struct {
	DownloadBytes int64 `json:"downloadBytes"`
	UploadBytes   int64 `json:"uploadBytes"`
	TotalBytes    int64 `json:"totalBytes"`
}

// Node is a test node as seen by a provider.
type Node struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Endpoint  string          `json:"endpoint"`
	Scope     Scope           `json:"scope"`
	Provider  ID              `json:"provider"`
	Ownership Ownership       `json:"ownership"`
	Discovery DiscoveryOrigin `json:"discovery"`
}

// ErrConsentRequired is the error a provider must return when a consent
// bound action (node discovery, negotiation, measurement) is attempted
// before the user agreed. Callers match it with errors.Is.
var ErrConsentRequired = errors.New("provider: user consent has not been granted")

// Discoverer is implemented by providers that can discover nodes (M-Lab
// Locate, a future authorized node directory). GoSpeed HTTP nodes come from
// the user's configuration and do not implement it.
//
// Privacy red line: a discovery request itself can leak the client's public
// IP to a third party. If the provider implements ConsentHandler, the caller
// must obtain and record the user's consent before calling Discover, and the
// implementation must refuse with ErrConsentRequired when consent was not
// recorded — it may not rely on the caller checking.
type Discoverer interface {
	Discover(ctx context.Context) ([]Node, error)
}

// ConsentHandler is implemented by providers whose measurements publish data
// on behalf of the user. While ConsentRequired returns true and no consent
// was recorded, a runner must not call Discover, Negotiate or Run for that
// provider: zero network connections before consent.
type ConsentHandler interface {
	ConsentRequired() bool
	// ConsentNotice is the full disclosure text (data publication, retention,
	// traffic budget, timeout) shown before the user decides.
	ConsentNotice() string
	// RecordConsent persists the user's decision bound to a policy version.
	RecordConsent(agreed bool, at time.Time, policyVersion string) error
}

// BudgetReporter is implemented by providers that enforce a traffic budget.
// Plan returns the budget the provider would apply; a zero field means that
// direction is unlimited by default.
type BudgetReporter interface {
	Plan() BudgetLimits
}

// ProgressEvent is one progress report. Phase and State reuse the engine's
// string semantics (latency/download/upload, start/progress/done). All
// numeric fields are observed values: a provider must not invent missing
// values, and zero is only filled when the observed value really is zero.
type ProgressEvent struct {
	Phase   string  `json:"phase"`
	State   string  `json:"state"`
	Bytes   int64   `json:"bytes"`
	Mbps    float64 `json:"mbps"`
	Message string  `json:"message,omitempty"`
}

// EventSink receives progress events. Implementations must return quickly
// (the GUI thread convention) and must not block the measurement.
type EventSink interface {
	OnProgress(ProgressEvent)
}

// RunRequest is one measurement request. A provider must fail an unsatisfiable
// request immediately instead of silently trimming it.
type RunRequest struct {
	// Node is the node to measure, already resolved by the caller.
	Node Node
	// Capabilities is the negotiated subset to measure in this run.
	Capabilities CapabilitySet
	// Directions selects the transfer directions to run.
	Directions []Direction
	// Duration is the intended transfer window. Zero means the provider's
	// own default window.
	Duration time.Duration
	// Budget limits the traffic of this run. Zero fields mean unlimited, but
	// a provider's own safety limits still apply.
	Budget BudgetLimits
	// Events receives progress events and may be nil. A nil sink must never
	// fail the run.
	Events EventSink
}

// Runner is the minimal execution interface every provider implements.
//
// Lifecycle and concurrency contract:
//   - Cancellation goes through ctx only: an implementation must make blocked
//     network I/O return as soon as ctx is cancelled (connection tracking and
//     forced closes, as prototyped in the M-Lab experiment) and must not wait
//     for protocol timeouts. There is deliberately no separate Cancel method —
//     an instance level cancel would blur whether it targets one run or the
//     whole provider instance, and the caller already owns the run scope.
//   - Run executes synchronously to a terminal state and returns the
//     FinalResult. There is no done channel; RunRequest.Events is the only
//     streaming channel and the return value is the only completion signal.
//   - One Runner instance runs at most one Run at a time; scheduling runs is
//     the caller's job.
type Runner interface {
	ID() ID
	// Negotiate returns the capabilities actually usable for this node. The
	// implementation must really probe (HTTP /capabilities, Locate metadata,
	// ...) instead of returning hardcoded optimistic values, and must not
	// send any probe before a required user consent was recorded.
	Negotiate(ctx context.Context, node Node) (CapabilitySet, error)
	// Run performs one measurement. The returned FinalResult is meaningful
	// together with the error: callers should persist it as evidence even
	// when err is non-nil, because the status field distinguishes failed,
	// cancelled, timeout and budget_exceeded outcomes.
	Run(ctx context.Context, req RunRequest) (FinalResult, error)
}
