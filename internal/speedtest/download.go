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
	// transferBufferSize is reused for every read so a long test does not grow
	// the heap: the payload never lives in memory as a whole.
	transferBufferSize = 64 << 10
	// progressInterval throttles progress callbacks from inside the read loop.
	progressInterval = 250 * time.Millisecond
	// errorBodySnippet bounds how much of an error response is read.
	errorBodySnippet = 4 << 10
	// serverEOFTolerance absorbs scheduling jitter when a duration limited
	// stream ends naturally.
	serverEOFTolerance = 50 * time.Millisecond
)

// measureDownload performs a real HTTP download and counts the bytes that
// actually arrived.
//
// The measurement window starts at the first response byte rather than at the
// start of the request, so DNS resolution, TCP/TLS setup and the request
// round trip are excluded from the rate. Reporting bytes over that window
// keeps the result explainable and reproducible.
func (e *Engine) measureDownload(ctx context.Context, opts Options) (*TransferResult, error) {
	phaseCtx, cancelPhase := context.WithTimeout(ctx, opts.Timeout)
	defer cancelPhase()
	// The duration deadline ends a time limited test gracefully; the phase
	// deadline is the hard upper bound that must not be exceeded.
	transferCtx, cancelTransfer := context.WithTimeout(phaseCtx, opts.Duration)
	defer cancelTransfer()

	endpoint, err := downloadEndpoint(opts)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(transferCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("download: build request: %w", err)
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

	emit(opts, Progress{Phase: PhaseDownload, Stage: StageStart})
	response, err := e.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, phaseError(PhaseDownload, ctx, transferCtx, opts.Timeout, err)
		}
		if phaseCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("download: timed out after %s: %w", opts.Timeout, context.DeadlineExceeded)
		}
		if transferCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("%w within %s: %v", ErrNoDataTransferred, opts.Duration, err)
		}
		return nil, fmt.Errorf("download: request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, errorBodySnippet))
		return nil, fmt.Errorf("%w: GET /download: %s", ErrUnexpectedStatus, response.Status)
	}

	windowStart := time.Now()
	if stamp := firstByteAt.Load(); stamp != 0 {
		windowStart = time.Unix(0, stamp)
	}
	tracker := newRateTracker(windowStart)
	// lastReport starts at the measurement window start so the first progress
	// callback waits for a full interval and therefore carries a real rate.
	writer := &countingWriter{tracker: tracker, reportInterval: progressInterval, lastReport: windowStart}
	writer.onReport = func(snapshot RateSnapshot) {
		emit(opts, Progress{
			Phase:       PhaseDownload,
			Stage:       StageProgress,
			Elapsed:     snapshot.Elapsed,
			Bytes:       snapshot.TotalBytes,
			Mbps:        snapshot.AverageMbps,
			InstantMbps: snapshot.InstantMbps,
		})
	}

	_, copyErr := io.CopyBuffer(writer, response.Body, make([]byte, transferBufferSize))
	windowEnd := time.Now()
	elapsed := windowEnd.Sub(windowStart)
	received := writer.written

	stopReason := StopReasonServerEOF
	switch {
	case opts.MaxBytes > 0 && received == opts.MaxBytes:
		stopReason = StopReasonRequestedBytes
	case transferCtx.Err() == context.DeadlineExceeded && phaseCtx.Err() == nil:
		stopReason = StopReasonDuration
	case ctx.Err() != nil:
		return nil, phaseError(PhaseDownload, ctx, transferCtx, opts.Timeout, copyErr)
	case phaseCtx.Err() == context.DeadlineExceeded:
		return nil, fmt.Errorf("download: timed out after %s: %w", opts.Timeout, context.DeadlineExceeded)
	case copyErr != nil:
		return nil, fmt.Errorf("download: transfer interrupted after %d bytes: %w", received, copyErr)
	}

	if opts.MaxBytes > 0 && received != opts.MaxBytes {
		return nil, fmt.Errorf("%w: expected %d bytes, received %d", ErrIncompleteTransfer, opts.MaxBytes, received)
	}
	if opts.MaxBytes == 0 && stopReason == StopReasonServerEOF && elapsed+serverEOFTolerance < opts.Duration {
		return nil, fmt.Errorf("%w: server closed the download after %s (%d bytes) although the test window was %s",
			ErrIncompleteTransfer, elapsed.Round(time.Millisecond), received, opts.Duration)
	}
	if received == 0 {
		return nil, ErrNoDataTransferred
	}
	rate, err := Mbps(received, elapsed)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	megabytesPerSecond, err := MBps(received, elapsed)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	emit(opts, Progress{
		Phase:   PhaseDownload,
		Stage:   StageDone,
		Elapsed: elapsed,
		Bytes:   received,
		Mbps:    rate,
	})
	return &TransferResult{
		Bytes:             received,
		DurationNs:        elapsed,
		Mbps:              rate,
		MBPerSecond:       megabytesPerSecond,
		Connections:       opts.Connections,
		MeasurementWindow: WindowDownload,
		StopReason:        stopReason,
	}, nil
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

// countingWriter counts written bytes, feeds a rate tracker and throttles
// progress callbacks without buffering the payload.
type countingWriter struct {
	tracker        *rateTracker
	onReport       func(RateSnapshot)
	reportInterval time.Duration
	written        int64
	lastReport     time.Time
}

func (w *countingWriter) Write(p []byte) (int, error) {
	now := time.Now()
	w.written += int64(len(p))
	snapshot := w.tracker.Add(int64(len(p)), now)
	if w.onReport != nil && (w.lastReport.IsZero() || now.Sub(w.lastReport) >= w.reportInterval) {
		w.lastReport = now
		w.onReport(snapshot)
	}
	return len(p), nil
}
