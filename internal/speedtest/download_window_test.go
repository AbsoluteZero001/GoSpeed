package speedtest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The tests in this file pin how the download phase classifies the end of a
// transfer (download worker classification plus the phase summary). They are
// deterministic by construction: every classification margin is at least
// 80 ms on a loopback fixture and most are in the hundreds of milliseconds,
// far above scheduler jitter, and no assertion depends on how fast the
// loopback transfers — only on the order of events, which the fixture
// controls. No test sleeps to paper over a race; the races themselves were
// removed at the event point where the read ends.

// serveFor streams blocks for closeAfter (measured on the server's own clock,
// exactly like the production handler) and then ends the response cleanly.
// It stops early when the client disconnects.
func serveFor(closeAfter time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		block := make([]byte, 32<<10)
		flusher, _ := w.(http.Flusher)
		deadline := time.Now().Add(closeAfter)
		for time.Now().Before(deadline) {
			if r.Context().Err() != nil {
				return
			}
			if _, err := w.Write(block); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// writeAndClose streams exactly payloadBytes and ends the response cleanly,
// in far less time than any duration window used by these tests.
func writeAndClose(payloadBytes int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		block := make([]byte, 32<<10)
		flusher, _ := w.(http.Flusher)
		var written int64
		for written < payloadBytes {
			if r.Context().Err() != nil {
				return
			}
			chunk := block
			if remaining := payloadBytes - written; remaining < int64(len(chunk)) {
				chunk = chunk[:remaining]
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			written += int64(len(chunk))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// Test A: the server closes the stream clearly before the client window
// ends. The phase must fail as an incomplete transfer — the window fix must
// never turn a real truncation into a successful measurement.
func TestDownloadEarlyServerCloseBeforeWindowFails(t *testing.T) {
	server := httptest.NewServer(writeAndClose(256 << 10))
	defer server.Close()

	options := normalizedOptions(t, Options{
		Target:   Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:   PhasesDownload,
		Duration: 2 * time.Second,
		Timeout:  5 * time.Second,
	})
	engine := NewEngine(nil)
	_, err := engine.measureDownload(context.Background(), options)
	if err == nil {
		t.Fatal("an early server close must fail the phase, got no error")
	}
	if !errors.Is(err, ErrIncompleteTransfer) {
		t.Fatalf("error = %v, want ErrIncompleteTransfer", err)
	}
	if !errors.Is(err, ErrAllConnectionsFailed) {
		t.Fatalf("error = %v, want ErrAllConnectionsFailed", err)
	}
}

// Test B: the client window runs to its deadline while the server keeps
// serving. The phase must complete with StopReasonDuration, whether the read
// is interrupted by the context or the server's own clock closes first.
func TestDownloadDurationWindowCompletes(t *testing.T) {
	server := httptest.NewServer(serveFor(10 * time.Second))
	defer server.Close()

	options := normalizedOptions(t, Options{
		Target:   Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:   PhasesDownload,
		Duration: 300 * time.Millisecond,
		Timeout:  5 * time.Second,
	})
	engine := NewEngine(nil)
	result, err := engine.measureDownload(context.Background(), options)
	if err != nil {
		t.Fatalf("measureDownload returned error: %v", err)
	}
	if result.StopReason != StopReasonDuration {
		t.Fatalf("stop reason = %q, want %q", result.StopReason, StopReasonDuration)
	}
	if result.Bytes == 0 {
		t.Fatal("a completed download window transferred no data")
	}
	if result.FailedConnections != 0 {
		t.Fatalf("failed connections = %d, want 0", result.FailedConnections)
	}
}

// Test C: the server's close lands near the client deadline, where the
// historic double clock race produced random failures. Both orders must give
// one deterministic outcome each: a close that lands after the deadline is
// the normal end of the window (success, whoever wins the close), and a
// close well before it is a truncation (failure). The margins leave only a
// multi-second whole-process stall to reorder them, which no CI tolerates.
func TestDownloadServerCloseNearWindowEndIsDeterministic(t *testing.T) {
	t.Run("server closes just after the client window", func(t *testing.T) {
		const window = 600 * time.Millisecond
		server := httptest.NewServer(serveFor(window + 80*time.Millisecond))
		defer server.Close()

		options := normalizedOptions(t, Options{
			Target:   Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
			Phases:   PhasesDownload,
			Duration: window,
			Timeout:  5 * time.Second,
		})
		engine := NewEngine(nil)
		result, err := engine.measureDownload(context.Background(), options)
		if err != nil {
			t.Fatalf("measureDownload returned error: %v", err)
		}
		if result.StopReason != StopReasonDuration {
			t.Fatalf("stop reason = %q, want %q", result.StopReason, StopReasonDuration)
		}
		if result.FailedConnections != 0 {
			t.Fatalf("failed connections = %d, want 0", result.FailedConnections)
		}
	})
	t.Run("server closes well before the client window", func(t *testing.T) {
		const window = 1500 * time.Millisecond
		server := httptest.NewServer(serveFor(300 * time.Millisecond))
		defer server.Close()

		options := normalizedOptions(t, Options{
			Target:   Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
			Phases:   PhasesDownload,
			Duration: window,
			Timeout:  5 * time.Second,
		})
		engine := NewEngine(nil)
		_, err := engine.measureDownload(context.Background(), options)
		if !errors.Is(err, ErrIncompleteTransfer) {
			t.Fatalf("error = %v, want ErrIncompleteTransfer", err)
		}
	})
}

// Test D: a user cancel mid-window is cancelled with the existing clear
// cancellation semantics — never completed, never a fake incomplete.
func TestDownloadUserCancelIsCancelled(t *testing.T) {
	server := httptest.NewServer(serveFor(10 * time.Second))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	engine := NewEngine(nil)
	result, err := engine.Run(ctx, Options{
		Target:   Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:   PhasesDownload,
		Duration: 20 * time.Second,
		Timeout:  30 * time.Second,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if result.Status != StatusCancelled {
		t.Fatalf("status = %q, want %q", result.Status, StatusCancelled)
	}
}

// Test E: a real transport failure (server drops the connection mid-stream,
// leaving the chunked body unterminated) must stay a failure that shows the
// underlying error. Neither the early close classification nor the window
// fix may absorb it.
func TestDownloadTransportErrorIsNotMasked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(make([]byte, 64<<10)); err != nil {
			return
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response does not support hijacking")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		_ = conn.Close() // drop the connection without the terminating chunk
	}))
	defer server.Close()

	options := normalizedOptions(t, Options{
		Target:   Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:   PhasesDownload,
		Duration: 2 * time.Second,
		Timeout:  5 * time.Second,
	})
	engine := NewEngine(nil)
	_, err := engine.measureDownload(context.Background(), options)
	if err == nil {
		t.Fatal("a dropped connection must fail the phase, got no error")
	}
	if !errors.Is(err, ErrAllConnectionsFailed) {
		t.Fatalf("error = %v, want ErrAllConnectionsFailed", err)
	}
	if errors.Is(err, ErrIncompleteTransfer) {
		t.Fatalf("a transport error must not be reported as an incomplete transfer: %v", err)
	}
	if !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("error = %v, want the underlying transport error to stay visible", err)
	}
}

// Test F: with multiple connections, one failing connection keeps the
// existing aggregation rules — the phase completes on the surviving
// connections, the failed connection stays visible in the connection
// reports, and the result carries a warning. Bytes come only from the
// connections that actually transferred.
func TestDownloadPartialConnectionFailureKeepsAggregate(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			writeAndClose(256<<10)(w, r)
			return
		}
		serveFor(10*time.Second)(w, r)
	}))
	defer server.Close()

	engine := NewEngine(nil)
	result, err := engine.Run(context.Background(), Options{
		Target:      Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:      PhasesDownload,
		Duration:    700 * time.Millisecond,
		Timeout:     5 * time.Second,
		Connections: 2,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q (%s), want completed", result.Status, result.ErrorMessage)
	}
	download := result.Download
	if download == nil {
		t.Fatal("completed run has no download result")
	}
	if download.FailedConnections != 1 {
		t.Fatalf("failed connections = %d, want 1", download.FailedConnections)
	}
	if download.ActiveConnections != 1 {
		t.Fatalf("active connections = %d, want 1", download.ActiveConnections)
	}
	if download.StopReason != StopReasonDuration {
		t.Fatalf("stop reason = %q, want %q", download.StopReason, StopReasonDuration)
	}
	if download.Bytes == 0 || download.Mbps <= 0 {
		t.Fatalf("aggregate = %+v, want real transferred bytes and a positive rate", download)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("a partial failure must surface as a warning")
	}
	for _, report := range download.ConnectionReports {
		if report.State == ConnectionFailed && !strings.Contains(report.Error, ErrIncompleteTransfer.Error()) {
			t.Fatalf("failed connection %d reports %q, want the incomplete transfer cause", report.Index, report.Error)
		}
	}
}
