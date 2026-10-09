package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
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

// measurementEngine is the subset of speedtest.Engine the GUI uses. Keeping it
// an interface makes the runner testable without weakening the engine.
type measurementEngine interface {
	Run(ctx context.Context, options speedtest.Options) (speedtest.Result, error)
}

type session struct {
	kind   string
	cancel context.CancelFunc
	done   chan struct{}
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
	// same engine the CLI uses.
	loadNodesFn func() (*nodes.Manager, string, error)
	newEngineFn func() (measurementEngine, func())
}

// NewRunner returns a runner that reports through emit.
func NewRunner(emit EmitFunc) *Runner {
	if emit == nil {
		emit = func(string, any) {}
	}
	return &Runner{
		emit:        emit,
		state:       StateIdle,
		probes:      make(map[string]nodes.Probe),
		loadNodesFn: loadNodeManager,
		newEngineFn: newSpeedtestEngine,
	}
}

// newSpeedtestEngine builds the same HTTP client the CLI uses and returns a
// cleanup function that closes idle connections when the run is over.
func newSpeedtestEngine() (measurementEngine, func()) {
	client := speedtest.NewHTTPClient()
	return speedtest.NewEngine(client), client.CloseIdleConnections
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
	session, ctx, err := r.begin("test")
	if err != nil {
		return StartTestResult{}, err
	}
	r.setState(StatePreparing, "test requested")
	go r.runTest(ctx, session, normalized)
	return StartTestResult{Started: true, Mode: normalized.SelectionMode}, nil
}

// CheckNodes probes every configured node. It blocks until the probes finish
// or CancelCheck is called; the same outcome is also emitted as an event.
func (r *Runner) CheckNodes() (NodeCheckResult, error) {
	session, ctx, err := r.begin("check")
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

	resolved, err := r.resolveRun(ctx, options)
	if err != nil {
		setFinishedError(&finished, err)
		return
	}
	r.emit(EventTarget, resolved.event)

	engine, cleanup := r.newEngineFn()
	defer cleanup()
	result, runErr := engine.Run(ctx, engineOptions(resolved.target, options, func(progress speedtest.Progress) {
		r.recordState(string(progress.State))
		r.emit(EventProgress, progressEvent(progress))
	}))
	if result.TestID != "" {
		finished.Result = &result
		finished.TestID = result.TestID
	}
	if runErr != nil {
		setFinishedError(&finished, runErr)
		return
	}
	finished.Status = StateCompleted
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
	return resolvedRun{
		target: target,
		path:   path,
		event: TargetEvent{
			Target:          targetView(target),
			SelectionMethod: target.SelectionMethod,
			SelectionReason: target.SelectionReason,
			ConfigPath:      path,
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
	return resolvedRun{
		target: target,
		path:   path,
		event: TargetEvent{
			Target:          targetView(target),
			SelectionMethod: target.SelectionMethod,
			SelectionReason: selection.Reason,
			SelectionNote:   selection.Note,
			Candidates:      candidateViews(selection.Candidates),
			ConfigPath:      path,
		},
	}, nil
}

// engineOptions builds the measurement request. Warmup, capability
// negotiation and latency sampling keep their engine defaults, so a GUI run
// and a CLI run with the same duration, connections and sample interval
// execute the same measurement.
func engineOptions(target speedtest.Target, options StartOptions, progress func(speedtest.Progress)) speedtest.Options {
	duration := time.Duration(options.DurationMs) * time.Millisecond
	timeout := duration + timeoutSlack
	if timeout < speedtest.DefaultTimeout {
		timeout = speedtest.DefaultTimeout
	}
	return speedtest.Options{
		Target:                target,
		Phases:                speedtest.PhasesAll,
		Duration:              duration,
		MaxBytes:              0,
		Timeout:               timeout,
		Connections:           options.Connections,
		LatencySamples:        speedtest.DefaultLatencySamples,
		LatencyInterval:       speedtest.DefaultLatencyInterval,
		Warmup:                true,
		NegotiateCapabilities: true,
		SampleInterval:        time.Duration(options.SampleIntervalMs) * time.Millisecond,
		Progress:              progress,
	}
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
func (r *Runner) begin(kind string) (*session, context.Context, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.session != nil {
		return nil, nil, fmt.Errorf("%w: a %s is running", ErrBusy, r.session.kind)
	}
	ctx, cancel := context.WithCancel(context.Background())
	session := &session{kind: kind, cancel: cancel, done: make(chan struct{})}
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
}

// normalized validates a request and fills in the engine defaults.
func (o StartOptions) normalized() (StartOptions, error) {
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

func progressEvent(progress speedtest.Progress) ProgressEvent {
	event := ProgressEvent{
		State:             string(progress.State),
		Phase:             string(progress.Phase),
		Stage:             string(progress.Stage),
		ElapsedMs:         milliseconds(progress.Elapsed),
		Bytes:             progress.Bytes,
		Mbps:              progress.Mbps,
		InstantMbps:       progress.InstantMbps,
		ActiveConnections: progress.ActiveConnections,
		Samples:           progress.Samples,
		Message:           progress.Message,
	}
	if progress.Budget.Fraction != nil {
		fraction := *progress.Budget.Fraction
		event.BudgetFraction = &fraction
	}
	if progress.Budget.Remaining != nil {
		remaining := milliseconds(*progress.Budget.Remaining)
		event.BudgetRemainingMs = &remaining
	}
	return event
}

func milliseconds(value time.Duration) float64 {
	return float64(value) / float64(time.Millisecond)
}
