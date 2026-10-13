package speedtest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This file pins the limit negotiation contract RC-01 established. Each test
// names one rule that must not drift silently.

// Rule 1: an explicit byte budget is the caller's own decision and is never
// rewritten. The engine may only lower a phase to a ceiling the caller did not
// ask for; a caller that set a budget keeps exactly that budget, and a budget
// the server refuses is refused before any traffic moves.
func TestExplicitMaxBytesIsNeverSilentlyModified(t *testing.T) {
	httpServer := limitedServer(t, 64<<20, 64<<20)

	// A loopback can move a small budget inside a single clock tick, which the
	// engine correctly refuses to rate. 16 MiB is the smallest budget this
	// repository treats as reliably measurable on the reference machine.
	const requested = measurableByteBudget
	options := durationOnlyOptions(httpServer.URL)
	options.Phases = PhasesUpload
	options.MaxBytes = requested

	result, err := NewEngine(nil).Run(context.Background(), options)
	if err != nil {
		t.Fatalf("Run: %v (%s)", err, result.ErrorMessage)
	}
	if result.Upload.ServerConfirmedBytes != requested {
		t.Fatalf("confirmed %d bytes, want the caller's own %d", result.Upload.ServerConfirmedBytes, requested)
	}
	// The recorded settings must state what the caller asked for, not what the
	// engine decided to use.
	if result.Settings.MaxBytes != requested {
		t.Fatalf("Settings.MaxBytes = %d, want the caller's %d", result.Settings.MaxBytes, requested)
	}
	if result.Upload.StopReason != StopReasonRequestedBytes {
		t.Fatalf("stop reason = %q, want %q: the caller's budget is what ended this",
			result.Upload.StopReason, StopReasonRequestedBytes)
	}
}

// Rule 2: the two endpoints have different limit semantics and the client must
// not invent symmetry between them.
//
// /upload applies max_upload_bytes unconditionally, so a duration run has to
// stop at it. /download applies max_download_bytes only to a request that
// declares one, and a duration run declares none, so a duration limited
// download is left to the window. If this test ever starts failing on the
// download side, a client side ceiling was added to a contract the server
// never made.
func TestUploadAndDownloadLimitSemanticsStayAsymmetric(t *testing.T) {
	const ceiling = measurableByteBudget
	// Both ceilings are the same value, so only the enforcement differs.
	httpServer := limitedServer(t, ceiling, ceiling)

	upload := durationOnlyOptions(httpServer.URL)
	upload.Phases = PhasesUpload
	uploadResult, err := NewEngine(nil).Run(context.Background(), upload)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if uploadResult.Upload.StopReason != StopReasonServerLimit {
		t.Fatalf("upload stop reason = %q, want %q: /upload enforces the ceiling",
			uploadResult.Upload.StopReason, StopReasonServerLimit)
	}
	if uploadResult.Upload.ServerConfirmedBytes != ceiling {
		t.Fatalf("upload confirmed %d bytes, want the ceiling %d", uploadResult.Upload.ServerConfirmedBytes, ceiling)
	}

	download := durationOnlyOptions(httpServer.URL)
	download.Phases = PhasesDownload
	download.Duration = 300 * time.Millisecond
	downloadResult, err := NewEngine(nil).Run(context.Background(), download)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if downloadResult.Download.StopReason == StopReasonServerLimit {
		t.Fatalf("download stop reason = %q: /download does not enforce its ceiling on a duration run, "+
			"so the client must not cap it either", downloadResult.Download.StopReason)
	}
	if downloadResult.Download.StopReason != StopReasonDuration {
		t.Fatalf("download stop reason = %q, want %q", downloadResult.Download.StopReason, StopReasonDuration)
	}
}

// Rule 3: a clean stop at a client budget and a server rejection are different
// outcomes and must never be reported as the same thing. A server that refuses
// a body it already promised to accept produces a failure, not a measurement.
func TestServerRejectionIsDistinctFromAClientBudgetStop(t *testing.T) {
	// The server advertises a ceiling it then ignores, rejecting much earlier.
	const advertised = 8 << 20
	const enforced = 512 << 10
	httpServer := dishonestUploadServer(t, advertised, enforced)

	options := durationOnlyOptions(httpServer.URL)
	options.Phases = PhasesUpload
	result, err := NewEngine(nil).Run(context.Background(), options)
	if err == nil {
		t.Fatalf("a server that rejected the body was reported as a measurement: %+v", result.Upload)
	}
	if !strings.Contains(err.Error(), "413") {
		t.Fatalf("error = %q, want the server's own 413", err.Error())
	}
	// The rejected run must not masquerade as a completed measurement, and must
	// not borrow the planned stop reason of a run that actually finished.
	if result.Status == StatusCompleted {
		t.Fatalf("a rejected upload was reported completed: %+v", result.Status)
	}
	if result.Upload != nil {
		t.Fatalf("a rejected upload must publish no rate: %+v", result.Upload)
	}
}

// Rule 4: parallel connections share one ceiling. The ceiling is per request on
// the server, so holding the aggregate down to it can only under-run each
// connection, never let one of them cross it.
func TestMultiConnectionUploadStaysUnderTheServerCeiling(t *testing.T) {
	const ceiling = measurableByteBudget
	for _, connections := range []int{2, 4, 8} {
		t.Run(connectionsName(connections), func(t *testing.T) {
			httpServer := limitedServer(t, ceiling, 64<<20)

			options := durationOnlyOptions(httpServer.URL)
			options.Phases = PhasesUpload
			options.Connections = connections
			result, err := NewEngine(nil).Run(context.Background(), options)
			if err != nil {
				t.Fatalf("%d connections: %v", connections, err)
			}
			if result.Upload.ServerConfirmedBytes > ceiling {
				t.Fatalf("aggregate confirmed %d bytes, over the %d ceiling", result.Upload.ServerConfirmedBytes, ceiling)
			}
			// Each connection is a separate request the server caps on its own,
			// so no single report may exceed the ceiling either.
			for _, report := range result.Upload.ConnectionReports {
				if report.ServerConfirmedBytes != nil && *report.ServerConfirmedBytes > ceiling {
					t.Fatalf("connection %d confirmed %d bytes, over the %d ceiling",
						report.Index+1, *report.ServerConfirmedBytes, ceiling)
				}
			}
			if result.Upload.ServerConfirmedBytes == 0 {
				t.Fatal("no bytes were transferred")
			}
		})
	}
}

// Rule 5: a node that declares no usable ceiling must leave the phase exactly
// as it was. Inventing a "safe default" would silently shorten every window
// against an unknown server.
func TestNodeWithoutAUsableCeilingGetsNoInventedOne(t *testing.T) {
	t.Run("no capabilities endpoint", func(t *testing.T) {
		// A v0.2.0 node that answers 404 to GET /capabilities.
		httpServer := capabilityServer(t, capabilityOptions{noCapabilities: true})
		assertUncappedRun(t, httpServer)
	})
	t.Run("capabilities without a byte limit", func(t *testing.T) {
		// A node that negotiates but declares max_upload_bytes as zero.
		httpServer := capabilityServer(t, capabilityOptions{
			capabilities:   []string{"latency", "download", "upload"},
			maxUploadBytes: 0,
		})
		assertUncappedRun(t, httpServer)
	})
}

func assertUncappedRun(t *testing.T, httpServer *httptest.Server) {
	t.Helper()
	options := durationOnlyOptions(httpServer.URL)
	options.Phases = PhasesUpload
	options.Duration = 300 * time.Millisecond
	result, err := NewEngine(nil).Run(context.Background(), options)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Upload.StopReason == StopReasonServerLimit {
		t.Fatalf("stop reason = %q: a node that declared no ceiling must not be given one", result.Upload.StopReason)
	}
	if result.Upload.StopReason != StopReasonDuration {
		t.Fatalf("stop reason = %q, want %q", result.Upload.StopReason, StopReasonDuration)
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "advertised") {
			t.Fatalf("invented a ceiling warning on a node that declared none: %q", warning)
		}
	}
}

func connectionsName(connections int) string {
	return map[int]string{2: "two", 4: "four", 8: "eight"}[connections]
}

// dishonestUploadServer advertises a generous ceiling and then enforces a much
// smaller one, which is what a misconfigured or changed node looks like to a
// client that trusted the advertisement.
func dishonestUploadServer(t *testing.T, advertised, enforced int64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/capabilities", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"protocol_version": 1,
			"server_version":   "dishonest",
			"capabilities":     []string{"latency", "download", "upload"},
			"limits": map[string]any{
				"max_duration_seconds": 60,
				"max_download_bytes":   advertised,
				"max_upload_bytes":     advertised,
			},
		})
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		body := http.MaxBytesReader(w, r.Body, enforced)
		if _, err := io.Copy(io.Discard, body); err != nil {
			http.Error(w, "upload exceeds the server limit of "+strconv.FormatInt(enforced, 10)+" bytes",
				http.StatusRequestEntityTooLarge)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	return httpServer
}
