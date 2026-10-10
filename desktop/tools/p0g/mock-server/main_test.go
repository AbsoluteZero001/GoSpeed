package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestFixture() *fixture {
	return &fixture{
		logger:   log.New(io.Discard, "", 0),
		stats:    newStats(),
		control:  newP0GControl(),
		maxBytes: defaultMaxBytes,
	}
}

func TestOptionsProvidesCORSAndTimingHeaders(t *testing.T) {
	request := httptest.NewRequest(http.MethodOptions, "/__down", nil)
	response := httptest.NewRecorder()

	newTestFixture().ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
	if got := response.Header().Get("Access-Control-Allow-Private-Network"); got != "true" {
		t.Fatalf("Access-Control-Allow-Private-Network = %q", got)
	}
	if got := response.Header().Get("Timing-Allow-Origin"); got != "*" {
		t.Fatalf("Timing-Allow-Origin = %q", got)
	}
}

func TestNormalDownloadReturnsRequestedBytes(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodGet,
		"/__down?bytes=16&scenario=normal&delay_ms=0",
		nil,
	)
	response := httptest.NewRecorder()
	fixture := newTestFixture()

	fixture.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Body.Len() != 16 {
		t.Fatalf("body length = %d, want 16", response.Body.Len())
	}
	if got := response.Header().Get("Server-Timing"); got != "cfSpeedEdge;dur=0" {
		t.Fatalf("Server-Timing = %q", got)
	}
	snapshot := fixture.stats.snapshot("normal")
	if snapshot["started"] != 1 || snapshot["completed"] != 1 || snapshot["inFlight"] != 0 {
		t.Fatalf("unexpected stats: %#v", snapshot)
	}
}

func TestStalledBodyMarksAbortedWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(
		http.MethodGet,
		"/__down?bytes=16&scenario=cancel",
		nil,
	).WithContext(ctx)
	response := httptest.NewRecorder()
	fixture := newTestFixture()
	done := make(chan struct{})
	go func() {
		fixture.ServeHTTP(response, request)
		close(done)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		snapshot := fixture.stats.snapshot("cancel")
		if snapshot["inFlight"] == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stalled request did not enter in-flight state")
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stalled handler did not stop after context cancellation")
	}

	snapshot := fixture.stats.snapshot("cancel")
	if snapshot["started"] != 1 || snapshot["completed"] != 0 || snapshot["aborted"] != 1 {
		t.Fatalf("unexpected stats after cancellation: %#v", snapshot)
	}
}
