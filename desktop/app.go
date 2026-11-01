package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/version"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ndt7DiscoveryTimeout caps one M-Lab Locate query.
//
// The provider applies its own request timeout and honours the schedule the
// server publishes; this cap is the outer bound so a GUI call can never hang
// indefinitely.
const ndt7DiscoveryTimeout = 30 * time.Second

// App is the Wails binding surface. Every method is a thin wrapper around the
// Runner, which owns the measurement logic. The Wails runtime is only used for
// event delivery; no measurement code depends on it.
type App struct {
	mu     sync.Mutex
	ctx    context.Context
	runner *Runner
}

// NewApp builds the desktop application. Nothing is measured or probed here:
// the engine only runs when the user starts it from the UI.
func NewApp() *App {
	app := &App{}
	app.runner = NewRunner(app.emit)
	return app
}

// startup stores the Wails context. It deliberately starts no test and no
// listening server.
func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ctx = ctx
}

// shutdown cancels anything still running, waits for the measurement to return
// so closing the window cannot leave transfers behind, and then releases the
// provider resources the runner created.
//
// The order matters: the registry is closed only after every task has finished,
// because closing it returns the pooled connections a run is still using.
func (a *App) shutdown(context.Context) {
	a.runner.CancelAll()
	a.runner.Wait()
	// The M-Lab session ends with the window: the pending authorization is
	// dropped and the short lived access token the last Locate answer
	// carried is discarded. The daily counter is not touched - it lives in a
	// file and survives a restart, a reload and a crash.
	if gateway, err := a.runner.ndt7Gateway(); err == nil {
		gateway.endSession()
	}
	_ = a.runner.CloseProviders()
}

// emit forwards one event to the frontend. Events are the only channel the GUI
// uses for measurement state; the frontend never polls.
func (a *App) emit(name string, payload any) {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx == nil {
		return
	}
	runtime.EventsEmit(ctx, name, payload)
}

// GetAppInfo returns build information, engine limits, the effective node
// configuration location and the test defaults the UI should prefill.
func (a *App) GetAppInfo() (AppInfo, error) {
	manager, path, err := a.runner.loadNodes()
	if err != nil {
		return AppInfo{}, err
	}
	return AppInfo{
		Version:                 version.Version,
		GoVersion:               runtimeVersion(),
		Platform:                platformName(),
		MaxConnections:          maxConnections(),
		DefaultConnections:      defaultConnections(),
		DefaultDurationMs:       defaultDurationMs(),
		DefaultSampleIntervalMs: defaultSampleIntervalMs(),
		MinDurationMs:           minDurationMs,
		MaxDurationMs:           maxDurationMs,
		MinSampleIntervalMs:     minSampleIntervalMs,
		MaxSampleIntervalMs:     maxSampleIntervalMs,
		LatencySamples:          defaultLatencySamples(),
		NodeConfigPath:          path,
		NodeConfigSource:        configSource(path),
		ConfigSearchPaths:       configSearchPaths(),
		NodeCount:               len(manager.List()),
	}, nil
}

// ListNodes returns the configured nodes exactly as they are stored. Nothing
// is probed here: a health check is an explicit, cancellable action.
func (a *App) ListNodes() (NodeListResult, error) {
	manager, path, err := a.runner.loadNodes()
	if err != nil {
		return NodeListResult{}, err
	}
	return NodeListResult{
		Path:   path,
		Source: configSource(path),
		Nodes:  nodeViews(manager.List()),
		Notice: nodeSetNotice(manager.List()),
	}, nil
}

// CheckNodes probes every configured node with the same staged probe the CLI
// uses. It blocks until the probes finish or CancelNodeCheck is called, and it
// emits the result through the nodes event as well.
func (a *App) CheckNodes() (NodeCheckResult, error) {
	return a.runner.CheckNodes()
}

// CancelNodeCheck cancels a running node check. It is a no-op when no check is
// running.
func (a *App) CancelNodeCheck() error {
	return a.runner.CancelCheck()
}

// StartTest validates the request and returns immediately. Progress, the
// selected target and the final result are pushed through events, so the
// frontend never polls for status.
func (a *App) StartTest(options StartOptions) (StartTestResult, error) {
	return a.runner.Start(options)
}

// CancelTest cancels the running measurement. The engine returns a cancelled
// result with the real counters observed so far; the finished event carries
// it. It is a no-op when no test is running.
func (a *App) CancelTest() error {
	return a.runner.CancelTest()
}

// GetStatus reports the current runner state. It is called once when the UI
// mounts to resynchronise after a hot reload; it is not a polling endpoint.
func (a *App) GetStatus() StatusResult {
	return a.runner.Status()
}

// GetNDT7State returns the state of the M-Lab gates: the consent decision,
// the full disclosure text, today's counter and the provider's traffic
// budget.
//
// It performs no network request, so it is safe to call whenever the GUI
// needs to redraw the M-Lab panel.
func (a *App) GetNDT7State() (NDT7ConsentState, error) {
	gateway, err := a.runner.ndt7Gateway()
	if err != nil {
		return NDT7ConsentState{}, err
	}
	return gateway.state(), nil
}

// SetNDT7Consent records the user's decision about the M-Lab disclosure.
//
// Agreeing enables node discovery and measurements. Refusing withdraws
// everything the agreement had enabled: the pending run authorizations are
// dropped and the discovered nodes, and with them the short lived access
// token the Locate answer carried, are discarded.
func (a *App) SetNDT7Consent(agreed bool) (NDT7ConsentState, error) {
	return a.runner.setNDT7Consent(agreed)
}

// DiscoverNDT7Nodes asks the M-Lab Locate service for the NDT7 servers near
// the client.
//
// The query itself reveals the client public IP to M-Lab, so it requires a
// current consent and is refused without one, before any request is sent.
// The returned nodes carry no endpoint and no access token: the GUI names a
// node by an opaque reference and the backend resolves that reference to the
// real service URL.
func (a *App) DiscoverNDT7Nodes() (NDT7NodeListResult, error) {
	views, err := a.runner.discoverNDT7Nodes(context.Background())
	if err != nil {
		return NDT7NodeListResult{}, err
	}
	gateway, err := a.runner.ndt7Gateway()
	if err != nil {
		return NDT7NodeListResult{}, err
	}
	return NDT7NodeListResult{Nodes: views, Consent: gateway.state()}, nil
}

// AuthorizeNDT7Run authorizes exactly one M-Lab measurement.
//
// The caller must only call this after the user confirmed THIS run: the GUI
// shows the per-run disclosure first. The grant is bound to one node and one
// direction, the provider consumes it on the first matching run, and it is
// never remembered.
//
// The node is named by the opaque reference the backend minted. There is no
// parameter for an endpoint, so a caller cannot point the measurement at an
// address of its own choosing.
func (a *App) AuthorizeNDT7Run(ref string, direction string) (NDT7RunAuthorization, error) {
	gateway, err := a.runner.ndt7Gateway()
	if err != nil {
		return NDT7RunAuthorization{}, err
	}
	parsed, err := parseNDT7Direction(direction)
	if err != nil {
		return NDT7RunAuthorization{}, err
	}
	return gateway.authorizeRun(strings.TrimSpace(ref), parsed)
}

// RevokeNDT7Authorization drops every pending M-Lab run authorization.
//
// It is what the GUI calls when the user dismisses the per-run confirmation,
// so a dismissed confirmation cannot authorize a later run.
func (a *App) RevokeNDT7Authorization() error {
	gateway, err := a.runner.ndt7Gateway()
	if err != nil {
		return err
	}
	gateway.revokeAuthorizations()
	return nil
}
