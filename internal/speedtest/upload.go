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
	// the payload away, and a single block keeps memory flat.
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
// is shared by all uploads because it is only read after initialization.
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

// uploadReader streams a repeating random block. It stops at the byte limit,
// when the duration budget is used up, or when the transport stops reading.
// Counters are atomic because the progress reporter reads them concurrently.
type uploadReader struct {
	block    []byte
	offset   int
	limit    int64
	duration time.Duration
	written  atomic.Int64
	started  atomic.Int64
}

func (r *uploadReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	written := r.written.Load()
	if r.limit > 0 && written >= r.limit {
		return 0, io.EOF
	}
	if r.duration > 0 && written > 0 {
		if started := r.started.Load(); started != 0 && time.Since(time.Unix(0, started)) >= r.duration {
			return 0, io.EOF
		}
	}
	remaining := len(p)
	if r.limit > 0 {
		if left := r.limit - written; int64(remaining) > left {
			remaining = int(left)
		}
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
		// CompareAndSwap keeps the first write timestamp monotonic even if a
		// future transport implementation reads the body concurrently.
		r.started.CompareAndSwap(0, time.Now().UnixNano())
		r.written.Add(int64(copied))
	}
	return copied, nil
}

// measureUpload streams random data to /upload and refuses to publish a rate
// until the server confirms exactly how many bytes it received.
//
// The measurement window starts when the transport asks for the first payload
// byte and ends when the server confirmation has been read. Using the server
// confirmed byte count as the numerator prevents a local write buffer from
// inflating the result.
func (e *Engine) measureUpload(ctx context.Context, opts Options) (*UploadResult, error) {
	phaseCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	block, err := uploadPayloadBlock()
	if err != nil {
		return nil, err
	}
	reader := &uploadReader{block: block, limit: opts.MaxBytes, duration: opts.Duration}
	request, err := http.NewRequestWithContext(phaseCtx, http.MethodPost, opts.Target.BaseURL+"/upload", reader)
	if err != nil {
		return nil, fmt.Errorf("upload: build request: %w", err)
	}
	// Unknown length: the body ends when the reader reaches its budget, so the
	// request is sent chunked instead of buffering the whole payload.
	request.ContentLength = -1
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("User-Agent", version.UserAgent())
	request.Header.Set("Cache-Control", "no-store")

	emit(opts, Progress{Phase: PhaseUpload, Stage: StageStart})
	done := make(chan struct{})
	var reporter sync.WaitGroup
	if opts.Progress != nil {
		reporter.Add(1)
		go reportUploadProgress(opts, reader, done, &reporter)
	}

	response, err := e.client.Do(request)
	close(done)
	reporter.Wait()
	if err != nil {
		if ctx.Err() != nil {
			return nil, phaseError(PhaseUpload, ctx, phaseCtx, opts.Timeout, err)
		}
		if phaseCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("upload: timed out after %s: %w", opts.Timeout, context.DeadlineExceeded)
		}
		return nil, fmt.Errorf("upload: request failed after %d bytes: %w", reader.written.Load(), err)
	}
	defer response.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(response.Body, uploadAckLimit))
	confirmedAt := time.Now()
	if response.StatusCode != http.StatusOK {
		return nil, unexpectedUploadStatus(response, body)
	}
	if readErr != nil {
		return nil, fmt.Errorf("upload: read server acknowledgement: %w", readErr)
	}
	var ack uploadAck
	if err := json.Unmarshal(body, &ack); err != nil {
		return nil, fmt.Errorf("upload: invalid server acknowledgement: %w", err)
	}
	if ack.Status != "" && ack.Status != "ok" {
		return nil, fmt.Errorf("upload: server reported status %q", ack.Status)
	}
	clientBytes := reader.written.Load()
	if ack.BytesReceived != clientBytes {
		return nil, fmt.Errorf("%w: client sent %d bytes, server received %d bytes",
			ErrByteCountMismatch, clientBytes, ack.BytesReceived)
	}
	if clientBytes == 0 {
		return nil, ErrNoDataTransferred
	}
	startedAt := reader.started.Load()
	if startedAt == 0 {
		return nil, ErrNoDataTransferred
	}
	elapsed := confirmedAt.Sub(time.Unix(0, startedAt))
	rate, err := Mbps(clientBytes, elapsed)
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	megabytesPerSecond, err := MBps(clientBytes, elapsed)
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	stopReason := StopReasonDuration
	if opts.MaxBytes > 0 && clientBytes == opts.MaxBytes {
		stopReason = StopReasonRequestedBytes
	}
	emit(opts, Progress{
		Phase:   PhaseUpload,
		Stage:   StageDone,
		Elapsed: elapsed,
		Bytes:   clientBytes,
		Mbps:    rate,
	})
	return &UploadResult{
		TransferResult: TransferResult{
			Bytes:             clientBytes,
			DurationNs:        elapsed,
			Mbps:              rate,
			MBPerSecond:       megabytesPerSecond,
			Connections:       opts.Connections,
			MeasurementWindow: WindowUpload,
			StopReason:        stopReason,
		},
		ServerConfirmedBytes: ack.BytesReceived,
		ServerDurationNs:     time.Duration(ack.DurationNs),
	}, nil
}

// reportUploadProgress samples the upload counters every progressInterval and
// stops as soon as done is closed.
func reportUploadProgress(opts Options, reader *uploadReader, done <-chan struct{}, reporter *sync.WaitGroup) {
	defer reporter.Done()
	ticker := time.NewTicker(progressInterval)
	defer ticker.Stop()
	lastBytes := int64(0)
	lastSample := time.Now()
	for {
		select {
		case <-done:
			return
		case now := <-ticker.C:
			total := reader.written.Load()
			event := Progress{Phase: PhaseUpload, Stage: StageProgress, Bytes: total}
			if started := reader.started.Load(); started != 0 {
				event.Elapsed = now.Sub(time.Unix(0, started))
			}
			if event.Elapsed > 0 {
				if average, err := Mbps(total, event.Elapsed); err == nil {
					event.Mbps = average
				}
			}
			if interval := now.Sub(lastSample); interval > 0 {
				if instant, err := Mbps(total-lastBytes, interval); err == nil {
					event.InstantMbps = instant
				}
			}
			lastBytes, lastSample = total, now
			emit(opts, event)
		}
	}
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
