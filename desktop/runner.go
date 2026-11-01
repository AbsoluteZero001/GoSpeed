package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// ErrBusy reports that another cancellable task is already running. The GUI
// allows one measurement or node check at a time so results and cancellation
// stay unambiguous.
var ErrBusy = errors.New("gospeed: another task is already running")

// GUI limits and probe settings. The probe settings mirror "gospeed nodes
// auto", so automatic selection in the GUI ranks nodes exactly like the CLI.
const (
	minDurationMs       = 1000
	maxDurationMs       = 300000
	minSampleIntervalMs = 50
	maxSampleIntervalMs = 5000

	probeAttempts       = 2
	probeTimeout        = 5 * time.Second
	probeLatencySamples = 3
	probeParallel       = 4
	checkOverallTimeout = 2 * time.Minute

	// timeoutSlack is added to the requested duration to build the per phase
	// timeout. The engine requires timeout > duration so a transfer can still
	// be confirmed by the server; it is never used to extend the measurement
	// window itself.
	timeoutSlack = 20 * time.Second
)

// EmitFunc delivers one event to the UI. It must return quickly.
type EmitFunc func(name string, payload any)

// The runner resolves its measurement backend through a provider.Registry
// rather than hardcoding the GoSpeed engine. The seam below exists so a test can
// inject a registry, not so the runner can pick a different measurement service
// at will: only the providers a build registers are reachable, and this build
// registers exactly one.

type session struct {
	kind   string
	cancel context.CancelFunc
	done   chan struct{}
	// provider names the measurement backend the task serves. It is empty
	// for the tasks that are not measurements (a node check, a discovery).
	// The consent withdrawal uses it to cancel an M-Lab measurement without
	// touching a self hosted one.
	provider string
}

// Runner executes measurements and node checks on behalf of the UI. Exactly
// one cancellable task runs at a time; every task releases its HTTP resources
// before the runner reports it as finished.
type Runner struct {
	mu      sync.Mutex
	emit    EmitFunc
	session *session
	state   string
	message string
	probes  map[string]nodes.Probe

	// Test seams. Production values load the same configuration and build the
	// same providers the CLI uses.
	loadNodesFn func() (*nodes.Manager, string, error)
	registryFn  func() (*provider.Registry, error)
	// providers is built once and reused, so a provider instance (and the HTTP
	// client it owns) survives across runs instead of being rebuilt per click.
	providers *provider.Registry
	// ndt7 is the M-Lab NDT7 gateway: the provider instance, the node store
	// and the stored consent decision. It is built once, so the provider
	// instance and the consent it holds survive across runs.
	ndt7    *ndt7Gateway
	ndt7Err error
}

// NewRunner returns a runner that reports through emit.
//
// The M-Lab gateway is built here rather than on first use, so the stored
// consent decision and today's counter are known when the UI mounts. Building
// it performs no network I/O: the counter file is only read when it is
// counted, and the stored consent is only read, never written.
func NewRunner(emit EmitFunc) *Runner {
	if emit == nil {
		emit = func(string, any) {}
	}
	gateway, gatewayErr := newNDT7Gateway()
	runner := &Runner{
		emit:        emit,
		state:       StateIdle,
		probes:      make(map[string]nodes.Probe),
		loadNodesFn: loadNodeManager,
		ndt7:        gateway,
		ndt7Err:     gatewayErr,
	}
	runner.registryFn = func() (*provider.Registry, error) {
		// The gateway is read through the accessor, so a test that swaps the
		// gateway gets the swapped instance in the registry too.
		current, err := runner.ndt7Gateway()
		if err != nil {
			// The M-Lab gateway could not be built. That must not take the
			// self hosted provider down with it: the registry is built
			// without the M-Lab provider, and the panel reports the reason.
			// A run that asks for the M-Lab provider is then refused by the
			// registry lookup instead of silently measuring something else.
			return newDefaultRegistry(nil)
		}
		return newDefaultRegistry(current)
	}
	return runner
}

// ndt7Gateway returns the M-Lab gateway.
//
// A gateway that could not be built is not fatal: the GoSpeed HTTP provider
// keeps working, and the NDT7 panel reports why the M-Lab path is
// unavailable.
func (r *Runner) ndt7Gateway() (*ndt7Gateway, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ndt7Err != nil {
		return nil, r.ndt7Err
	}
	if r.ndt7 == nil {
		return nil, errors.New("desktop: the M-Lab NDT7 provider is not available")
	}
	return r.ndt7, nil
}

// registry returns the provider registry, building it on first use.
//
// The registry is created lazily and cached, so consecutive measurements reuse
// one provider instance and its connection pool. Release is explicit: Close on
// the application shutdown path, never implicitly between runs.
func (r *Runner) registry() (*provider.Registry, error) {
	r.mu.Lock()
	registry := r.providers
	r.mu.Unlock()
	if registry != nil {
		return registry, nil
	}
	// The builder is called without the lock held: it needs the M-Lab
	// gateway, which takes the same lock.
	built, err := r.registryFn()
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers != nil {
		// Another caller built one first. The first one wins and the extra
		// registry is released, so exactly one registry stays open.
		_ = built.Close()
		return r.providers, nil
	}
	r.providers = built
	return built, nil
}

// CloseProviders releases the provider resources this runner created. It is
// called on shutdown, after every task has finished.
func (r *Runner) CloseProviders() error {
	r.mu.Lock()
	registry := r.providers
	r.providers = nil
	r.mu.Unlock()
	if registry == nil {
		return nil
	}
	return registry.Close()
}

// loadNodes reads the effective node configuration.
func (r *Runner) loadNodes() (*nodes.Manager, string, error) {
	return r.loadNodesFn()
}

// Start validates the request, registers the session and returns immediately.
// The measurement runs in its own goroutine and reports through events.
func (r *Runner) Start(options StartOptions) (StartTestResult, error) {
	normalized, err := options.normalized()
	if err != nil {
		return StartTestResult{}, err
	}
	// The session records the normalized provider: the normalized options
	// are what the run will actually use, and the consent withdrawal has to
	// recognise an M-Lab measurement by exactly that name.
	session, ctx, err := r.begin("test", normalized.Provider)
	if err != nil {
		return StartTestResult{}, err
	}
	r.setState(StatePreparing, "test requested")
	go r.runTest(ctx, session, normalized)
	return StartTestResult{Started: true, Mode: normalized.SelectionMode}, nil
}

// discoverNDT7Nodes asks the M-Lab Locate service for the NDT7 servers near
// the client.
//
// It is a task of its own, not a side effect of a measurement. The runner
// admits one task at a time, so a discovery never runs alongside a transfer:
// a measurement drops the node references when it ends, and a discovery that
// published into a store that is emptied underneath it would lose its
// result.
//
// The caller supplies the parent context; the caller's own deadline still
// applies.
func (r *Runner) discoverNDT7Nodes(parent context.Context) ([]ProviderNodeView, error) {
	session, ctx, err := r.begin("discover", "")
	if err != nil {
		return nil, err
	}
	defer r.finish(session, StateIdle, "")

	gateway, err := r.ndt7Gateway()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, ndt7DiscoveryTimeout)
	defer cancel()
	return gateway.discover(ctx)
}

// setNDT7Consent records the user's decision about the M-Lab disclosure.
//
// Withdrawing the consent takes effect immediately: a measurement that is
// still running would keep publishing the user's address to a third party
// after they withdrew the permission, so it is stopped here. A self hosted
// measurement is left alone - it publishes nothing to anybody.
func (r *Runner) setNDT7Consent(agreed bool) (NDT7ConsentState, error) {
	gateway, err := r.ndt7Gateway()
	if err != nil {
		return NDT7ConsentState{}, err
	}
	state, err := gateway.recordConsent(agreed)
	if err != nil {
		return NDT7ConsentState{}, err
	}
	if !agreed {
		r.cancelNDT7Run()
	}
	return state, nil
}

// cancelNDT7Run stops a running M-Lab measurement.
//
// It cancels only a task that the M-Lab provider serves. A self hosted
// measurement is never touched by an M-Lab decision.
func (r *Runner) cancelNDT7Run() {
	r.mu.Lock()
	session := r.session
	r.mu.Unlock()
	if session == nil || session.provider != string(ndt7ProviderID) {
		return
	}
	_ = r.cancelKind(session.kind)
}

// CheckNodes probes every configured node. It blocks until the probes finish
// or CancelCheck is called; the same outcome is also emitted as an event.
func (r *Runner) CheckNodes() (NodeCheckResult, error) {
	session, ctx, err := r.begin("check", "")
	if err != nil {
		return NodeCheckResult{}, err
	}
	defer r.finish(session, StateIdle, "")

	manager, path, err := r.loadNodesFn()
	if err != nil {
		return NodeCheckResult{}, err
	}
	list := manager.List()
	if len(list) == 0 {
		return NodeCheckResult{}, errors.New("no node is configured; add one with \"gospeed nodes add\"")
	}
	r.setState(StateChecking, fmt.Sprintf("checking %d node(s)", len(list)))

	checkCtx, cancel := context.WithTimeout(ctx, checkOverallTimeout)
	defer cancel()
	probes, probeErr := manager.CheckAll(checkCtx, nil, nodes.CheckOptions{
		ProbeOptions: nodes.ProbeOptions{
			Attempts:       probeAttempts,
			Timeout:        probeTimeout,
			LatencySamples: probeLatencySamples,
		},
		Parallel: probeParallel,
	})
	if probeErr != nil && !errors.Is(probeErr, context.Canceled) && !errors.Is(probeErr, context.DeadlineExceeded) {
		return NodeCheckResult{}, probeErr
	}
	if errors.Is(probeErr, context.DeadlineExceeded) && ctx.Err() == nil {
		return NodeCheckResult{}, fmt.Errorf("node check timed out after %s", checkOverallTimeout)
	}
	probes = alignProbes(probes, list)
	r.cacheProbes(probes)

	result := NodeCheckResult{
		Path:      path,
		CheckedAt: formatTime(time.Now()),
		Cancelled: ctx.Err() != nil,
		Nodes:     make([]NodeStatusView, 0, len(list)),
	}
	probesByID := make(map[string]nodes.Probe, len(probes))
	for _, probe := range probes {
		probesByID[probe.NodeID] = probe
	}
	for _, node := range list {
		result.Nodes = append(result.Nodes, probeView(node, probesByID[node.ID]))
	}
	r.emit(EventNodes, result)
	return result, nil
}

// runTest resolves the target and executes the measurement engine. Every exit
// path emits exactly one finished event and releases the session.
func (r *Runner) runTest(ctx context.Context, session *session, options StartOptions) {
	started := time.Now()
	finished := FinishedEvent{}
	defer func() {
		finished.DurationMs = milliseconds(time.Since(started))
		r.emit(EventFinished, finished)
		r.finish(session, terminalState(finished), finished.Error)
	}()

	if options.Provider == string(ndt7ProviderID) {
		r.runNDT7Test(ctx, session, options, &finished)
		return
	}

	resolved, err := r.resolveRun(ctx, options)
	if err != nil {
		setFinishedError(&finished, err)
		return
	}
	finished.Notice = resolved.notice
	r.emit(EventTarget, resolved.event)

	final, runErr := r.runProviderMeasurement(ctx, resolved.target, options, session)
	applyFinished(&finished, final, runErr)
}

// resolveRun loads the node configuration and resolves the requested
// selection. Automatic selection probes and ranks enabled nodes with measured
// data; manual selection never probes and never fails because of a health
// check. Nothing is invented when no node qualifies.
func (r *Runner) resolveRun(ctx context.Context, options StartOptions) (resolvedRun, error) {
	manager, path, err := r.loadNodesFn()
	if err != nil {
		return resolvedRun{}, err
	}
	if options.SelectionMode == SelectionModeAuto {
		return r.selectAuto(ctx, manager, path)
	}
	node, err := manager.Get(options.NodeID)
	if err != nil {
		return resolvedRun{}, fmt.Errorf("%w (available nodes: %s)", err, nodeIDList(manager.List()))
	}
	target := nodeTarget(node)
	target.SelectionMethod = string(nodes.SelectionManual)
	target.SelectionReason = "manually selected"
	if probe, ok := r.cachedProbe(node.ID); ok {
		target.HealthStatus = string(probe.Status)
		if latency, ok := probe.SelectionLatency(); ok {
			target.HealthLatency = latency
		}
	}
	notice := noticeForTarget(target)
	return resolvedRun{
		target: target,
		path:   path,
		notice: &notice,
		event: TargetEvent{
			Target:          targetView(target),
			SelectionMethod: target.SelectionMethod,
			SelectionReason: target.SelectionReason,
			ConfigPath:      path,
			Notice:          &notice,
		},
	}, nil
}

// selectAuto probes the enabled nodes and ranks them with the shared selector.
func (r *Runner) selectAuto(ctx context.Context, manager *nodes.Manager, path string) (resolvedRun, error) {
	list := enabledNodes(manager.List())
	if len(list) == 0 {
		return resolvedRun{}, errors.New("no enabled node is configured; enable one with \"gospeed nodes enable <id>\"")
	}
	r.setState(StateProbing, fmt.Sprintf("probing %d enabled node(s)", len(list)))
	probes, err := manager.CheckAll(ctx, nil, nodes.CheckOptions{
		ProbeOptions: nodes.ProbeOptions{
			Attempts:       probeAttempts,
			Timeout:        probeTimeout,
			LatencySamples: probeLatencySamples,
		},
		Parallel:    probeParallel,
		EnabledOnly: true,
	})
	if err != nil {
		return resolvedRun{}, err
	}
	r.cacheProbes(probes)
	selection, err := nodes.SelectAuto(list, probes)
	if err != nil {
		return resolvedRun{}, err
	}
	target := nodeTarget(selection.Node)
	target.SelectionMethod = string(selection.Method)
	target.SelectionReason = selection.Reason
	target.HealthStatus = string(selection.Probe.Status)
	if latency, ok := selection.Probe.SelectionLatency(); ok {
		target.HealthLatency = latency
	}
	notice := noticeForTarget(target)
	return resolvedRun{
		target: target,
		path:   path,
		notice: &notice,
		event: TargetEvent{
			Target:          targetView(target),
			SelectionMethod: target.SelectionMethod,
			SelectionReason: selection.Reason,
			SelectionNote:   selection.Note,
			Candidates:      candidateViews(selection.Candidates),
			ConfigPath:      path,
			Notice:          &notice,
		},
	}, nil
}

// CancelTest asks the running measurement to stop. It is a no-op when no test
// is running.
func (r *Runner) CancelTest() error {
	return r.cancelKind("test")
}

// CancelCheck asks a running node check to stop. It is a no-op when no check
// is running.
func (r *Runner) CancelCheck() error {
	return r.cancelKind("check")
}

// CancelAll cancels whatever is running. It is used when the window closes.
func (r *Runner) CancelAll() {
	r.mu.Lock()
	session := r.session
	r.mu.Unlock()
	if session == nil {
		return
	}
	_ = r.cancelKind(session.kind)
}

// Wait blocks until the current session has fully released its resources. It
// is used on shutdown and by tests.
func (r *Runner) Wait() {
	r.mu.Lock()
	session := r.session
	r.mu.Unlock()
	if session == nil {
		return
	}
	<-session.done
}

// Status returns a one shot snapshot for the UI.
func (r *Runner) Status() StatusResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := StatusResult{State: r.state, Message: r.message}
	if r.session != nil {
		status.Busy = true
		status.Kind = r.session.kind
	}
	return status
}

// begin registers a new session. Only one cancellable task runs at a time.
//
// providerID names the measurement backend the task serves, and is empty for
// the tasks that are not measurements.
func (r *Runner) begin(kind string, providerID string) (*session, context.Context, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session != nil {
		return nil, nil, fmt.Errorf("%w: a %s is running", ErrBusy, r.session.kind)
	}
	ctx, cancel := context.WithCancel(context.Background())
	session := &session{kind: kind, cancel: cancel, done: make(chan struct{}), provider: providerID}
	r.session = session
	return session, ctx, nil
}

// finish releases the session and publishes the terminal state. The session's
// context is always cancelled so no request can outlive the task.
func (r *Runner) finish(session *session, state, message string) {
	session.cancel()
	r.mu.Lock()
	if r.session == session {
		r.session = nil
		r.state = state
		r.message = message
	}
	r.mu.Unlock()
	close(session.done)
}

func (r *Runner) cancelKind(kind string) error {
	r.mu.Lock()
	session := r.session
	if session == nil || session.kind != kind {
		r.mu.Unlock()
		return nil
	}
	cancel := session.cancel
	r.state = StateCancelling
	message := kind + " cancellation requested"
	r.message = message
	r.mu.Unlock()
	r.emit(EventState, StateEvent{State: StateCancelling, Message: message})
	cancel()
	return nil
}

func (r *Runner) setState(state, message string) {
	r.mu.Lock()
	r.state = state
	r.message = message
	r.mu.Unlock()
	r.emit(EventState, StateEvent{State: state, Message: message})
}

// recordState keeps Status current without emitting an extra event: every
// running state already reaches the UI inside the progress events.
func (r *Runner) recordState(state string) {
	r.mu.Lock()
	r.state = state
	r.message = ""
	r.mu.Unlock()
}

// cacheProbes stores the last measured probes so a later manual selection can
// report the node's last known health without pretending it was just probed.
func (r *Runner) cacheProbes(probes []nodes.Probe) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, probe := range probes {
		if probe.NodeID == "" {
			continue
		}
		r.probes[probe.NodeID] = probe
	}
}

func (r *Runner) cachedProbe(nodeID string) (nodes.Probe, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	probe, ok := r.probes[nodeID]
	return probe, ok
}

// resolvedRun is a target plus the event describing how it was chosen.
type resolvedRun struct {
	target speedtest.Target
	path   string
	event  TargetEvent
	notice *MeasurementNotice
}

// normalized validates a request and fills in the engine defaults.
//
// The validation is provider specific. A request for the M-Lab NDT7 provider
// carries a node reference and a direction instead of the engine's connection
// count and duration, because NDT7 measures one direction over a single
// WebSocket and applies its own traffic budget and timeout.
func (o StartOptions) normalized() (StartOptions, error) {
	o.Provider = strings.TrimSpace(o.Provider)
	switch o.Provider {
	case "":
		o.Provider = string(defaultProviderID)
	case string(ndt7ProviderID):
		return o.normalizedNDT7()
	default:
		return o, fmt.Errorf("unknown provider %q; this build offers %q and %q",
			o.Provider, defaultProviderID, ndt7ProviderID)
	}
	o.SelectionMode = strings.ToLower(strings.TrimSpace(o.SelectionMode))
	if o.SelectionMode == "" {
		o.SelectionMode = SelectionModeAuto
	}
	switch o.SelectionMode {
	case SelectionModeAuto:
		o.NodeID = ""
	case SelectionModeManual:
		o.NodeID = strings.TrimSpace(o.NodeID)
		if o.NodeID == "" {
			return o, errors.New("manual selection requires a node id")
		}
	default:
		return o, fmt.Errorf("unknown selection mode %q; expected %q or %q",
			o.SelectionMode, SelectionModeAuto, SelectionModeManual)
	}
	if o.Connections == 0 {
		o.Connections = speedtest.DefaultConnections
	}
	if o.Connections < 1 || o.Connections > speedtest.MaxConnections {
		return o, fmt.Errorf("connections must be between 1 and %d, got %d",
			speedtest.MaxConnections, o.Connections)
	}
	if o.DurationMs == 0 {
		o.DurationMs = defaultDurationMs()
	}
	if o.DurationMs < minDurationMs || o.DurationMs > maxDurationMs {
		return o, fmt.Errorf("duration must be between %d ms and %d ms, got %d ms",
			minDurationMs, maxDurationMs, o.DurationMs)
	}
	if o.SampleIntervalMs == 0 {
		o.SampleIntervalMs = defaultSampleIntervalMs()
	}
	if o.SampleIntervalMs < minSampleIntervalMs || o.SampleIntervalMs > maxSampleIntervalMs {
		return o, fmt.Errorf("sample interval must be between %d ms and %d ms, got %d ms",
			minSampleIntervalMs, maxSampleIntervalMs, o.SampleIntervalMs)
	}
	return o, nil
}

// normalizedNDT7 validates a request for the M-Lab NDT7 provider.
//
// The engine's knobs do not exist in this protocol: NDT7 measures one
// direction over a single WebSocket and applies its own traffic budget and
// overall timeout. The request therefore states the node reference and the
// direction, and the engine's fields are cleared rather than silently
// applied to a protocol that does not have them.
func (o StartOptions) normalizedNDT7() (StartOptions, error) {
	o.SelectionMode = SelectionModeManual
	o.NodeID = ""
	o.Connections = 1
	o.DurationMs = 0
	o.SampleIntervalMs = 0
	o.ProviderNodeRef = strings.TrimSpace(o.ProviderNodeRef)
	if o.ProviderNodeRef == "" {
		return o, errors.New("an M-Lab measurement requires the reference of a discovered node")
	}
	if _, err := parseNDT7Direction(o.Direction); err != nil {
		return o, err
	}
	return o, nil
}

// alignProbes keeps one probe per configured node, in configuration order. A
// node without a probe stays "unknown" instead of receiving a guessed status.
func alignProbes(probes []nodes.Probe, list []nodes.Node) []nodes.Probe {
	byID := make(map[string]nodes.Probe, len(probes))
	for _, probe := range probes {
		byID[probe.NodeID] = probe
	}
	aligned := make([]nodes.Probe, 0, len(list))
	for _, node := range list {
		if probe, ok := byID[node.ID]; ok {
			aligned = append(aligned, probe)
			continue
		}
		aligned = append(aligned, nodes.Probe{NodeID: node.ID, Status: nodes.HealthUnknown})
	}
	return aligned
}

// setFinishedError records the exact engine error and the terminal status.
func setFinishedError(finished *FinishedEvent, err error) {
	finished.Error = err.Error()
	switch {
	case errors.Is(err, context.Canceled):
		finished.Status = StateCancelled
		finished.Cancelled = true
	case errors.Is(err, context.DeadlineExceeded):
		finished.Status = StateFailed
	default:
		finished.Status = StateFailed
	}
}

func terminalState(finished FinishedEvent) string {
	switch {
	case finished.Cancelled:
		return StateCancelled
	case finished.Status != "":
		return finished.Status
	default:
		return StateFailed
	}
}

func milliseconds(value time.Duration) float64 {
	return float64(value) / float64(time.Millisecond)
}
