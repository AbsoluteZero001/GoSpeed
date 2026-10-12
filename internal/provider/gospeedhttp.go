package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// ProtocolGoSpeedHTTP names the measurement method of every result this
// provider produces. Aggregations across providers must group by this value:
// an HTTP request to first response byte never enters the same statistical set
// as an NDT7 TCP MinRTT.
const ProtocolGoSpeedHTTP = "gospeed-http"

// Metric and capability notes. They are exported through CapabilitySet.Notes
// and FinalResult so a consumer can never show an HTTP round trip time as an
// ICMP ping, or a rate without the window it was measured over.
const (
	// NoteLatencySource describes what the latency metric really is.
	NoteLatencySource = "http request to first response byte (GET /ping); not an icmp ping"
	// NoteJitterSource describes the jitter derivation.
	NoteJitterSource = "mean absolute difference of consecutive http rtt samples"
	// NoteDownloadSource describes how the download rate is counted.
	NoteDownloadSource = "client counted payload bytes over the shared measurement window"
	// NoteUploadSource describes how the upload rate is counted. The upload
	// phase is the only direction with an independent server side byte count.
	NoteUploadSource = "client counted payload bytes over the shared measurement window, confirmed by the server byte count"
	// NoteLegacyCapabilities explains a capability set that was inferred
	// instead of confirmed by the server.
	NoteLegacyCapabilities = "inferred: server does not implement GET /capabilities (legacy), " +
		"a GoSpeed HTTP server has served /ping, /download and /upload since v0.1.0"
)

// timeoutMargin keeps the engine phase timeout above the transfer window.
// The engine rejects a run whose timeout is not longer than the duration
// (ErrInvalidOptions) so that a transfer can always be confirmed by the server.
const timeoutMargin = 5 * time.Second

// Sentinel errors of the GoSpeed HTTP provider. Callers match them with
// errors.Is to separate "this request can never be served" from "the
// measurement ran and failed".
var (
	// ErrInvalidRequest reports a RunRequest the provider cannot execute as
	// written, for example a budget that cannot cover the requested
	// directions. The provider fails it instead of silently trimming it.
	ErrInvalidRequest = errors.New("provider: invalid run request")
	// ErrCapabilityNotNegotiated reports a direction that was requested but is
	// not part of the negotiated capability set.
	ErrCapabilityNotNegotiated = errors.New("provider: capability was not negotiated for this run")
	// ErrNegotiationFailed reports that the capability probe itself did not
	// produce an answer, so nothing can be confirmed.
	ErrNegotiationFailed = errors.New("provider: capability negotiation did not produce an answer")
)

// GoSpeedHTTPProvider adapts the GoSpeed measurement engine to the provider
// Runner interface.
//
// It owns no measurement logic. Every number it reports is produced by
// speedtest.Engine: the adapter only maps engine options onto a RunRequest,
// engine results onto FinalResult, and engine errors onto a MeasurementStatus.
// It therefore never re-derives a rate, a latency or a byte count.
//
// Cancellation is context only. There is no Cancel method and no package level
// run state; the caller owns the run scope through ctx.
type GoSpeedHTTPProvider struct {
	client *http.Client
	engine *speedtest.Engine
	// ownsClient records that client was created here because the caller
	// passed none, so CloseIdleConnections does not release a client the
	// caller still owns.
	ownsClient bool
}

// NewGoSpeedHTTP returns a provider backed by client. A nil client installs the
// engine's own HTTP client (compression disabled, keep-alive on) and the
// provider then owns it.
func NewGoSpeedHTTP(client *http.Client) *GoSpeedHTTPProvider {
	owns := false
	if client == nil {
		client = speedtest.NewHTTPClient()
		owns = true
	}
	return &GoSpeedHTTPProvider{client: client, engine: speedtest.NewEngine(client), ownsClient: owns}
}

// ID returns the provider identity. GoSpeed HTTP nodes come from the user's
// own configuration, so this provider never discloses anything the user has not
// already chosen to contact.
func (p *GoSpeedHTTPProvider) ID() ID { return IDGoSpeedHTTP }

// CloseIdleConnections releases pooled connections. It only affects a client the
// provider created itself; a caller supplied client stays the caller's
// responsibility. It is deliberately not part of Runner.
func (p *GoSpeedHTTPProvider) CloseIdleConnections() {
	if p.ownsClient && p.client != nil {
		p.client.CloseIdleConnections()
	}
}

// Negotiate probes GET /capabilities on the node and reports only what the
// answer confirms.
//
// The probe reuses the engine's own negotiation path (speedtest.FetchCapabilities),
// so the request, the legacy 404/405 rule and the advertised limits are exactly
// the ones a measurement would see. No second capability parser exists here.
//
// Honesty rules:
//   - A probe that failed (network error, unexpected status, unparsable body)
//     yields no capabilities and an error. Nothing is guessed.
//   - A server that answers 200 but advertises no capability confirms nothing.
//   - A legacy server that does not implement the endpoint keeps the engine's
//     "continue anyway" rule, and the inferred set is labelled as inferred.
//   - Jitter is only claimed together with latency because it is derived from
//     the latency samples. Packet loss and loaded latency are never claimed:
//     the engine runs no packet level measurement, and an HTTP failure rate is
//     not a loss rate.
func (p *GoSpeedHTTPProvider) Negotiate(ctx context.Context, node Node) (CapabilitySet, error) {
	target, err := targetFor(node)
	if err != nil {
		return CapabilitySet{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return capabilitiesFrom(speedtest.FetchCapabilities(ctx, p.client, target))
}

// capabilitiesFrom maps a capability answer onto the provider capability set.
func capabilitiesFrom(info speedtest.CapabilityInfo) (CapabilitySet, error) {
	// The probe did not answer. The engine itself records the reason in Error,
	// and an unanswered probe must not turn into an optimistic default set.
	if info.Error != "" && !info.Unsupported {
		return CapabilitySet{}, fmt.Errorf("%w: GET /capabilities: %s", ErrNegotiationFailed, info.Error)
	}

	caps := CapabilitySet{
		Supported: make(map[Capability]bool, 4),
		Notes:     make(map[Capability]string, 4),
	}

	// Legacy rule, identical to the engine's: a server that does not implement
	// the endpoint is usable. The inferred capabilities are labelled so a GUI
	// can say they were inferred rather than confirmed.
	if info.Unsupported || len(info.Capabilities) == 0 {
		if info.Supported && len(info.Capabilities) == 0 {
			return CapabilitySet{}, fmt.Errorf("%w: server answered GET /capabilities without advertising a capability", ErrNegotiationFailed)
		}
		caps.Supported[CapDownload] = true
		caps.Supported[CapUpload] = true
		caps.Supported[CapLatency] = true
		caps.Supported[CapJitter] = true
		caps.Notes[CapDownload] = NoteLegacyCapabilities + "; " + NoteDownloadSource
		caps.Notes[CapUpload] = NoteLegacyCapabilities + "; " + NoteUploadSource
		caps.Notes[CapLatency] = NoteLegacyCapabilities + "; " + NoteLatencySource
		caps.Notes[CapJitter] = NoteLegacyCapabilities + "; " + NoteJitterSource
		return caps, nil
	}

	advertised := make(map[string]bool, len(info.Capabilities))
	for _, name := range info.Capabilities {
		advertised[strings.ToLower(strings.TrimSpace(name))] = true
	}
	if advertised["download"] {
		caps.Supported[CapDownload] = true
		caps.Notes[CapDownload] = NoteDownloadSource
	}
	if advertised["upload"] {
		caps.Supported[CapUpload] = true
		caps.Notes[CapUpload] = NoteUploadSource
	}
	if advertised["latency"] {
		caps.Supported[CapLatency] = true
		caps.Notes[CapLatency] = NoteLatencySource
		// Jitter is the mean absolute difference of consecutive latency samples,
		// so it exists exactly when latency sampling does. A run that collects
		// fewer than two samples still reports a null jitter, which is a
		// missing metric rather than a missing capability.
		caps.Supported[CapJitter] = true
		caps.Notes[CapJitter] = NoteJitterSource
	}
	return caps, nil
}

// Plan reports the traffic budget the provider applies when a RunRequest leaves
// its budget fields at zero: a duration limited run with no byte cap. A zero
// direction means unlimited by default; it is not a carrier billing limit.
func (p *GoSpeedHTTPProvider) Plan() BudgetLimits { return BudgetLimits{} }

// Run performs one measurement through the GoSpeed engine and maps the outcome
// onto FinalResult.
//
// Request mapping:
//
//	Directions  empty  -> every direction the negotiated set supports
//	Directions  set    -> exactly those, and a direction outside the negotiated
//	                      set is refused instead of trimmed
//	Duration    zero   -> the engine default window
//	Budget      zero   -> duration limited, no byte cap
//	latency     runs when the negotiated set contains CapLatency
//
// The engine always runs with the connection count it defaults to
// (speedtest.DefaultConnections): RunRequest carries no connection setting and
// the provider must not invent one. Capability re-negotiation inside the run is
// left enabled so the engine keeps enforcing the server's advertised limits.
//
// The returned FinalResult is meaningful even when err is non-nil: Status
// separates completed, failed, cancelled, timeout and budget_exceeded outcomes.
func (p *GoSpeedHTTPProvider) Run(ctx context.Context, req RunRequest) (FinalResult, error) {
	base := FinalResult{ProviderID: IDGoSpeedHTTP, ServerID: serverIDFor(req.Node)}
	if ctx == nil {
		ctx = context.Background()
	}

	directions, err := selectedDirections(req)
	if err != nil {
		return rejectedResult(base, req, err)
	}

	budget, err := effectiveBudget(req, directions)
	if err != nil {
		return rejectedResult(base, req, err)
	}
	opts, err := optionsFor(req, directions, budget, base.ServerID)
	if err != nil {
		return rejectedResult(base, req, err)
	}

	// The engine reports progress from its sampler goroutine as well as from
	// the run goroutine, so the terminal elapsed time is shared under a lock.
	var (
		mu      sync.Mutex
		elapsed time.Duration
	)
	opts.Progress = func(event speedtest.Progress) {
		if event.Stage == speedtest.StageDone {
			mu.Lock()
			elapsed = event.Elapsed
			mu.Unlock()
		}
		if req.Events == nil {
			return
		}
		// Every field is forwarded from the engine's own observed counters and
		// monotonic clock reads. Nothing is recomputed, resampled or
		// interpolated here, so a consumer's curves keep the engine's exact
		// cadence and values.
		progress := ProgressEvent{
			Phase:             string(event.Phase),
			State:             string(event.Stage),
			RunState:          string(event.State),
			Bytes:             event.Bytes,
			Mbps:              event.Mbps,
			InstantMbps:       event.InstantMbps,
			ElapsedMs:         durationToMs(event.Elapsed),
			ActiveConnections: event.ActiveConnections,
			Samples:           event.Samples,
			Message:           event.Message,
		}
		if event.Budget.Fraction != nil {
			fraction := *event.Budget.Fraction
			progress.BudgetFraction = &fraction
		}
		if event.Budget.Remaining != nil {
			remaining := durationToMs(*event.Budget.Remaining)
			progress.BudgetRemainingMs = &remaining
		}
		req.Events.OnProgress(progress)
	}

	result, runErr := p.engine.Run(ctx, opts)

	mu.Lock()
	runElapsed := elapsed
	mu.Unlock()

	return mapResult(result, runErr, runID{node: req.Node, caps: req.Capabilities, budget: budget, elapsed: runElapsed}), runErr
}

// runID carries the request facts a Result cannot supply on its own.
type runID struct {
	node    Node
	caps    CapabilitySet
	budget  BudgetLimits
	elapsed time.Duration
}

// rejectedResult builds the result of a request that never reached the network.
func rejectedResult(base FinalResult, req RunRequest, err error) (FinalResult, error) {
	base.Status = StatusFailed
	base.ErrorCode = errorCodeFor(err)
	base.ErrorMessage = err.Error()
	base.Method = MethodRecord{
		Protocol:       ProtocolGoSpeedHTTP,
		NegotiatedCaps: req.Capabilities,
		Budget:         req.Budget,
		Warnings:       warningsFor(speedtest.Result{}, req.Node, req.Capabilities),
	}
	base.Quality = Quality{Completeness: Incomplete, Stability: "unknown", Trust: "unknown"}
	return base, err
}

// serverIDFor names the measured node. The engine records the same id.
func serverIDFor(node Node) string {
	if id := strings.TrimSpace(node.ID); id != "" {
		return id
	}
	if name := strings.TrimSpace(node.Name); name != "" {
		return name
	}
	return ""
}

// targetFor converts a provider node into an engine target. Only the address is
// taken from the node: the engine normalizes and classifies the URL itself.
func targetFor(node Node) (speedtest.Target, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(node.Endpoint), "/")
	if endpoint == "" {
		return speedtest.Target{}, fmt.Errorf("%w: node %q has no endpoint", ErrInvalidRequest, serverIDFor(node))
	}
	target := speedtest.Target{ID: serverIDFor(node), Name: strings.TrimSpace(node.Name), BaseURL: endpoint}
	if target.Name == "" {
		target.Name = target.ID
	}
	return target, nil
}

// selectedDirections resolves the directions a run will execute. An empty list
// means "every direction the negotiated set supports"; an explicit list must be
// covered by the negotiated set, because trimming a request silently would make
// the result describe something else than what was asked for.
func selectedDirections(req RunRequest) ([]Direction, error) {
	if len(req.Directions) == 0 {
		var dirs []Direction
		if req.Capabilities.Supports(CapDownload) {
			dirs = append(dirs, DirectionDownload)
		}
		if req.Capabilities.Supports(CapUpload) {
			dirs = append(dirs, DirectionUpload)
		}
		if len(dirs) == 0 {
			return nil, fmt.Errorf("%w: the request has no directions and the negotiated capability set contains neither download nor upload", ErrInvalidRequest)
		}
		return dirs, nil
	}

	seen := make(map[Direction]bool, len(req.Directions))
	dirs := make([]Direction, 0, len(req.Directions))
	for _, direction := range req.Directions {
		if seen[direction] {
			continue
		}
		switch direction {
		case DirectionDownload:
			if !req.Capabilities.Supports(CapDownload) {
				return nil, fmt.Errorf("%w: download was requested but is not in the negotiated capability set", ErrCapabilityNotNegotiated)
			}
		case DirectionUpload:
			if !req.Capabilities.Supports(CapUpload) {
				return nil, fmt.Errorf("%w: upload was requested but is not in the negotiated capability set", ErrCapabilityNotNegotiated)
			}
		default:
			return nil, fmt.Errorf("%w: unknown direction %q", ErrCapabilityNotNegotiated, string(direction))
		}
		seen[direction] = true
		dirs = append(dirs, direction)
	}
	return dirs, nil
}

// effectiveBudget resolves the request budget into the single per phase byte cap
// the engine can enforce.
//
// The engine has exactly one MaxBytes value, applied as a shared aggregate cap
// inside each transfer phase. A total budget is therefore split evenly across
// the directions of this run, and a direction specific limit may only lower the
// result, never raise it. Zero means unlimited in both layers.
//
// The returned BudgetLimits records the caps that were actually in effect, so a
// result states on which layer its traffic was counted.
func effectiveBudget(req RunRequest, directions []Direction) (BudgetLimits, error) {
	budget := BudgetLimits{}
	if req.Budget.DownloadBytes < 0 || req.Budget.UploadBytes < 0 || req.Budget.TotalBytes < 0 {
		return budget, fmt.Errorf("%w: a budget must not be negative", ErrInvalidRequest)
	}

	share := int64(0)
	if req.Budget.TotalBytes > 0 {
		if req.Budget.TotalBytes < int64(len(directions)) {
			return budget, fmt.Errorf("%w: a total budget of %d bytes cannot cover %d directions",
				ErrInvalidRequest, req.Budget.TotalBytes, len(directions))
		}
		share = req.Budget.TotalBytes / int64(len(directions))
	}

	tightest := func(perDirection int64) int64 {
		result := share
		if perDirection > 0 && (result == 0 || perDirection < result) {
			result = perDirection
		}
		return result
	}

	for _, direction := range directions {
		switch direction {
		case DirectionDownload:
			budget.DownloadBytes = tightest(req.Budget.DownloadBytes)
		case DirectionUpload:
			budget.UploadBytes = tightest(req.Budget.UploadBytes)
		}
	}
	return budget, nil
}

// optionsFor builds the engine options for a request. The phase timeout is
// always derived so it stays above the transfer window.
func optionsFor(req RunRequest, directions []Direction, budget BudgetLimits, serverID string) (speedtest.Options, error) {
	target, err := targetFor(req.Node)
	if err != nil {
		return speedtest.Options{}, err
	}
	target.ID = serverID

	var phases speedtest.PhaseMask
	for _, direction := range directions {
		switch direction {
		case DirectionDownload:
			phases |= speedtest.PhasesDownload
		case DirectionUpload:
			phases |= speedtest.PhasesUpload
		}
	}
	if req.Capabilities.Supports(CapLatency) {
		phases |= speedtest.PhasesLatency
	}
	if phases == 0 {
		return speedtest.Options{}, fmt.Errorf("%w: the request selects no measurable phase", ErrInvalidRequest)
	}

	timeout := speedtest.DefaultTimeout
	if req.Duration > 0 {
		if extended := req.Duration + timeoutMargin; extended > timeout {
			timeout = extended
		}
	}

	maxBytes := int64(0)
	for _, cap := range []int64{budget.DownloadBytes, budget.UploadBytes} {
		if cap > 0 && (maxBytes == 0 || cap < maxBytes) {
			maxBytes = cap
		}
	}

	// Connections is forwarded exactly as requested and is never clamped here.
	// The engine owns the supported range and rejects an out of range value
	// with its own error, so a caller that asks for sixteen connections can
	// never silently receive one. Zero keeps the unchanged engine default.
	connections := req.Connections
	if connections == 0 {
		connections = speedtest.DefaultConnections
	}

	return speedtest.Options{
		Target:          target,
		Phases:          phases,
		Duration:        req.Duration,
		MaxBytes:        maxBytes,
		Timeout:         timeout,
		Connections:     connections,
		Warmup:          req.Warmup,
		SampleInterval:  req.SampleInterval,
		LatencySamples:  req.LatencySamples,
		LatencyInterval: req.LatencyInterval,
		// Keep the engine's own limit enforcement: it refuses a run that
		// exceeds the server's advertised limits and records the answer in the
		// result.
		NegotiateCapabilities: true,
	}, nil
}

// mapResult turns an engine result and engine error into a FinalResult.
func mapResult(result speedtest.Result, runErr error, id runID) FinalResult {
	out := FinalResult{
		ProviderID: IDGoSpeedHTTP,
		ServerID:   id.serverID(),
		ElapsedMs:  id.elapsed.Milliseconds(),
		Method: MethodRecord{
			Protocol:       ProtocolGoSpeedHTTP,
			NegotiatedCaps: id.caps,
			Budget:         id.budget,
			Warnings:       warningsFor(result, id.node, id.caps),
		},
	}
	if out.ServerID == "" {
		out.ServerID = result.Target.ServerID
	}

	out.Download = rateFrom(result.Download)
	// The upload phase embeds TransferResult, so the pointer must only be
	// taken after the upload itself is known to exist. Taking the address of
	// the embedded field of a nil pointer would fault.
	var upload *speedtest.TransferResult
	if result.Upload != nil {
		upload = &result.Upload.TransferResult
	}
	out.Upload = rateFrom(upload)
	out.Latency, out.Jitter = latencyMetrics(result.Latency)
	// Total payload of the run. Each direction is counted once by the engine,
	// so summing the two directions cannot double count.
	out.TransferredBytes = transferredBytes(result)

	// Scope is resolved once, from the address the engine actually measured and
	// never from the observed rate: a fast loopback run is local and a slow
	// public run is remote. Every other field that mentions the scope uses this
	// one value so the result cannot contradict itself.
	scope := scopeOf(result, id.node)

	out.Status, out.ErrorCode, out.ErrorMessage = classify(runErr, result, id.budget)
	out.Quality = qualityFor(out, result, id.node, scope)
	out.Scope = scope
	out.Ownership = id.node.Ownership
	out.Discovery = id.node.Discovery
	if detail, err := detailOf(result); err != nil {
		// The detail channel is an addition; it is never a reason to fail or
		// degrade a measurement that already produced evidence.
		out.Method.Warnings = append(out.Method.Warnings,
			fmt.Sprintf("provider native detail could not be encoded: %v", err))
	} else {
		out.Detail = detail
	}
	return out
}

// scopeOf reports where the node really was, from the address the engine
// classified. A measured rate never influences the scope.
func scopeOf(result speedtest.Result, node Node) Scope {
	switch result.Target.NetworkScope {
	case speedtest.ScopeLocal:
		return ScopeLocal
	case speedtest.ScopeLAN:
		return ScopeLAN
	case speedtest.ScopeRemote:
		return ScopeRemote
	}
	// The engine could not derive one (an unresolved host stays unknown), so
	// fall back to the classification the node configuration carried. A public
	// scope is never guessed.
	switch node.Scope {
	case ScopeLocal, ScopeLAN, ScopeRemote:
		return node.Scope
	default:
		return ScopeUnknown
	}
}

// detailOf encodes the engine's own result verbatim. Nothing is rebuilt, so a
// consumer of this payload reads real values and never a reconstructed one.
//
// It is skipped when the engine rejected the options before starting, because
// then there is no result worth reporting.
func detailOf(result speedtest.Result) (*ProviderDetail, error) {
	if result.TestID == "" && result.Timestamp.IsZero() {
		return nil, nil
	}
	// The engine result records the node address verbatim, and a configured
	// endpoint may embed credentials (https://user:pass@host). A result is
	// routinely written to disk or shared, so the userinfo component is removed
	// before the payload leaves the provider. This is the only field that is
	// altered; every measured value stays exactly what the engine produced.
	redacted := result
	if address, changed := redactURLCredentials(result.Target.ServerAddress); changed {
		redacted.Target.ServerAddress = address
	}
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return nil, err
	}
	return &ProviderDetail{Format: DetailFormatGoSpeedResult, Payload: encoded}, nil
}

// redactURLCredentials drops the userinfo component of an address. It reports
// whether anything was removed, so a clean address is left byte identical.
func redactURLCredentials(address string) (string, bool) {
	if address == "" || !strings.Contains(address, "//") {
		return address, false
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.User == nil {
		return address, false
	}
	parsed.User = nil
	return parsed.String(), true
}

// serverID is a small accessor so mapResult can fill a missing id.
func (r runID) serverID() string { return serverIDFor(r.node) }

// rateFrom maps one transfer result. A phase that never ran stays nil, which is
// a missing metric rather than a zero rate.
func rateFrom(transfer *speedtest.TransferResult) *Rate {
	if transfer == nil {
		return nil
	}
	mbps := transfer.Mbps
	return &Rate{
		Mbps:             &mbps,
		ElapsedMs:        transfer.DurationNs.Milliseconds(),
		TransferredBytes: transfer.Bytes,
		Window:           transfer.MeasurementWindow,
		StopReason:       string(transfer.StopReason),
	}
}

// latencyMetrics maps the HTTP RTT statistics. Jitter stays completely null
// (nil pointer, not a zero value) unless the engine collected at least two
// samples, because the engine leaves JitterNs nil in that case.
func latencyMetrics(latency *speedtest.LatencyResult) (*LatencyMetric, *JitterMetric) {
	if latency == nil || latency.SuccessfulSamples == 0 {
		return nil, nil
	}
	if latency.Type != speedtest.LatencyHTTPRTT {
		// The provider only ever produces HTTP RTT. A different type would be
		// a metric this package must not relabel, so it is dropped rather than
		// presented as an HTTP measurement.
		return nil, nil
	}
	if belowClockResolution(latency) {
		// Every sample rounded to zero on the platform clock. The round trip
		// happened, but its duration was not resolvable, so publishing 0.00 ms
		// would claim a measured value that was never obtained. Both the
		// latency and the jitter derived from these samples stay null.
		return nil, nil
	}

	value := durationToMs(latency.AverageNs)
	metric := &LatencyMetric{ValueMs: &value, Type: MetricHTTPRTT, Source: NoteLatencySource}
	if latency.JitterNs == nil {
		return metric, nil
	}
	jitter := durationToMs(*latency.JitterNs)
	return metric, &JitterMetric{ValueMs: &jitter, Type: JitterConsecutiveDiff, Source: NoteJitterSource}
}

// belowClockResolution reports that no latency sample was large enough to be
// measured. On a loopback link every request can complete inside one tick of a
// coarse clock, which produces a set of zero samples: a real observation that
// the quantity was too small to resolve, not a measurement of zero.
func belowClockResolution(latency *speedtest.LatencyResult) bool {
	if len(latency.SamplesNs) == 0 {
		return latency.AverageNs == 0
	}
	for _, sample := range latency.SamplesNs {
		if sample > 0 {
			return false
		}
	}
	return true
}

// durationToMs converts a nanosecond duration to milliseconds as a float, so no
// precision is lost for sub-millisecond samples.
func durationToMs(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// transferredBytes is the run's total payload. Each direction contributes the
// bytes the engine counted for it exactly once.
func transferredBytes(result speedtest.Result) int64 {
	var total int64
	if result.Download != nil {
		total += result.Download.Bytes
	}
	if result.Upload != nil {
		total += result.Upload.Bytes
	}
	return total
}

// classify maps the engine outcome onto a terminal status.
//
// Budget semantics, verified against the engine before being encoded here: the
// engine's MaxBytes is an exact shared cap, and a transfer that stops exactly at
// it is a planned measurement. It carries StopReasonRequestedBytes and the
// engine reports the run as completed, so the adapter must keep reporting
// StatusCompleted. StatusBudgetExceeded is reserved for a budget integrity
// violation: the server confirmed more bytes than the cap allows, or the client
// and server byte counts disagree while a budget was in force.
func classify(runErr error, result speedtest.Result, budget BudgetLimits) (MeasurementStatus, string, string) {
	if runErr == nil {
		if result.Status == speedtest.StatusCompleted {
			return StatusCompleted, "", ""
		}
		return StatusFailed, "engine_incomplete", result.ErrorMessage
	}
	switch {
	case errors.Is(runErr, context.Canceled):
		return StatusCancelled, "cancelled", runErr.Error()
	case errors.Is(runErr, context.DeadlineExceeded):
		return StatusTimeout, "timeout", runErr.Error()
	case budgetOverdraw(result, budget):
		// Only a provable overdraw of the budget is a budget abort. The engine's
		// shared byte budget is an exact cap, so reaching it is a completed
		// measurement; this branch requires recorded bytes above the cap.
		return StatusBudgetExceeded, "budget_exceeded", runErr.Error()
	}
	// Everything else is a failed measurement with a specific code. In
	// particular a client/server byte count disagreement is NOT a budget
	// abort: ErrByteCountMismatch says the two counts differ, not that a budget
	// stopped the transfer.
	return StatusFailed, errorCodeFor(runErr), runErr.Error()
}

// budgetOverdraw reports a provable overdraw of the byte budget: the engine
// recorded more payload than the effective cap allowed.
//
// It deliberately does NOT treat a byte count mismatch as a budget abort, and
// it does NOT treat a planned stop at the cap as one either. Budget status is
// reserved for evidence that the budget itself was overrun; every other
// accounting failure keeps its own specific error code.
func budgetOverdraw(result speedtest.Result, budget BudgetLimits) bool {
	cap := budget.DownloadBytes
	if budget.UploadBytes > 0 && (cap == 0 || budget.UploadBytes < cap) {
		cap = budget.UploadBytes
	}
	if cap <= 0 {
		return false
	}
	if result.Download != nil && result.Download.Bytes > cap {
		return true
	}
	return result.Upload != nil && result.Upload.ServerConfirmedBytes > cap
}

// errorCodeFor maps an engine sentinel error onto a stable machine readable code.
func errorCodeFor(err error) string {
	switch {
	case errors.Is(err, ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(err, ErrCapabilityNotNegotiated):
		return "capability_not_negotiated"
	case errors.Is(err, ErrNegotiationFailed):
		return "negotiation_failed"
	case errors.Is(err, speedtest.ErrInvalidOptions):
		return "invalid_options"
	case errors.Is(err, speedtest.ErrInvalidTarget):
		return "invalid_target"
	case errors.Is(err, speedtest.ErrUnsupportedProtocol):
		return "unsupported_protocol"
	case errors.Is(err, speedtest.ErrUnsupportedConnections):
		return "unsupported_connections"
	case errors.Is(err, speedtest.ErrServerLimitExceeded):
		return "server_limit_exceeded"
	case errors.Is(err, speedtest.ErrServerCapabilityMissing):
		return "server_capability_missing"
	case errors.Is(err, speedtest.ErrIncompleteTransfer):
		return "incomplete_transfer"
	case errors.Is(err, speedtest.ErrNoDataTransferred):
		return "no_data_transferred"
	case errors.Is(err, speedtest.ErrByteCountMismatch):
		return "byte_count_mismatch"
	case errors.Is(err, speedtest.ErrAllConnectionsFailed):
		return "all_connections_failed"
	case errors.Is(err, speedtest.ErrAllSamplesFailed):
		return "all_samples_failed"
	case errors.Is(err, speedtest.ErrUnexpectedStatus):
		return "unexpected_status"
	case errors.Is(err, speedtest.ErrZeroDuration):
		return "zero_duration"
	case errors.Is(err, speedtest.ErrInvalidBytes):
		return "invalid_bytes"
	case err != nil:
		return "measurement_failed"
	default:
		return ""
	}
}

// warningsFor collects the non fatal observations of a run. The engine's own
// warnings survive unchanged, which is what keeps a partial multi connection
// failure visible in the unified result.
func warningsFor(result speedtest.Result, node Node, caps CapabilitySet) []string {
	warnings := make([]string, 0, len(result.Warnings)+3)
	warnings = append(warnings, result.Warnings...)
	if latency := result.Latency; latency != nil {
		if latency.FailedSamples > 0 {
			warnings = append(warnings, fmt.Sprintf("latency: %d of %d http rtt samples failed", latency.FailedSamples, latency.Attempts))
		}
		if latency.JitterNs == nil && latency.SuccessfulSamples > 0 {
			warnings = append(warnings, "jitter is null: fewer than two http rtt samples were collected")
		}
	}
	if strings.HasPrefix(caps.Note(CapDownload), NoteLegacyCapabilities) ||
		strings.HasPrefix(caps.Note(CapUpload), NoteLegacyCapabilities) {
		warnings = append(warnings, "capabilities were inferred from a legacy server that does not implement GET /capabilities")
	}
	if result.Target.Capabilities != nil && result.Target.Capabilities.Unsupported {
		warnings = append(warnings, "legacy server: GET /capabilities is not implemented, engine limits were not enforced")
	}
	if scopeOf(speedtest.Result{}, node) == ScopeRemote {
		warnings = append(warnings, "remote node: the rate is limited by the server, the path and local congestion, not by the local line alone")
	}
	if len(warnings) == 0 {
		return nil
	}
	return warnings
}

// qualityFor states how complete and how trustworthy the evidence is.
//
// Stability is about the sampling integrity: a run that lost samples or
// connections is degraded. Trust is about whether the bytes were corroborated
// independently: only the upload direction has a server side byte count, so a
// completed upload earns a verified trust, while a download only run stays
// normal because the byte count is the client's own.
func qualityFor(out FinalResult, result speedtest.Result, node Node, scope Scope) Quality {
	quality := Quality{}
	warnings := out.Method.Warnings

	measured := 0
	if out.Download != nil {
		measured++
	}
	if out.Upload != nil {
		measured++
	}
	switch {
	case out.Status == StatusCompleted:
		quality.Completeness = Complete
	case measured > 0:
		// At least one requested direction produced real evidence while the
		// run as a whole did not complete.
		quality.Completeness = Partial
	default:
		quality.Completeness = Incomplete
	}

	switch {
	case out.Status != StatusCompleted:
		quality.Stability = "unknown"
	case len(warnings) > 0:
		quality.Stability = "degraded"
	default:
		quality.Stability = "normal"
	}

	serverConfirmed := result.Upload != nil &&
		result.Upload.ServerConfirmedBytes > 0 &&
		result.Upload.ServerConfirmedBytes == result.Upload.Bytes
	switch {
	case out.Status != StatusCompleted:
		quality.Trust = "unknown"
	case serverConfirmed && len(warnings) == 0:
		quality.Trust = "verified"
	default:
		quality.Trust = "normal"
	}

	notes := make([]string, 0, 2)
	// The note must state the same scope the result reports, otherwise a
	// consumer would read "local" and "node scope: unknown" side by side.
	if scope != "" {
		notes = append(notes, "node scope: "+string(scope))
	}
	if result.Target.Local {
		notes = append(notes, "target is a loopback address: the rate describes local throughput, not internet bandwidth")
	}
	quality.Notes = notes
	return quality
}
