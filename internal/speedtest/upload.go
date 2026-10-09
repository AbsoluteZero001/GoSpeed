package speedtest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/version"
)

const (
	// uploadBlockSize is the size of the random payload block that is streamed
	// repeatedly. Random bytes keep the server from compressing or optimizing
	// the payload away, and a single shared block keeps memory flat no matter
	// how many connections are used.
	uploadBlockSize = 256 << 10
	// uploadAckLimit bounds how much of the acknowledgement is read.
	uploadAckLimit = 1 << 20
)

var (
	uploadBlockOnce sync.Once
	uploadBlockData []byte
	uploadBlockErr  error
)

// uploadPayloadBlock lazily fills one random block with crypto/rand. The block
// is shared by every connection because it is only read after initialization.
func uploadPayloadBlock() ([]byte, error) {
	uploadBlockOnce.Do(func() {
		block := make([]byte, uploadBlockSize)
		if _, err := rand.Read(block); err != nil {
			uploadBlockErr = fmt.Errorf("upload: generate random payload: %w", err)
			return
		}
		uploadBlockData = block
	})
	return uploadBlockData, uploadBlockErr
}

// uploadAck is the acknowledgement the GoSpeed server returns for /upload.
type uploadAck struct {
	Status        string `json:"status"`
	BytesReceived int64  `json:"bytes_received"`
	DurationNs    int64  `json:"duration_ns"`
}

// uploadReader streams a repeating random block for one connection. It stops
// at the shared byte budget, at the shared duration deadline, or when the
// transport stops reading.
//
// Counters are atomic because net/http reads the request body from its own
// goroutine while the worker and the sampler read the counters.
type uploadReader struct {
	block    []byte
	offset   int
	budget   *sharedBudget
	deadline time.Time
	counters *transferCounters
	written  atomic.Int64
	started  atomic.Int64
}

func (r *uploadReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if !r.deadline.IsZero() && !time.Now().Before(r.deadline) {
		// The shared window is over: end the body gracefully so the server can
		// finish counting and confirm what it received.
		return 0, io.EOF
	}
	remaining := len(p)
	if r.budget != nil {
		allowed := r.budget.take(len(p))
		if allowed == 0 {
			return 0, io.EOF
		}
		remaining = allowed
	}
	copied := 0
	for copied < remaining {
		n := copy(p[copied:remaining], r.block[r.offset:])
		copied += n
		r.offset += n
		if r.offset >= len(r.block) {
			r.offset = 0
		}
	}
	if copied > 0 {
		now := time.Now()
		r.started.CompareAndSwap(0, now.UnixNano())
		r.written.Add(int64(copied))
		if r.counters != nil {
			r.counters.addBytes(int64(copied))
			r.counters.markStarted(now)
		}
	}
	return copied, nil
}

// measureUpload streams random data to /upload with one or more parallel
// connections and refuses to publish a rate until the server confirms exactly
// how many bytes it received.
//
// Timing windows:
//   - one connection: first body write -> its server confirmation
//   - N connections: first body write of the first connection -> the last
//     server confirmation of the phase
//
// Both windows include the confirmation round trip, which is stated in the
// result's measurement_window field and makes the rate conservative rather
// than optimistic.
func (e *Engine) measureUpload(ctx context.Context, opts Options) (*UploadResult, error) {
	phaseCtx, cancelPhase := context.WithTimeout(ctx, opts.Timeout)
	defer cancelPhase()

	// The upload body ends by itself when the shared deadline passes (the
	// reader returns io.EOF), which lets the server finish counting and answer.
	// Cancelling the whole request at that moment would kill the confirmation
	// read, so the phase context alone is used as the hard timeout.
	transferCtx := phaseCtx
	var transferDeadline time.Time
	if opts.Duration > 0 {
		transferDeadline = time.Now().Add(opts.Duration)
	}

	block, err := uploadPayloadBlock()
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
		State:          StateUploadTesting,
		Phase:          PhaseUpload,
		Stage:          StageStart,
		PhaseStartedAt: time.Now().UTC(),
		Message:        fmt.Sprintf("%d connections", opts.Connections),
	})
	sampler := newSampler(opts, PhaseUpload, counters, spec)
	sampler.start()
	results := runWorkers(opts.Connections, func(index int) workerResult {
		result := e.uploadWorker(transferCtx, opts, transferDeadline, block, budget, counters)
		result.index = index
		return result
	})
	samples := sampler.stop()

	aggregate := aggregateWorkers(results)

	if ctx.Err() != nil {
		return nil, phaseError(PhaseUpload, ctx, transferCtx, opts.Timeout, firstWorkerError(results))
	}
	if phaseCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("upload: timed out after %s: %w", opts.Timeout, context.DeadlineExceeded)
	}
	if aggregate.active == 0 {
		if cause := firstWorkerError(results); cause != nil {
			return nil, fmt.Errorf("%w: all %d connections failed: %w",
				ErrAllConnectionsFailed, opts.Connections, cause)
		}
		return nil, fmt.Errorf("%w: all %d connections failed: %s",
			ErrAllConnectionsFailed, opts.Connections, firstError(aggregate.errors))
	}
	// The client total and the server total must match, otherwise the byte
	// stream was truncated somewhere and no rate may be published.
	if aggregate.clientBytes != aggregate.serverConfirmed {
		return nil, fmt.Errorf("%w: client sent %d bytes, server confirmed %d bytes across %d connections (%d active, %d failed): %s",
			ErrByteCountMismatch, aggregate.clientBytes, aggregate.serverConfirmed,
			opts.Connections, aggregate.active, aggregate.failed, firstError(aggregate.errors))
	}
	if aggregate.serverConfirmed == 0 {
		return nil, ErrNoDataTransferred
	}
	if aggregate.windowStart.IsZero() || !aggregate.finishedAt.After(aggregate.windowStart) {
		return nil, fmt.Errorf("upload: measurement window is empty: %w", ErrZeroDuration)
	}
	elapsed := aggregate.finishedAt.Sub(aggregate.windowStart)
	rate, err := Mbps(aggregate.serverConfirmed, elapsed)
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	megabytesPerSecond, err := MBps(aggregate.serverConfirmed, elapsed)
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	stopReason := StopReasonDuration
	if opts.MaxBytes > 0 && aggregate.serverConfirmed == opts.MaxBytes {
		stopReason = StopReasonRequestedBytes
	}
	emit(opts, Progress{
		State:             StateUploadTesting,
		Phase:             PhaseUpload,
		Stage:             StageDone,
		Elapsed:           elapsed,
		Bytes:             aggregate.serverConfirmed,
		Mbps:              rate,
		ActiveConnections: aggregate.active,
	})
	return &UploadResult{
		TransferResult: TransferResult{
			Bytes:             aggregate.serverConfirmed,
			DurationNs:        elapsed,
			Mbps:              rate,
			MBPerSecond:       megabytesPerSecond,
			Connections:       opts.Connections,
			ActiveConnections: aggregate.active,
			FailedConnections: aggregate.failed,
			MeasurementWindow: uploadWindow(opts.Connections),
			StopReason:        stopReason,
			Samples:           samples,
			Statistics:        Summarize(samples),
			ConnectionReports: buildConnectionReports(results, true),
		},
		ServerConfirmedBytes: aggregate.serverConfirmed,
		ServerDurationNs:     aggregate.serverDurationMax,
	}, nil
}

// uploadWorker streams one connection and waits for the server confirmation.
func (e *Engine) uploadWorker(transferCtx context.Context, opts Options, deadline time.Time, block []byte, budget *sharedBudget, counters *transferCounters) workerResult {
	var result workerResult
	reader := &uploadReader{block: block, budget: budget, deadline: deadline, counters: counters}
	request, err := http.NewRequestWithContext(transferCtx, http.MethodPost, opts.Target.BaseURL+"/upload", reader)
	if err != nil {
		result.err = fmt.Errorf("build request: %w", err)
		result.finishedAt = time.Now()
		return result
	}
	// Unknown length: the body ends when the reader reaches its budget, so the
	// request is sent chunked instead of buffering the whole payload.
	request.ContentLength = -1
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("User-Agent", version.UserAgent())
	request.Header.Set("Cache-Control", "no-store")

	counters.beginConnection()
	defer counters.endConnection()
	response, err := e.client.Do(request)
	result.bytes = reader.written.Load()
	if started := reader.started.Load(); started != 0 {
		result.firstAt = time.Unix(0, started)
	}
	result.finishedAt = time.Now()
	result.lastAt = result.finishedAt
	if err != nil {
		result.err = fmt.Errorf("request failed after %d bytes: %w", result.bytes, err)
		return result
	}
	defer response.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(response.Body, uploadAckLimit))
	result.finishedAt = time.Now()
	result.lastAt = result.finishedAt
	if response.StatusCode != http.StatusOK {
		result.err = unexpectedUploadStatus(response, body)
		return result
	}
	if readErr != nil {
		result.err = fmt.Errorf("read server acknowledgement: %w", readErr)
		return result
	}
	var ack uploadAck
	if err := json.Unmarshal(body, &ack); err != nil {
		result.err = fmt.Errorf("invalid server acknowledgement: %w", err)
		return result
	}
	if ack.Status != "" && ack.Status != "ok" {
		result.err = fmt.Errorf("server reported status %q", ack.Status)
		return result
	}
	result.serverConfirmed = ack.BytesReceived
	result.serverDuration = time.Duration(ack.DurationNs)
	return result
}

func uploadWindow(connections int) string {
	if connections > 1 {
		return WindowUploadMulti
	}
	return WindowUpload
}

// unexpectedUploadStatus reports a non-success response with a bounded snippet
// of the body so the caller can see what the server objected to.
func unexpectedUploadStatus(response *http.Response, body []byte) error {
	snippet := strings.TrimSpace(string(body))
	if snippet == "" {
		return fmt.Errorf("%w: POST /upload: %s", ErrUnexpectedStatus, response.Status)
	}
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}
	return fmt.Errorf("%w: POST /upload: %s: %s", ErrUnexpectedStatus, response.Status, snippet)
}
