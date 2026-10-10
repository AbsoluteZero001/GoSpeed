package speedtest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/version"
)

const (
	// transferBufferSize is reused by every read so a long test does not grow
	// the heap: the payload never lives in memory as a whole.
	transferBufferSize = 64 << 10
	// errorBodySnippet bounds how much of an error response is read.
	errorBodySnippet = 4 << 10
)

// measureDownload performs a real HTTP download with one or more parallel
// connections.
//
// All workers share one measurement window. The window opens when the first
// connection sees its first response byte and closes when the aggregate
// transfer stops (all bytes requested were transferred, or the duration
// budget expired). The rate is therefore:
//
//	(total bytes received by every connection) * 8 / shared window / 1e6
//
// Per connection durations are reported for diagnostics but are never summed,
// because concurrent windows overlap.
func (e *Engine) measureDownload(ctx context.Context, opts Options) (*TransferResult, error) {
	phaseCtx, cancelPhase := context.WithTimeout(ctx, opts.Timeout)
	defer cancelPhase()

	transferCtx := phaseCtx
	var transferDeadline time.Time
	if opts.Duration > 0 {
		transferDeadline = time.Now().Add(opts.Duration)
		var cancelTransfer context.CancelFunc
		transferCtx, cancelTransfer = context.WithDeadline(phaseCtx, transferDeadline)
		defer cancelTransfer()
	}

	endpoint, err := downloadEndpoint(opts)
	if err != nil {
		return nil, err
	}

	counters := &transferCounters{}
	var budget *sharedBudget
	if opts.MaxBytes > 0 {
		budget = newSharedBudget(opts.MaxBytes)
	}
	spec := budgetSpec{duration: opts.Duration, maxBytes: opts.MaxBytes}

	emit(opts, Progress{
		State:          StateDownloadTesting,
		Phase:          PhaseDownload,
		Stage:          StageStart,
		PhaseStartedAt: time.Now().UTC(),
		Message:        fmt.Sprintf("%d connections", opts.Connections),
	})
	sampler := newSampler(opts, PhaseDownload, counters, spec)
	sampler.start()
	results := runWorkers(opts.Connections, func(index int) workerResult {
		result := e.downloadWorker(transferCtx, ctx, opts, endpoint, transferDeadline, budget, counters)
		result.index = index
		return result
	})
	samples := sampler.stop()

	// A shared byte budget defines phase completion by the aggregate, not by a
	// single connection: the final read of one connection may return data and
	// io.EOF together, so it never reaches the "budget drained" check inside the
	// reader. If the aggregate delivered exactly the requested budget, those
	// natural EOFs are normal completion; otherwise they are truncations.
	budgetMet := opts.MaxBytes > 0 && counters.totalBytes() == opts.MaxBytes
	if !budgetMet {
		for index := range results {
			if results[index].err == nil && results[index].earlyEOF {
				results[index].err = fmt.Errorf("%w: server closed the stream after %d bytes",
					ErrIncompleteTransfer, results[index].bytes)
			}
		}
	}
	aggregate := aggregateWorkers(results)

	// Cancellation and the phase timeout win over transport errors, because
	// the workers only see a generic "context deadline exceeded".
	if ctx.Err() != nil {
		return nil, phaseError(PhaseDownload, ctx, transferCtx, opts.Timeout, firstWorkerError(results))
	}
	if phaseCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("download: timed out after %s: %w", opts.Timeout, context.DeadlineExceeded)
	}
	if aggregate.active == 0 {
		// Keep the underlying cause matchable with errors.Is while marking the
		// phase as an all-connections failure.
		if cause := firstWorkerError(results); cause != nil {
			return nil, fmt.Errorf("%w: all %d connections failed: %w",
				ErrAllConnectionsFailed, opts.Connections, cause)
		}
		return nil, fmt.Errorf("%w: all %d connections failed: %s",
			ErrAllConnectionsFailed, opts.Connections, firstError(aggregate.errors))
	}
	if aggregate.clientBytes == 0 {
		return nil, ErrNoDataTransferred
	}

	stopReason := StopReasonServerEOF
	windowEnd := aggregate.lastDataAt
	// The window elapsed when the context confirms it, or when any worker
	// observed its own end of stream at or after the deadline. The worker
	// evidence is a monotonic comparison captured at the event point, so the
	// phase stays correct even when the context timer itself is scheduled
	// late under load.
	windowElapsed := transferCtx.Err() == context.DeadlineExceeded
	if !windowElapsed && opts.Duration > 0 {
		for index := range results {
			if results[index].endedByWindow {
				windowElapsed = true
				break
			}
		}
	}
	switch {
	case opts.MaxBytes > 0 && aggregate.clientBytes == opts.MaxBytes:
		stopReason = StopReasonRequestedBytes
	case windowElapsed:
		stopReason = StopReasonDuration
		// A duration limited window ends at the intended deadline, so a stall
		// inside the window lowers the measured rate instead of hiding.
		windowEnd = transferDeadline
	}
	if opts.MaxBytes > 0 && aggregate.clientBytes != opts.MaxBytes {
		return nil, fmt.Errorf("%w: received %d of %d bytes across %d connections (%d active, %d failed)",
			ErrIncompleteTransfer, aggregate.clientBytes, opts.MaxBytes,
			opts.Connections, aggregate.active, aggregate.failed)
	}
	if stopReason == StopReasonServerEOF {
		return nil, fmt.Errorf("%w: every connection stopped before the %s window ended (%d bytes, %d active connections)",
			ErrIncompleteTransfer, opts.Duration, aggregate.clientBytes, aggregate.active)
	}
	elapsed, err := windowDuration(aggregate.windowStart, windowEnd)
	if err != nil {
		// The transfer was faster than the platform clock can resolve. Report an
		// invalid measurement instead of inventing a duration or a rate.
		return nil, fmt.Errorf(
			"download: the transfer finished within one clock tick, so the measurement window is not resolvable: %w (increase --max-bytes or --duration, or use a slower target)",
			err)
	}
	rate, err := Mbps(aggregate.clientBytes, elapsed)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	megabytesPerSecond, err := MBps(aggregate.clientBytes, elapsed)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	emit(opts, Progress{
		State:             StateDownloadTesting,
		Phase:             PhaseDownload,
		Stage:             StageDone,
		Elapsed:           elapsed,
		Bytes:             aggregate.clientBytes,
		Mbps:              rate,
		ActiveConnections: aggregate.active,
	})
	return &TransferResult{
		Bytes:             aggregate.clientBytes,
		DurationNs:        elapsed,
		Mbps:              rate,
		MBPerSecond:       megabytesPerSecond,
		Connections:       opts.Connections,
		ActiveConnections: aggregate.active,
		FailedConnections: aggregate.failed,
		MeasurementWindow: downloadWindow(opts.Connections),
		StopReason:        stopReason,
		Samples:           samples,
		Statistics:        Summarize(samples),
		ConnectionReports: buildConnectionReports(results, false),
	}, nil
}

// downloadWorker transfers data over one connection. It never calls user code:
// byte counters are atomic and the sampler reads them from its own goroutine.
// deadline is the client window's monotonic deadline; it is zero when the run
// has no duration window.
func (e *Engine) downloadWorker(transferCtx, parentCtx context.Context, opts Options, endpoint string, deadline time.Time, budget *sharedBudget, counters *transferCounters) workerResult {
	var result workerResult
	request, err := http.NewRequestWithContext(transferCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		result.err = fmt.Errorf("build request: %w", err)
		result.finishedAt = time.Now()
		return result
	}
	request.Header.Set("User-Agent", version.UserAgent())
	// Cache and compression would both make the rate describe something other
	// than the payload the user asked to download.
	request.Header.Set("Cache-Control", "no-store")
	request.Header.Set("Pragma", "no-cache")
	request.Header.Set("Accept-Encoding", "identity")

	var firstByteAt atomic.Pointer[time.Time]
	trace := &httptrace.ClientTrace{
		GotFirstResponseByte: func() {
			now := time.Now()
			firstByteAt.CompareAndSwap(nil, &now)
		},
	}
	request = request.WithContext(httptrace.WithClientTrace(transferCtx, trace))

	response, err := e.client.Do(request)
	if err != nil {
		result.finishedAt = time.Now()
		result.err = err
		return result
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, errorBodySnippet))
		result.finishedAt = time.Now()
		result.err = fmt.Errorf("%w: GET /download: %s", ErrUnexpectedStatus, response.Status)
		return result
	}

	if pointer := firstByteAt.Load(); pointer != nil {
		// Keeping the full time.Time preserves the monotonic clock reading of
		// the httptrace callback.
		result.firstAt = *pointer
	} else {
		result.firstAt = time.Now()
	}
	counters.markStarted(result.firstAt)
	counters.beginConnection()
	defer counters.endConnection()

	source := io.Reader(response.Body)
	var budgetSource *budgetReader
	if budget != nil {
		budgetSource = &budgetReader{reader: response.Body, budget: budget}
		source = budgetSource
	}
	sink := &countingSink{onWrite: func(bytes int64) {
		result.bytes += bytes
		counters.addBytes(bytes)
		result.lastAt = time.Now()
	}}
	_, copyErr := io.CopyBuffer(sink, source, make([]byte, transferBufferSize))
	result.finishedAt = time.Now()
	if result.lastAt.IsZero() {
		result.lastAt = result.firstAt
	}

	// Classify why the read ended, at the moment it actually ended. The clean
	// EOF case must not consult the context here or later in the phase: the
	// deadline is applied by the runtime timer goroutine, which can itself be
	// scheduled late under load, so a delayed Err() observation must never
	// relabel an early close as a normal stop (the historic CI failure), and
	// a summary-stage check would run after the window in every non-trivial
	// case and relabel a real truncation as success. finishedAt and deadline
	// both carry monotonic clock readings, so the comparison
	// below stays correct even when timers or other goroutines are starved.
	stoppedByBudget := budgetSource != nil && budgetSource.exhaustedByBudget()
	switch {
	case copyErr == nil:
		if opts.Duration > 0 && !stoppedByBudget && parentCtx.Err() == nil {
			if result.finishedAt.Before(deadline) {
				// The stream ended naturally while the client window was still
				// open: the server really closed early. The phase decides later
				// whether this was the shared budget boundary or a truncation.
				result.earlyEOF = true
			} else {
				// The window had already elapsed when the stream ended: the
				// server closing the connection is the normal end of a duration
				// limited window, even if the context timer has not been
				// observed to fire yet.
				result.endedByWindow = true
			}
		}
	case parentCtx.Err() != nil:
		result.err = copyErr
	case transferCtx.Err() == context.DeadlineExceeded:
		// The duration budget ended the read; this is the normal stop.
		result.endedByWindow = true
	default:
		result.err = copyErr
	}
	return result
}

func downloadWindow(connections int) string {
	if connections > 1 {
		return WindowDownloadMulti
	}
	return WindowDownload
}

// downloadEndpoint builds the /download URL including the requested byte and
// duration limits.
func downloadEndpoint(opts Options) (string, error) {
	parsed, err := url.Parse(opts.Target.BaseURL + "/download")
	if err != nil {
		return "", fmt.Errorf("download: build endpoint: %w", err)
	}
	query := parsed.Query()
	if opts.MaxBytes > 0 {
		// The byte budget is shared by all connections, so the server is asked
		// for at least that many bytes per connection and the client enforces
		// the aggregate limit.
		query.Set("bytes", strconv.FormatInt(opts.MaxBytes, 10))
	}
	if opts.Duration > 0 {
		milliseconds := opts.Duration.Milliseconds()
		if milliseconds < 1 {
			milliseconds = 1
		}
		query.Set("duration_ms", strconv.FormatInt(milliseconds, 10))
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
