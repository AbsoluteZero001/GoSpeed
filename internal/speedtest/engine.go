// Package speedtest contains the measurement engine.
//
// The engine executes network measurements, collects raw counters and returns
// a structured result. It never writes to stdout and never depends on the CLI,
// the test server or any future GUI, so the same engine can back a Wails
// desktop client or a web frontend later.
package speedtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/version"
)

// PhaseMask selects which phases a run executes.
type PhaseMask uint8

const (
	// PhasesLatency runs the HTTP RTT phase.
	PhasesLatency PhaseMask = 1 << iota
	// PhasesDownload runs the download phase.
	PhasesDownload
	// PhasesUpload runs the upload phase.
	PhasesUpload

	// PhasesAll runs every phase implemented in v0.1.0.
	PhasesAll = PhasesLatency | PhasesDownload | PhasesUpload
)

// ProgressStage tells a progress observer what just happened.
type ProgressStage string

const (
	StageStart    ProgressStage = "start"
	StageProgress ProgressStage = "progress"
	StageDone     ProgressStage = "done"
)

// Progress is one engine event. Bytes, Mbps and InstantMbps carry the raw
// counters observed so far; they are never synthesized.
type Progress struct {
	Phase       Phase
	Stage       ProgressStage
	Elapsed     time.Duration
	Bytes       int64
	Mbps        float64
	InstantMbps float64
	Message     string
}

// Defaults used when Options leaves a field at its zero value.
const (
	DefaultDuration        = 10 * time.Second
	DefaultTimeout         = 30 * time.Second
	DefaultConnections     = 1
	DefaultLatencySamples  = 5
	DefaultLatencyInterval = 100 * time.Millisecond
)

// Options configures a test run.
type Options struct {
	// Target identifies the server under test.
	Target Target
	// Phases selects the measurements to run. Zero means PhasesAll.
	Phases PhaseMask
	// Duration is the intended transfer window of the download and upload
	// phases. Zero means DefaultDuration.
	Duration time.Duration
	// MaxBytes stops each transfer after this many bytes. Zero means the
	// duration alone decides when a transfer ends. Duration still acts as an
	// upper bound so a slow link cannot hang forever.
	MaxBytes int64
	// Timeout bounds every phase. Zero means DefaultTimeout. It must be longer
	// than Duration.
	Timeout time.Duration
	// Connections is the number of parallel connections per transfer phase.
	// v0.1.0 supports exactly one.
	Connections int
	// LatencySamples is the number of HTTP RTT samples to collect.
	LatencySamples int
	// LatencyInterval is the pause between latency samples.
	LatencyInterval time.Duration
	// Warmup sends one unmeasured /health request first so that DNS resolution
	// and connection setup do not distort the first measured samples.
	Warmup bool
	// Progress receives progress events. It is called from the goroutine that
	// runs the test and must return quickly.
	Progress func(Progress)
}

func (o Options) normalized() (Options, error) {
	if o.Phases == 0 {
		o.Phases = PhasesAll
	}
	if o.Phases&^PhasesAll != 0 {
		return o, fmt.Errorf("%w: unknown phase mask %d", ErrInvalidOptions, o.Phases)
	}
	target, err := o.Target.normalized()
	if err != nil {
		return o, err
	}
	o.Target = target
	if o.Duration < 0 {
		return o, fmt.Errorf("%w: duration must not be negative", ErrInvalidOptions)
	}
	if o.Duration == 0 {
		o.Duration = DefaultDuration
	}
	if o.Timeout < 0 {
		return o, fmt.Errorf("%w: timeout must not be negative", ErrInvalidOptions)
	}
	if o.Timeout == 0 {
		o.Timeout = DefaultTimeout
	}
	if o.Connections < 0 {
		return o, fmt.Errorf("%w: connections must not be negative", ErrInvalidOptions)
	}
	if o.Connections == 0 {
		o.Connections = DefaultConnections
	}
	if o.Connections != DefaultConnections {
		return o, fmt.Errorf("%w: requested %d connections", ErrUnsupportedConnections, o.Connections)
	}
	if o.LatencySamples < 0 {
		return o, fmt.Errorf("%w: latency samples must not be negative", ErrInvalidOptions)
	}
	if o.LatencySamples == 0 {
		o.LatencySamples = DefaultLatencySamples
	}
	if o.LatencyInterval < 0 {
		return o, fmt.Errorf("%w: latency interval must not be negative", ErrInvalidOptions)
	}
	if o.LatencyInterval == 0 {
		o.LatencyInterval = DefaultLatencyInterval
	}
	if o.MaxBytes < 0 {
		return o, fmt.Errorf("%w: max bytes must not be negative", ErrInvalidOptions)
	}
	if o.Phases&(PhasesDownload|PhasesUpload) != 0 && o.Timeout <= o.Duration {
		return o, fmt.Errorf("%w: timeout %s must be longer than duration %s so the transfer can be confirmed",
			ErrInvalidOptions, o.Timeout, o.Duration)
	}
	return o, nil
}

// phaseList expands a mask into the execution order used by Result.Settings.
func (m PhaseMask) phaseList() []Phase {
	phases := make([]Phase, 0, 3)
	if m&PhasesLatency != 0 {
		phases = append(phases, PhaseLatency)
	}
	if m&PhasesDownload != 0 {
		phases = append(phases, PhaseDownload)
	}
	if m&PhasesUpload != 0 {
		phases = append(phases, PhaseUpload)
	}
	return phases
}

// Engine runs measurements against one target at a time. It is safe to reuse
// an Engine for sequential runs; concurrent runs share the underlying
// http.Client, which is itself safe for concurrent use.
type Engine struct {
	client *http.Client
}

// NewEngine returns an engine that uses client. A nil client installs the
// default GoSpeed client, which keeps connections alive and disables
// transparent response compression so rates are not inflated by gzip.
func NewEngine(client *http.Client) *Engine {
	if client == nil {
		client = NewHTTPClient()
	}
	return &Engine{client: client}
}

// NewHTTPClient builds the client used by the CLI. DisableCompression is
// essential for an honest measurement: transparent gzip would report
// compressed bytes travelled over the wire while the user expects payload
// throughput.
func NewHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
	}
	return &http.Client{Transport: transport}
}

// Run executes the configured phases in order and returns a result even when a
// phase fails, so callers can persist and display partial evidence.
func (e *Engine) Run(ctx context.Context, options Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	opts, err := options.normalized()
	if err != nil {
		return Result{}, err
	}
	testID, err := NewTestID()
	if err != nil {
		return Result{}, err
	}
	result := Result{
		TestID:    testID,
		Timestamp: time.Now().UTC(),
		Status:    StatusRunning,
		Target: TargetInfo{
			ServerID:      opts.Target.ID,
			ServerName:    opts.Target.Name,
			ServerAddress: opts.Target.BaseURL,
			Protocol:      opts.Target.Protocol,
			Local:         opts.Target.Local,
		},
		Settings: SettingsSnapshot{
			Phases:          opts.Phases.phaseList(),
			DurationNs:      opts.Duration,
			TimeoutNs:       opts.Timeout,
			MaxBytes:        opts.MaxBytes,
			Connections:     opts.Connections,
			LatencySamples:  opts.LatencySamples,
			LatencyInterval: opts.LatencyInterval,
			Warmup:          opts.Warmup,
		},
	}

	if opts.Warmup {
		if err := e.warmup(ctx, opts); err != nil {
			return fail(result, fmt.Errorf("warmup: %w", err))
		}
	}
	if opts.Phases&PhasesLatency != 0 {
		latency, err := e.measureLatency(ctx, opts)
		if latency != nil {
			result.Latency = latency
		}
		if err != nil {
			return fail(result, err)
		}
	}
	if opts.Phases&PhasesDownload != 0 {
		download, err := e.measureDownload(ctx, opts)
		if download != nil {
			result.Download = download
		}
		if err != nil {
			return fail(result, err)
		}
	}
	if opts.Phases&PhasesUpload != 0 {
		upload, err := e.measureUpload(ctx, opts)
		if upload != nil {
			result.Upload = upload
		}
		if err != nil {
			return fail(result, err)
		}
	}
	result.Status = StatusCompleted
	return result, nil
}

// fail marks a result as failed or cancelled and keeps the engine error.
func fail(result Result, err error) (Result, error) {
	if errors.Is(err, context.Canceled) {
		result.Status = StatusCancelled
	} else {
		result.Status = StatusFailed
	}
	result.ErrorMessage = err.Error()
	return result, err
}

// warmup performs one unmeasured request. Besides removing connection setup
// from the first latency sample it fails fast when the server is unreachable.
func (e *Engine) warmup(ctx context.Context, opts Options) error {
	warmupCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(warmupCtx, http.MethodGet, opts.Target.BaseURL+"/health", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := e.client.Do(req)
	if err != nil {
		// The caller adds the "warmup" prefix, so this must not masquerade as a
		// latency sample failure.
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case warmupCtx.Err() == context.DeadlineExceeded:
			return fmt.Errorf("timed out after %s: %w", opts.Timeout, context.DeadlineExceeded)
		default:
			return err
		}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: GET /health: %s", ErrUnexpectedStatus, resp.Status)
	}
	return nil
}

// phaseError turns transport level failures into stable, phase labelled errors.
func phaseError(phase Phase, parentCtx, phaseCtx context.Context, timeout time.Duration, err error) error {
	switch {
	case parentCtx.Err() == context.Canceled:
		return fmt.Errorf("%s: %w", phase, context.Canceled)
	case parentCtx.Err() == context.DeadlineExceeded:
		return fmt.Errorf("%s: %w", phase, context.DeadlineExceeded)
	case phaseCtx.Err() == context.DeadlineExceeded:
		return fmt.Errorf("%s: timed out after %s: %w", phase, timeout, context.DeadlineExceeded)
	case err != nil:
		return fmt.Errorf("%s: %w", phase, err)
	default:
		return nil
	}
}

func emit(opts Options, event Progress) {
	if opts.Progress != nil {
		opts.Progress(event)
	}
}
