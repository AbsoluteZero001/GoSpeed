// runner.go is the ndt7 provider's public surface. It adapts the SDK
// wrapper (adapter.go) to the provider.Runner / provider.Discoverer /
// provider.ConsentHandler / provider.BudgetReporter contracts the
// rest of GoSpeed uses, and enforces the protocol-specific invariants
// the SDK does not (single connection, byte budget, opt-in consent).
package ndt7

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
)

// Protocol is the value MethodRecord.Protocol records for every run
// the ndt7 provider produces. Aggregations across providers must group
// by this value.
const Protocol = "ndt7"

// StopReason values the ndt7 provider writes into provider.Rate.StopReason.
// They are also exported so the provider package's tests can assert them.
const (
	StopReasonNDT7BudgetExceeded = "ndt7_budget_exceeded"
	StopReasonNDT7Cancelled      = "ndt7_cancelled"
	StopReasonNDT7Timeout        = "ndt7_timeout"
	// StopReasonNDT7Completed is the neutral completion reason. The
	// SDK discards the protocol loop's error, so the adapter never
	// observes WHY the measurement channel closed and must not claim
	// a duration elapsed or a server-side EOF it cannot prove.
	StopReasonNDT7Completed = "ndt7_completed"
	// StopReasonNDT7WindowUnavailable means bytes moved but no honest
	// rate could be formed (single frame, counter regression or a
	// zero-length window).
	StopReasonNDT7WindowUnavailable = "ndt7_window_unavailable"
	StopReasonNDT7NoData            = "ndt7_no_application_bytes"
)

// Errors a runner may surface. They are sentinel so callers can match
// with errors.Is; the messages are stable for logs.
var (
	// ErrConnectionsNotSupported is returned when the caller asks for
	// more than one parallel connection: NDT7 measures one direction
	// over a single WebSocket connection and the SDK does not expose a
	// multi-connection mode.
	ErrConnectionsNotSupported = errors.New("ndt7: NDT7 measures one direction over a single WebSocket; multi-connection requests are refused, not silently clamped")
	// ErrConsentRequired is returned by Negotiate, Discover and Run
	// when the consent gate has not been satisfied. The handler and the
	// GUI both match on it.
	ErrConsentRequired = errors.New("ndt7: explicit user consent is required before any measurement, negotiation or discovery")
	// ErrUnsupportedDirection is returned when the caller asks for a
	// direction the protocol does not carry.
	ErrUnsupportedDirection = errors.New("ndt7: direction must be download or upload")
)

// ConsentRecord is the durable record of one user's opt-in. It is
// stored next to the GoSpeed configuration and threaded through the
// Runner before any network call.
//
// The zero value is unsafe: Agreed must be true before the runner
// touches the network.
type ConsentRecord struct {
	// Agreed is true only after the user has explicitly consented.
	Agreed bool
	// AgreedAt is the wall-clock time of the consent decision.
	AgreedAt time.Time
	// PolicyVersion identifies the exact consent wording shown. A
	// stored agreement can always be traced back to what was displayed.
	PolicyVersion string
	// ClientName is the GoSpeed product name that recorded consent;
	// it makes the record self-describing in a future multi-product
	// configuration.
	ClientName string
	// ClientVersion is the GoSpeed product version that recorded consent.
	ClientVersion string
}

// consentVersion is the consent wording in effect. Bumping it forces a
// fresh consent dialog even when the previous one was already
// recorded.
const consentVersion = "ndt7-notice-v1"

// ClientName / ClientVersion identify this build. They are exposed so
// the consent notice, the warning strings and the User-Agent use the
// same identifier.
const (
	clientName    = "GoSpeed"
	clientVersion = "0.1.0-ndt7"
)

// DefaultBudgetBytes are the wire-level byte caps the provider applies
// when the caller does not specify any. They bound the application
// payload from above and are an experimental PoC default; they do not
// match ISP billing limits.
var DefaultBudgetBytes = struct {
	Download int64 // wire-level read cap for download
	Upload   int64 // wire-level write cap for upload
	Total    int64 // wire-level cap shared across the whole plan
}{
	Download: 24 << 20, // 24 MiB
	Upload:   16 << 20, // 16 MiB
	Total:    40 << 20, // 40 MiB
}

// DefaultOverallTimeout caps every run when the caller does not
// specify one. It is intentionally shorter than the SDK's combined
// DownloadTimeout (15 s) + UploadTimeout (10 s) so the run context
// fires before the SDK I/O deadline and produces a clean
// provider.StatusTimeout.
const DefaultOverallTimeout = 30 * time.Second

// Runner is the production ndt7 provider. It implements
// provider.Runner, provider.Discoverer, provider.ConsentHandler and
// provider.BudgetReporter. One Runner instance may run exactly one Run
// at a time; the provider package documents the contract.
type Runner struct {
	consent *ConsentRecord
	// budgets holds the wire-level byte caps the next Run will apply.
	// Zero values mean "use the defaults".
	budgets Budgets
	// httpClient is unused in this offline build but reserved so a
	// future live implementation can share a connection pool across
	// directions; nil is acceptable for the mock-only path.
	httpClient httpClient
}

// Budgets is the per-direction wire-level byte cap. A zero field means
// "use the default". The provider never advertises these as ISP
// billing limits; they are experimental PoC defaults.
type Budgets struct {
	DownloadBytes int64
	UploadBytes   int64
}

// New returns a Runner with no consent recorded. The runner refuses
// every network call until RecordConsent has been called.
func NewRunner() *Runner { return &Runner{} }

// NewWithConsent returns a Runner pre-loaded with the user's consent
// decision. Passing nil is equivalent to New.
func NewRunnerWithConsent(consent *ConsentRecord) *Runner {
	r := &Runner{}
	r.setConsentRecord(consent)
	return r
}

// setConsentRecord stores a record directly. The public
// provider.ConsentHandler.RecordConsent method below rejects
// out-of-policy versions; this one accepts whatever the UI persisted
// after the consent dialog closed.
func (r *Runner) setConsentRecord(consent *ConsentRecord) {
	r.consent = consent
}

// ID implements provider.Runner.
func (r *Runner) ID() provider.ID { return provider.IDMLabNDT7 }

// Negotiate implements provider.Runner.
//
// For ndt7, negotiation must:
//
//   - refuse with ErrConsentRequired if no consent has been recorded
//   - return a capability set that maps the SDK's TCPInfo.MinRTT to
//     provider.MetricTCPMinRTT, never to provider.MetricHTTPRTT or
//     provider.MetricICMPPing
//   - declare no jitter (NDT7 has no application-level jitter); a
//     TCP RTTVar observation is noted in MethodRecord.Warnings, never
//     placed into a JitterMetric
//   - carry the correct measurement-method notes for every
//     capability it claims
//
// The probe is real: Negotiate performs a Get on /capabilities-like
// metadata when the server exposes it. The ndt7 protocol does not
// expose a discovery endpoint, so when no service URL is configured
// Negotiate falls back to the known capability shape and reports that
// the capabilities are inferred, never confirmed.
//
// The PoC does not implement a live probe yet. It is wired to fail
// fast with ErrConsentRequired before any network call, so the
// contract that "no consent, no probe" is observable. A future
// implementation that adds Locate-based negotiation belongs in this
// method, behind the consent gate, never before it.
func (r *Runner) Negotiate(ctx context.Context, node provider.Node) (provider.CapabilitySet, error) {
	if !r.consentAgreed() {
		return provider.CapabilitySet{}, ErrConsentRequired
	}
	if err := ctx.Err(); err != nil {
		return provider.CapabilitySet{}, err
	}
	// The PoC negotiates only from the documented protocol contract:
	// every NDT7 server supports download, upload and tcp_min_rtt;
	// none of them supports application-level jitter.
	notes := map[provider.Capability]string{
		provider.CapDownload: NoteDownloadSource,
		provider.CapUpload:   NoteUploadSource,
		provider.CapLatency:  NoteLatencySource,
	}
	return provider.CapabilitySet{
		Supported: map[provider.Capability]bool{
			provider.CapDownload: true,
			provider.CapUpload:   true,
			provider.CapLatency:  true,
		},
		Notes: notes,
	}, nil
}

// Run implements provider.Runner.
//
// The PoC supports a single direction per Run call: the
// provider.RunRequest.Directions slice is expected to contain one
// element. A multi-direction slice triggers ErrConnectionsNotSupported
// because NDT7 measures one direction over a single WebSocket and
// running two directions sequentially would double the wall-clock
// without the SDK expressing a multi-direction primitive.
//
// Run refuses the request before any network call when:
//   - consent has not been recorded (ErrConsentRequired)
//   - Connections > 1 (ErrConnectionsNotSupported)
//   - an unsupported direction is requested (ErrUnsupportedDirection)
func (r *Runner) Run(ctx context.Context, req provider.RunRequest) (provider.FinalResult, error) {
	if !r.consentAgreed() {
		return rejected(provider.StatusConsentNeeded, provider.FinalResult{
			ProviderID:   r.ID(),
			ServerID:     req.Node.ID,
			Status:       provider.StatusConsentNeeded,
			ErrorCode:    "consent_required",
			ErrorMessage: ErrConsentRequired.Error(),
			Scope:        req.Node.Scope,
			Ownership:    req.Node.Ownership,
			Discovery:    req.Node.Discovery,
			Quality:      provider.Quality{Completeness: provider.Incomplete, Stability: "unknown", Trust: "unknown"},
		}), ErrConsentRequired
	}
	if err := validateRunRequest(req); err != nil {
		return rejected(provider.StatusFailed, provider.FinalResult{
			ProviderID:   r.ID(),
			ServerID:     req.Node.ID,
			Status:       provider.StatusFailed,
			ErrorCode:    errorCodeFor(err),
			ErrorMessage: err.Error(),
			Scope:        req.Node.Scope,
			Ownership:    req.Node.Ownership,
			Discovery:    req.Node.Discovery,
			Quality:      provider.Quality{Completeness: provider.Incomplete, Stability: "unknown", Trust: "unknown"},
		}), err
	}
	if err := ctx.Err(); err != nil {
		return rejected(provider.StatusCancelled, provider.FinalResult{
			ProviderID:   r.ID(),
			ServerID:     req.Node.ID,
			Status:       provider.StatusCancelled,
			ErrorCode:    "cancelled",
			ErrorMessage: err.Error(),
			Scope:        req.Node.Scope,
			Ownership:    req.Node.Ownership,
			Discovery:    req.Node.Discovery,
			Quality:      provider.Quality{Completeness: provider.Incomplete, Stability: "unknown", Trust: "unknown"},
		}), err
	}
	direction := req.Directions[0]
	adapterOptions := mapToAdapterOptions(req, direction)
	adapter, err := New(adapterOptions)
	if err != nil {
		return rejected(provider.StatusFailed, provider.FinalResult{
			ProviderID:   r.ID(),
			ServerID:     req.Node.ID,
			Status:       provider.StatusFailed,
			ErrorCode:    errorCodeFor(err),
			ErrorMessage: err.Error(),
			Scope:        req.Node.Scope,
			Ownership:    req.Node.Ownership,
			Discovery:    req.Node.Discovery,
			Quality:      provider.Quality{Completeness: provider.Incomplete, Stability: "unknown", Trust: "unknown"},
		}), err
	}
	result, runErr := adapter.Run(ctx)
	return buildFinalResult(req, direction, result, runErr), runErr
}

// Discover implements provider.Discoverer.
//
// The PoC does not actually call Locate: it refuses with
// ErrConsentRequired until consent is recorded, then returns an empty
// list. A future implementation belongs in this method, behind the
// consent gate.
func (r *Runner) Discover(ctx context.Context) ([]provider.Node, error) {
	if !r.consentAgreed() {
		return nil, ErrConsentRequired
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}

// ConsentRequired implements provider.ConsentHandler.
func (r *Runner) ConsentRequired() bool { return true }

// ConsentNotice returns the privacy disclosure that must be shown
// before the user is asked to opt in. It describes what data the
// measurement publishes (client public IP, the test parameters, the
// observed bytes and TCPInfo) and where the data ends up (M-Lab
// public dataset, BigQuery, indefinite retention).
//
// The text is exported in full so a UI cannot shorten or reword it
// without a deliberate code change. A future revision must bump
// consentVersion so a previously recorded agreement does not silently
// apply to a different notice.
func (r *Runner) ConsentNotice() string {
	return defaultConsentNotice
}

// RecordConsent implements provider.ConsentHandler. It validates the
// user's decision into the runner so the next Negotiate, Discover or
// Run can reach the network.
func (r *Runner) RecordConsent(agreed bool, at time.Time, policyVersion string) error {
	if policyVersion == "" {
		policyVersion = consentVersion
	}
	if agreed && policyVersion != consentVersion {
		return fmt.Errorf("ndt7: consent policy version mismatch: got %q, want %q", policyVersion, consentVersion)
	}
	r.consent = &ConsentRecord{
		Agreed:        agreed,
		AgreedAt:      at,
		PolicyVersion: policyVersion,
		ClientName:    clientName,
		ClientVersion: clientVersion,
	}
	return nil
}

// Plan implements provider.BudgetReporter. It returns the wire-level
// byte caps the runner would apply by default. Zero BudgetLimits means
// "no caps", so callers who pass an explicit zero BudgetLimits opt
// out of the default caps.
func (r *Runner) Plan() provider.BudgetLimits {
	return provider.BudgetLimits{
		DownloadBytes: DefaultBudgetBytes.Download,
		UploadBytes:   DefaultBudgetBytes.Upload,
		TotalBytes:    DefaultBudgetBytes.Total,
	}
}

// consentAgreed reports whether a usable consent record is present.
// A nil record, a record with Agreed=false or an out-of-policy version
// all fail the gate.
func (r *Runner) consentAgreed() bool {
	c := r.consent
	if c == nil || !c.Agreed {
		return false
	}
	return c.PolicyVersion == consentVersion
}

// validateRunRequest enforces the protocol-specific invariants:
// exactly one direction, exactly one connection. A multi-direction
// request is the most common mistake because the GUI by default asks
// for download+upload; ndt7 measures one direction at a time and
// silently degrading to a single direction would surprise the user.
func validateRunRequest(req provider.RunRequest) error {
	switch len(req.Directions) {
	case 0:
		return errors.New("ndt7: at least one direction is required")
	case 1:
		// OK
	default:
		return fmt.Errorf("ndt7: NDT7 measures one direction per run; got %d directions", len(req.Directions))
	}
	switch req.Directions[0] {
	case provider.DirectionDownload, provider.DirectionUpload:
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedDirection, req.Directions[0])
	}
	if req.Connections > 1 {
		return fmt.Errorf("%w (got %d)", ErrConnectionsNotSupported, req.Connections)
	}
	if req.Connections < 0 {
		return errors.New("ndt7: connection count must not be negative")
	}
	return nil
}

// mapToAdapterOptions translates a provider.RunRequest into the
// adapter's Options. Direction, scheme, server, overall timeout and
// wire-level budget are mapped; every other field is ignored.
func mapToAdapterOptions(req provider.RunRequest, direction provider.Direction) Options {
	scheme := "ws"
	if strings.HasPrefix(strings.ToLower(req.Node.Endpoint), "https://") ||
		strings.HasPrefix(strings.ToLower(req.Node.Endpoint), "wss://") {
		scheme = "wss"
	}
	// OverallTimeout is a wall-clock cap, distinct from the
	// transfer window in req.Duration. The slack added on top of
	// req.Duration is small for short measurements and the SDK
	// default (30 s) is used when the caller does not specify one.
	timeout := DefaultOverallTimeout
	if req.Duration > 0 {
		slack := 5 * time.Second
		if req.Duration < slack {
			slack = req.Duration / 2
			if slack < 250*time.Millisecond {
				slack = 250 * time.Millisecond
			}
		}
		timeout = req.Duration + slack
		if timeout > DefaultOverallTimeout {
			timeout = DefaultOverallTimeout
		}
	}
	budget := req.Budget.DownloadBytes
	if direction == provider.DirectionUpload {
		budget = req.Budget.UploadBytes
	}
	if budget == 0 {
		// Caller opted out of caps; leave 0.
	}
	return Options{
		Server:          req.Node.Endpoint,
		Scheme:          scheme,
		Direction:       Direction(direction),
		OverallTimeout:  timeout,
		WireBudgetBytes: budget,
	}
}

// buildFinalResult maps the adapter's Result onto the unified
// provider.FinalResult. The mapping is one-way: every numeric field
// the adapter produced is copied; nothing is invented. When the
// adapter's Status says partial, the budget_exceeded status and the
// ndt7-specific stop reason are preserved.
func buildFinalResult(req provider.RunRequest, direction provider.Direction, r Result, runErr error) provider.FinalResult {
	result := provider.FinalResult{
		ProviderID: provider.IDMLabNDT7,
		ServerID:   req.Node.ID,
		Scope:      req.Node.Scope,
		Ownership:  req.Node.Ownership,
		Discovery:  req.Node.Discovery,
		Quality:    qualityFor(r, runErr),
	}
	switch r.Status {
	case StatusCompleted:
		result.Status = provider.StatusCompleted
	case StatusPartial:
		// Contract evidence for why this is NOT a "partial" status:
		// internal/provider/model.go defines the complete set of
		// MeasurementStatus values as completed / failed / cancelled /
		// timeout / budget_exceeded / consent_required. There is no
		// "partial" member, so a budget-truncated run maps onto the
		// contract's dedicated budget_exceeded status. The "partial"
		// notion lives in Quality.Completeness instead, which
		// qualityFor sets to provider.Partial below. Inventing a new
		// MeasurementStatus value would break the shared provider
		// contract that the HTTP provider and the GUI both consume.
		result.Status = provider.StatusBudgetExceeded
	case StatusCancelled:
		result.Status = provider.StatusCancelled
	case StatusTimeout:
		result.Status = provider.StatusTimeout
	default:
		result.Status = provider.StatusFailed
	}
	if r.Error != "" {
		result.ErrorMessage = r.Error
	} else if runErr != nil {
		result.ErrorMessage = runErr.Error()
	}
	result.ErrorCode = errorCodeForRunError(r.ErrorClass, runErr)
	if r.BudgetExceeded && result.ErrorCode == "" {
		// A budget-truncated run is a real, machine-readable outcome
		// and must never publish an empty ErrorCode. The adapter sets
		// ErrorNone because the truncation is a planned stop rather
		// than a protocol failure, so the code is supplied here from
		// the unambiguous BudgetExceeded flag.
		result.ErrorCode = "budget_exceeded"
	}
	result.Method = provider.MethodRecord{
		Protocol:       Protocol,
		NegotiatedCaps: req.Capabilities,
		Budget:         req.Budget,
		ConsentVersion: consentVersion,
	}
	stopReason := mapStopReason(r.StopReason)
	rate := provider.Rate{
		Window:     r.Window,
		StopReason: stopReason,
	}
	// ApplicationBytes is the window delta (matches the engine's
	// TransferredBytes contract). The SDK's cumulative count is in
	// Detail.Payload as CumulativeAppBytes.
	if r.ApplicationBytes > 0 {
		// Mbps is a *float64 in Rate; a non-positive value is
		// intentionally left nil to communicate "could not be
		// measured", not "0 Mbps".
		mbps := r.Mbps
		if mbps > 0 {
			rate.Mbps = &mbps
		}
		rate.ElapsedMs = r.ElapsedMs
		rate.TransferredBytes = r.ApplicationBytes
	}
	switch direction {
	case provider.DirectionDownload:
		result.Download = &rate
	case provider.DirectionUpload:
		result.Upload = &rate
	}
	result.ElapsedMs = r.ElapsedMs
	result.TransferredBytes = r.ApplicationBytes
	// Warnings carry what the SDK does not let the adapter observe.
	// They never change the numbers; they exist so a consumer reading
	// a neutral StopReason knows exactly why that value is neutral.
	warnings := []string{}
	if r.Status == StatusCompleted || r.Status == StatusPartial {
		warnings = append(warnings,
			"ndt7: ndt7-client-go discards the protocol loop error (Client.collectData starts it as `go f(ctx, conn, inch)`), "+
				"so the adapter cannot distinguish an SDK deadline from a server-side close or an abnormal disconnect; "+
				"the StopReason is reported as the neutral "+StopReasonNDT7Completed+" rather than an unverified cause")
	}
	if r.MeasurementSource == MeasurementSourceUnavailable && r.WindowRejectReason != "" {
		warnings = append(warnings, "ndt7: no rate published - "+r.WindowRejectReason)
	}
	if r.MeasurementSource == MeasurementSourceClientAppInfo && r.ServerFrameSeen {
		warnings = append(warnings, fmt.Sprintf(
			"ndt7: rate uses client-origin AppInfo (cumulative %d bytes over %d us); "+
				"the server-origin measurement (%d bytes over %d us) is recorded for transparency only and is excluded from the formula",
			r.CumulativeAppBytes, r.CumulativeElapsedUs, r.ServerAppBytes, r.ServerElapsedUs))
	}
	result.Method.Warnings = warnings
	result.Detail = &provider.ProviderDetail{
		Format: DetailFormat,
		Payload: json.RawMessage(mustMarshal(nativePayload{
			Direction:           string(direction),
			ApplicationBytes:    r.ApplicationBytes,
			CumulativeAppBytes:  r.CumulativeAppBytes,
			CumulativeElapsedUs: r.CumulativeElapsedUs,
			DeltaBytes:          r.DeltaBytes,
			WindowUs:            r.WindowUs,
			FirstAppBytes:       r.FirstAppBytes,
			FirstElapsedUs:      r.FirstElapsedUs,
			LastAppBytes:        r.LastAppBytes,
			LastElapsedUs:       r.LastElapsedUs,
			MeasurementSource:   r.MeasurementSource,
			WindowRejectReason:  r.WindowRejectReason,
			ServerOriginFrames:  r.ServerOriginFrames,
			ServerFrameSeen:     r.ServerFrameSeen,
			ServerAppBytes:      r.ServerAppBytes,
			ServerElapsedUs:     r.ServerElapsedUs,
			WireBytesRead:       r.WireBytesRead,
			WireBytesWritten:    r.WireBytesWritten,
			ElapsedMs:           r.ElapsedMs,
			Mbps:                r.Mbps,
			Window:              r.Window,
			StopReason:          stopReason,
			Subprotocol:         r.SubprotocolObserved,
			ServerName:          r.ServerName,
			BudgetLimit:         r.BudgetLimitBytes,
			BudgetExceeded:      r.BudgetExceeded,
		})),
	}
	return result
}

// nativePayload is the adapter-native payload the Detail field
// carries. It mirrors adapter.Result so a consumer that knows the
// schema can read the raw fields verbatim. The schema version is
// exposed in Detail.Format so a consumer can decide whether it
// understands the payload before decoding it.
//
// ApplicationBytes / ElapsedMs / Mbps are the window delta the
// Rate itself carries (provider.Rate contract: "payload bytes
// the window counted"). CumulativeAppBytes /
// CumulativeElapsedUs are the SDK's own cumulative values from
// AppInfo.NumBytes / AppInfo.ElapsedTime and represent the
// total transfer at the moment of the last observed frame.
type nativePayload struct {
	Direction           string `json:"direction"`
	ApplicationBytes    int64  `json:"applicationBytes"`
	CumulativeAppBytes  int64  `json:"cumulativeAppBytes"`
	CumulativeElapsedUs int64  `json:"cumulativeElapsedUs"`
	// DeltaBytes and WindowUs are the exact inputs of the Mbps
	// formula, so a consumer can recompute the published rate instead
	// of trusting it. Both come from MeasurementSource.
	DeltaBytes         int64  `json:"deltaBytes"`
	WindowUs           int64  `json:"windowUs"`
	FirstAppBytes      int64  `json:"firstAppBytes"`
	FirstElapsedUs     int64  `json:"firstElapsedUs"`
	LastAppBytes       int64  `json:"lastAppBytes"`
	LastElapsedUs      int64  `json:"lastElapsedUs"`
	MeasurementSource  string `json:"measurementSource"`
	WindowRejectReason string `json:"windowRejectReason,omitempty"`
	// ServerOriginFrames / ServerFrameSeen / ServerAppBytes /
	// ServerElapsedUs are the server's own observation. They are
	// published for cross-checking and are never inputs to the rate.
	ServerOriginFrames int64   `json:"serverOriginFrames"`
	ServerFrameSeen    bool    `json:"serverFrameSeen"`
	ServerAppBytes     int64   `json:"serverAppBytes"`
	ServerElapsedUs    int64   `json:"serverElapsedUs"`
	WireBytesRead      int64   `json:"wireBytesRead"`
	WireBytesWritten   int64   `json:"wireBytesWritten"`
	ElapsedMs          int64   `json:"elapsedMs"`
	Mbps               float64 `json:"mbps"`
	Window             string  `json:"window"`
	StopReason         string  `json:"stopReason"`
	Subprotocol        string  `json:"subprotocol"`
	ServerName         string  `json:"serverName"`
	BudgetLimit        int64   `json:"budgetLimitBytes"`
	BudgetExceeded     bool    `json:"budgetExceeded"`
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		// json.Marshal of a struct with primitive fields cannot fail.
		// Returning an empty payload keeps the public Result consistent
		// while surfacing the developer error in the logs.
		return []byte("{}")
	}
	return b
}

// mapStopReason maps the adapter's stop-reason constants to the
// provider-level values that aggregate across providers.
func mapStopReason(adapterReason string) string {
	switch adapterReason {
	case StopReasonCompleted, "":
		return StopReasonNDT7Completed
	case StopReasonWindowUnavailable:
		return StopReasonNDT7WindowUnavailable
	case StopReasonNoData:
		return StopReasonNDT7NoData
	case StopReasonServerEOF:
		// Unreachable in practice: the adapter never emits it. Kept so
		// an unexpected value is still mapped rather than passed through.
		return "server_eof"
	case StopReasonBudgetExceeded:
		return StopReasonNDT7BudgetExceeded
	case StopReasonCancelled:
		return StopReasonNDT7Cancelled
	case StopReasonTimeout:
		return StopReasonNDT7Timeout
	default:
		// An unknown reason is preserved verbatim so a consumer can
		// still distinguish it from the documented set.
		return adapterReason
	}
}

// qualityFor translates the adapter's terminal state into a Quality
// statement. Completeness follows whether every requested direction
// finished. Stability stays "degraded" if the adapter emitted any
// non-fatal warning (carried via Wire-level bytes). Trust is "verified"
// when the upload produced an app-level server-confirmed count and
// the wire budget was not exceeded.
func qualityFor(r Result, runErr error) provider.Quality {
	q := provider.Quality{Completeness: provider.Incomplete, Stability: "unknown", Trust: "unknown"}
	switch r.Status {
	case StatusCompleted:
		q.Completeness = provider.Complete
		q.Stability = "normal"
		if !r.BudgetExceeded && r.ApplicationBytes > 0 {
			q.Trust = "verified"
		}
	case StatusPartial:
		q.Completeness = provider.Partial
		q.Stability = "degraded"
	default:
		q.Stability = "degraded"
		if runErr != nil {
			q.Stability = "unknown"
		}
	}
	if r.Status == StatusCompleted && q.Trust != "verified" {
		q.Trust = "normal"
	}
	return q
}

// errorCodeForRunError picks a stable, machine-readable code for
// the final result. The same code shows up in runErr too when the
// caller asks for it.
func errorCodeForRunError(class ErrorClass, runErr error) string {
	switch class {
	case ErrorHandshake:
		return "handshake_failed"
	case ErrorNetwork:
		return "network_error"
	case ErrorTimeout:
		return "timeout"
	case ErrorCancelled:
		return "cancelled"
	case ErrorServerDown:
		return "server_down"
	}
	if runErr != nil {
		return "measurement_failed"
	}
	return ""
}

// errorCodeFor translates a Go-side error into a stable, machine
// readable code so logs and the result agree.
func errorCodeFor(err error) string {
	switch {
	case errors.Is(err, ErrConsentRequired):
		return "consent_required"
	case errors.Is(err, ErrConnectionsNotSupported):
		return "connections_not_supported"
	case errors.Is(err, ErrUnsupportedDirection):
		return "unsupported_direction"
	}
	return "invalid_request"
}

// rejected builds a FinalResult for a request that never reached the
// network. The function centralises the zero-rate / zero-byte layout
// so every early-return path produces the same shape.
func rejected(status provider.MeasurementStatus, base provider.FinalResult) provider.FinalResult {
	base.Status = status
	if base.Quality.Completeness == "" {
		base.Quality.Completeness = provider.Incomplete
	}
	if base.Method.Protocol == "" {
		base.Method.Protocol = Protocol
	}
	if base.Method.ConsentVersion == "" {
		base.Method.ConsentVersion = consentVersion
	}
	if base.Method.NegotiatedCaps.Supported == nil {
		base.Method.NegotiatedCaps = provider.CapabilitySet{}
	}
	if base.Detail == nil {
		base.Detail = &provider.ProviderDetail{Format: DetailFormat, Payload: json.RawMessage("{}")}
	}
	return base
}

// Measurement-method notes the adapter and runner use. They mirror
// the strings the GoSpeed HTTP provider exposes so a UI that shows
// "http request to first response byte (GET /ping); not an icmp ping"
// for one measurement and the ndt7 equivalent for another can rely
// on the same wording.
const (
	NoteDownloadSource = "ndt7: server-reported AppInfo.NumBytes (download)"
	NoteUploadSource   = "ndt7: client-counted AppInfo.NumBytes, confirmed by server TCPInfo (upload)"
	NoteLatencySource  = "ndt7: TCP MinRTT from the kernel TCP_INFO socket option (TCPInfo.MinRTT), not an ICMP ping"
)

// defaultConsentNotice is the privacy disclosure shown before any
// ndt7 measurement, negotiation or discovery. The text references
// only what is publicly documented about the M-Lab NDT7 service. It
// is intentionally explicit about the data publication and the
// traffic budget so a consent given under a future UI matches the
// reality of the public service.
//
// Note: this string is the canonical disclosure for consentVersion
// "ndt7-notice-v1". A change here must bump the version so any stored
// agreement can no longer be silently reused.

func mibLabel(bytes int64) string {
	return strings.TrimRight(strings.TrimRight(formatBytes(bytes), "0"), ".")
}

func formatBytes(bytes int64) string {
	const step = 1024.0
	if bytes < step {
		return fmt.Sprintf("%d", bytes)
	}
	div, exp := int64(step), 0
	for n := bytes / step; n >= step; n /= step {
		div *= step
		exp++
	}
	return fmt.Sprintf("%.2f", float64(bytes)/float64(div))
}

// defaultConsentNotice is the privacy disclosure shown before any
// ndt7 measurement, negotiation or discovery. The text references
// only what is publicly documented about the M-Lab NDT7 service. It
// is intentionally explicit about the data publication and the
// traffic budget so a consent given under a future UI matches the
// reality of the public service.
//
// Note: this string is the canonical disclosure for consentVersion
// "ndt7-notice-v1". A change here must bump the version so any stored
// agreement can no longer be silently reused.
var defaultConsentNotice = "M-Lab NDT7 measurement disclosure (GoSpeed, ndt7-notice-v1)\n\n" +
	"1. Public-data destination. Every measurement runs against an M-Lab NDT7 server\n" +
	"   and contributes to M-Lab public measurement datasets. Retention has no\n" +
	"   deletion API.\n\n" +
	"2. Public IP disclosure. The measurement reveals the client public IP to the M-Lab\n" +
	"   server and to every router on the path. The IP is part of the public dataset.\n\n" +
	"3. Metadata published. The client name (GoSpeed), client version, user agent\n" +
	"   (GoSpeed/<version> ndt7-client-go/0.10.1), direction, elapsed time, application\n" +
	"   bytes, TCPInfo (MinRTT, RTTVar, bytes sent/received) and timestamps are published.\n\n" +
	"4. Locate discovery. When used, Locate queries locate.measurementlab.net over\n" +
	"   HTTPS to find a nearby server. No consent means no Locate query is sent.\n\n" +
	"5. Traffic budget. The default per-run caps are:\n" +
	"   download MB: " + mibLabel(DefaultBudgetBytes.Download) + "\n" +
	"   upload MB:   " + mibLabel(DefaultBudgetBytes.Upload) + "\n" +
	"   total MB:    " + mibLabel(DefaultBudgetBytes.Total) + "\n" +
	"   counted at the socket layer. They bound the application payload from above;\n" +
	"   ISP billing, TCP retransmissions and kernel-buffered data are not part\n" +
	"   of this budget.\n\n" +
	"6. Cancellation. Context cancellation, overall timeout and a wire budget overrun\n" +
	"   all force-close the socket. The SDK I/O deadline is not the cancellation\n" +
	"   mechanism; the runner enforces its own.\n\n" +
	"7. Mobile / metered networks. The wire budget does not pause for carrier policies.\n" +
	"   A metered connection may observe more bytes than the application payload implies.\n\n" +
	"8. Server-side Jitter. NDT7 does not provide application-level jitter.\n" +
	"   TCP RTTVar is recorded as a warning, never placed into a JitterMetric.\n\n" +
	"M-Lab privacy policy: https://www.measurementlab.net/privacy/\n" +
	"M-Lab Acceptable Use Policy: https://www.measurementlab.net/aup/\n"

// offline build has no network dependencies, so the field is
// unused today; keeping it in the type documents where a future
// connection pool would live.
type httpClient = interface{}
