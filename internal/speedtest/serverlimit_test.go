package speedtest

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/server"
)

// limitedServer starts the real GoSpeed test server with the given byte
// ceilings. It is the real handler set, not a stub, so the 413 behaviour this
// file guards against is the one a user actually hits.
func limitedServer(t *testing.T, maxUploadBytes, maxDownloadBytes int64) *httptest.Server {
	t.Helper()
	testServer, err := server.New(server.Config{
		MaxUploadBytes:          maxUploadBytes,
		MaxDownloadBytes:        maxDownloadBytes,
		MaxDownloadDuration:     30 * time.Second,
		DefaultDownloadDuration: 10 * time.Second,
		WriteTimeout:            60 * time.Second,
		ReadTimeout:             60 * time.Second,
	})
	if err != nil {
		t.Fatalf("server.New returned error: %v", err)
	}
	httpServer := httptest.NewServer(testServer.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer
}

// durationOnlyOptions is the shape of the reported failure: a run bounded only
// by time, with no byte budget at all. MaxBytes stays zero, which is exactly
// what the desktop client sends when the user leaves the byte budget unset.
func durationOnlyOptions(baseURL string) Options {
	return Options{
		Target:                Target{ID: "local", Name: "local", BaseURL: baseURL, Protocol: ProtocolHTTP, Local: true},
		Duration:              10 * time.Second,
		Timeout:               30 * time.Second,
		Connections:           1,
		NegotiateCapabilities: true,
	}
}

// TestDurationLimitedUploadStopsAtServerCeiling reproduces the reported
// failure. On a loopback link a ten second upload moves far more than the
// server's advertised max_upload_bytes, so before the ceiling was honoured the
// server's MaxBytesReader rejected the body with HTTP 413 and the whole phase
// was reported as failed.
func TestDurationLimitedUploadStopsAtServerCeiling(t *testing.T) {
	const ceiling = 4 << 20
	httpServer := limitedServer(t, ceiling, 64<<20)

	options := durationOnlyOptions(httpServer.URL)
	options.Phases = PhasesUpload
	result, err := NewEngine(nil).Run(context.Background(), options)
	if err != nil {
		t.Fatalf("duration limited upload returned error: %v (status %q, message %q)",
			err, result.Status, result.ErrorMessage)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, want %q", result.Status, StatusCompleted)
	}
	if result.Upload == nil {
		t.Fatal("upload result is nil")
	}
	if result.Upload.ServerConfirmedBytes != ceiling {
		t.Fatalf("server confirmed %d bytes, want exactly the advertised ceiling %d",
			result.Upload.ServerConfirmedBytes, ceiling)
	}
	if result.Upload.StopReason != StopReasonServerLimit {
		t.Fatalf("stop reason = %q, want %q", result.Upload.StopReason, StopReasonServerLimit)
	}
	if result.Upload.Mbps <= 0 {
		t.Fatalf("upload rate = %v, want a positive measurement", result.Upload.Mbps)
	}
	// The caller's own settings must not be rewritten to look like a byte
	// budget the user never asked for.
	if result.Settings.MaxBytes != 0 {
		t.Fatalf("Settings.MaxBytes = %d, want 0: the run had no caller byte budget", result.Settings.MaxBytes)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("no warning: a run that stopped at the server ceiling must say so")
	}
	found := false
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "advertised") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings %q do not explain the server byte ceiling", result.Warnings)
	}
}

// TestDurationLimitedUploadUnderCeilingCompletesOnDuration proves the ceiling
// does not truncate a run that would have finished inside it: the stop reason
// must stay duration_elapsed and the warning must stay absent.
func TestDurationLimitedUploadUnderCeilingCompletesOnDuration(t *testing.T) {
	// A ceiling far above what a short loopback upload can move in the window.
	httpServer := limitedServer(t, 1<<30, 1<<30)

	options := durationOnlyOptions(httpServer.URL)
	options.Phases = PhasesUpload
	options.Duration = 300 * time.Millisecond
	result, err := NewEngine(nil).Run(context.Background(), options)
	if err != nil {
		t.Fatalf("upload returned error: %v (%s)", err, result.ErrorMessage)
	}
	if result.Upload.StopReason != StopReasonDuration {
		t.Fatalf("stop reason = %q, want %q: the ceiling was never reached",
			result.Upload.StopReason, StopReasonDuration)
	}
	if result.Upload.ServerConfirmedBytes <= 0 {
		t.Fatal("no bytes were transferred")
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "advertised") {
			t.Fatalf("unexpected server limit warning on a duration completed run: %q", warning)
		}
	}
}

// TestServerWithoutByteLimitKeepsDurationRunUnbounded protects the legacy path:
// a server that advertises no byte ceiling must leave the run exactly as it
// was, bounded by duration only.
func TestServerWithoutByteLimitKeepsDurationRunUnbounded(t *testing.T) {
	// A server with no /capabilities at all is the v0.2.0 legacy case.
	httpServer := capabilityServer(t, capabilityOptions{noCapabilities: true})

	options := durationOnlyOptions(httpServer.URL)
	options.Phases = PhasesUpload
	options.Duration = 300 * time.Millisecond
	result, err := NewEngine(nil).Run(context.Background(), options)
	if err != nil {
		t.Fatalf("legacy upload returned error: %v (%s)", err, result.ErrorMessage)
	}
	if result.Upload.StopReason != StopReasonDuration {
		t.Fatalf("stop reason = %q, want %q", result.Upload.StopReason, StopReasonDuration)
	}
	if result.Upload.ServerConfirmedBytes <= 0 {
		t.Fatal("no bytes were transferred")
	}
}

// TestExplicitBudgetStillWins guards the existing contract: when the caller
// asked for fewer bytes than the server ceiling, the stop reason must stay
// requested_bytes_reached and no server limit warning may appear.
func TestExplicitBudgetStillWins(t *testing.T) {
	httpServer := limitedServer(t, 64<<20, 64<<20)

	options := durationOnlyOptions(httpServer.URL)
	options.Phases = PhasesUpload
	options.MaxBytes = 1 << 20
	result, err := NewEngine(nil).Run(context.Background(), options)
	if err != nil {
		t.Fatalf("upload returned error: %v (%s)", err, result.ErrorMessage)
	}
	if result.Upload.StopReason != StopReasonRequestedBytes {
		t.Fatalf("stop reason = %q, want %q", result.Upload.StopReason, StopReasonRequestedBytes)
	}
	if result.Upload.ServerConfirmedBytes != options.MaxBytes {
		t.Fatalf("confirmed %d bytes, want the requested %d", result.Upload.ServerConfirmedBytes, options.MaxBytes)
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "advertised") {
			t.Fatalf("unexpected server limit warning on a caller budget run: %q", warning)
		}
	}
}

// TestEffectiveByteCap pins the ceiling arithmetic: the server limit can only
// lower a caller budget, never raise it, and a caller budget always wins when
// it is the smaller of the two.
func TestEffectiveByteCap(t *testing.T) {
	tests := []struct {
		name      string
		requested int64
		serverCap int64
		want      int64
	}{
		{"no budget and no ceiling leaves the phase unbounded", 0, 0, 0},
		{"no budget takes the server ceiling", 0, 4096, 4096},
		{"a smaller caller budget wins", 1024, 4096, 1024},
		{"a larger caller budget is pulled down to the ceiling", 8192, 4096, 4096},
		{"an equal caller budget is unchanged", 4096, 4096, 4096},
		{"no ceiling leaves the caller budget alone", 1024, 0, 1024},
		{"negative inputs are treated as absent", -1, 4096, 4096},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := effectiveByteCap(test.requested, test.serverCap); got != test.want {
				t.Fatalf("effectiveByteCap(%d, %d) = %d, want %d",
					test.requested, test.serverCap, got, test.want)
			}
		})
	}
}

// TestByteCapCameFromServer proves the stop reason is only attributed to the
// server when the server ceiling is what actually ended the phase. Reporting
// "the server limit" for a run the user bounded itself would be a false
// statement about why the measurement is short.
func TestByteCapCameFromServer(t *testing.T) {
	tests := []struct {
		name        string
		requested   int64
		serverCap   int64
		transferred int64
		want        bool
	}{
		{"duration run that reached the ceiling", 0, 4096, 4096, true},
		{"caller budget below the ceiling is a request", 1024, 4096, 1024, false},
		{"caller budget equal to the ceiling is a request", 4096, 4096, 4096, false},
		{"stopping short of the ceiling", 0, 4096, 2048, false},
		{"no advertised ceiling", 0, 0, 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := byteCapCameFromServer(test.requested, test.serverCap, test.transferred)
			if got != test.want {
				t.Fatalf("byteCapCameFromServer(%d, %d, %d) = %v, want %v",
					test.requested, test.serverCap, test.transferred, got, test.want)
			}
		})
	}
}
