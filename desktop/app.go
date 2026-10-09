package main

import (
	"context"
	"sync"

	"github.com/AbsoluteZero001/GoSpeed/internal/version"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

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

// shutdown cancels anything still running and waits for the engine to return,
// so closing the window cannot leave measurements behind.
func (a *App) shutdown(context.Context) {
	a.runner.CancelAll()
	a.runner.Wait()
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
