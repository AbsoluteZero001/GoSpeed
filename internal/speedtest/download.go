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
		result := e.downloadWorker(transferCtx, ctx, opts, endpoint, budget, counters)
		result.index = index
		return result
	})
	samples := sampler.stop()

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
	switch {
	case opts.MaxBytes > 0 && aggregate.clientBytes == opts.MaxBytes:
		stopReason = StopReasonRequestedBytes
	case transferCtx.Err() == context.DeadlineExceeded:
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
	if aggregate.windowStart.IsZero() || !windowEnd.After(aggregate.windowStart) {
		return nil, fmt.Errorf("download: measurement window is empty: %w", ErrZeroDuration)
	}
	elapsed := windowEnd.Sub(aggregate.windowStart)
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
func (e *Engine) downloadWorker(transferCtx, parentCtx context.Context, opts Options, endpoint string, budget *sharedBudget, counters *transferCounters) workerResult {
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

	var firstByteAt atomic.Int64
	trace := &httptrace.ClientTrace{
		GotFirstResponseByte: func() {
			firstByteAt.Store(time.Now().UnixNano())
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

	if stamp := firstByteAt.Load(); stamp != 0 {
		result.firstAt = time.Unix(0, stamp)
	} else {
		result.firstAt = time.Now()
	}
	counters.markStarted(result.firstAt)
	counters.beginConnection()
	defer counters.endConnection()

	source := io.Reader(response.Body)
	if budget != nil {
		source = &budgetReader{reader: response.Body, budget: budget}
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

	stoppedByDuration := parentCtx.Err() == nil && transferCtx.Err() == context.DeadlineExceeded
	stoppedByBudget := budget != nil && budget.exhausted()
	switch {
	case copyErr == nil:
		if !stoppedByDuration && !stoppedByBudget && opts.Duration > 0 {
			// The server closed the stream while the window was still open.
			result.err = fmt.Errorf("%w: server closed the stream after %d bytes",
				ErrIncompleteTransfer, result.bytes)
		}
	case parentCtx.Err() != nil:
		result.err = copyErr
	case transferCtx.Err() == context.DeadlineExceeded:
		// The duration budget ended the read; this is the normal stop.
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
