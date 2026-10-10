package mlabpoc_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/experiments/mlab-poc/mock"
	mlabpoc "github.com/AbsoluteZero001/GoSpeed/experiments/mlab-poc/provider"
)

func newTestClient(t *testing.T, server *mock.Server, options mlabpoc.Options) *mlabpoc.Client {
	t.Helper()
	options.Server = server.Host()
	options.Scheme = "ws"
	options.PrivacyConsent = true
	if options.OverallTimeout == 0 {
		options.OverallTimeout = 5 * time.Second
	}
	client, err := mlabpoc.NewClient(options)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestConsentIsRequiredBeforeAnyRun(t *testing.T) {
	server := mock.New(mock.ScenarioNormalDownload)
	defer server.Close()
	client, err := mlabpoc.NewClient(mlabpoc.Options{
		Server: server.Host(),
		Scheme: "ws",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	result := client.Run(context.Background(), mlabpoc.DirectionDownload)
	if result.Status != mlabpoc.StatusConsentNeeded {
		t.Fatalf("status = %q, want %q", result.Status, mlabpoc.StatusConsentNeeded)
	}
	if result.ErrorClass != mlabpoc.ErrorConsent {
		t.Fatalf("error class = %q, want %q", result.ErrorClass, mlabpoc.ErrorConsent)
	}
	if snapshot := server.Snapshot(); snapshot.Requests != 0 {
		t.Fatalf("server received %d requests before consent", snapshot.Requests)
	}
}

func TestNormalDownloadMapping(t *testing.T) {
	server := mock.New(mock.ScenarioNormalDownload)
	defer server.Close()
	client := newTestClient(t, server, mlabpoc.Options{})

	result := client.Run(context.Background(), mlabpoc.DirectionDownload)
	if result.Status != mlabpoc.StatusCompleted {
		t.Fatalf("status = %q, error = %q", result.Status, result.Error)
	}
	if result.DownloadGoodputMbps == nil || *result.DownloadGoodputMbps <= 0 {
		t.Fatalf("download goodput = %v", result.DownloadGoodputMbps)
	}
	if result.TCPMinRTTMs == nil || *result.TCPMinRTTMs != 12 {
		t.Fatalf("TCP MinRTT = %v, want 12 ms", result.TCPMinRTTMs)
	}
	if result.TCPRTTVarMs == nil || *result.TCPRTTVarMs != 1.5 {
		t.Fatalf("TCP RTTVar = %v, want 1.5 ms", result.TCPRTTVarMs)
	}
	if result.JitterMs != nil {
		t.Fatalf("Jitter must remain nil, got %v", *result.JitterMs)
	}
	if result.TransferredBytes == 0 {
		t.Fatal("transferred bytes was not reported")
	}
	if !strings.Contains(result.Quality.Completeness, "complete") {
		t.Fatalf("quality = %+v", result.Quality)
	}
}

func TestNormalUploadMapping(t *testing.T) {
	server := mock.New(mock.ScenarioNormalUpload)
	defer server.Close()
	client := newTestClient(t, server, mlabpoc.Options{})

	result := client.Run(context.Background(), mlabpoc.DirectionUpload)
	if result.Status != mlabpoc.StatusCompleted {
		t.Fatalf("status = %q, error = %q", result.Status, result.Error)
	}
	if result.UploadGoodputMbps == nil || *result.UploadGoodputMbps <= 0 {
		t.Fatalf("upload goodput = %v", result.UploadGoodputMbps)
	}
	if result.TCPMinRTTMs == nil {
		t.Fatal("TCP MinRTT was not mapped")
	}
	if result.JitterMs != nil {
		t.Fatalf("Jitter must remain nil, got %v", *result.JitterMs)
	}
	if result.TransferredBytes < 512*1024 {
		t.Fatalf("transferred bytes = %d", result.TransferredBytes)
	}
}

func TestHandshakeFailureIsClassified(t *testing.T) {
	server := mock.New(mock.ScenarioHandshakeFailure)
	defer server.Close()
	client := newTestClient(t, server, mlabpoc.Options{})

	result := client.Run(context.Background(), mlabpoc.DirectionDownload)
	if result.Status != mlabpoc.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	if result.ErrorClass != mlabpoc.ErrorHandshake {
		t.Fatalf("error class = %q, want %q", result.ErrorClass, mlabpoc.ErrorHandshake)
	}
}

func TestEarlyCloseIsPartial(t *testing.T) {
	server := mock.New(mock.ScenarioEarlyClose)
	defer server.Close()
	client := newTestClient(t, server, mlabpoc.Options{})

	result := client.Run(context.Background(), mlabpoc.DirectionDownload)
	if result.Status != mlabpoc.StatusPartial {
		t.Fatalf("status = %q, want %q", result.Status, mlabpoc.StatusPartial)
	}
	if result.Quality.Completeness != "partial" {
		t.Fatalf("quality = %+v", result.Quality)
	}
}

func TestNetworkDisconnectDoesNotLookCompleted(t *testing.T) {
	server := mock.New(mock.ScenarioNetworkDisconnect)
	defer server.Close()
	client := newTestClient(t, server, mlabpoc.Options{})

	result := client.Run(context.Background(), mlabpoc.DirectionDownload)
	if result.Status == mlabpoc.StatusCompleted {
		t.Fatalf("disconnect was incorrectly marked completed: %+v", result)
	}
}

func TestSoftByteBudgetTriggersWithoutClaimingHardLimit(t *testing.T) {
	server := mock.New(mock.ScenarioNormalDownload)
	defer server.Close()
	client := newTestClient(t, server, mlabpoc.Options{SoftByteBudget: 32 * 1024})

	result := client.Run(context.Background(), mlabpoc.DirectionDownload)
	if !result.BudgetExceeded {
		t.Fatalf("budget was not reported as exceeded: %+v", result)
	}
	if result.Status != mlabpoc.StatusBudgetExceeded {
		t.Fatalf("status = %q, want %q", result.Status, mlabpoc.StatusBudgetExceeded)
	}
	if result.TransferredBytes < 32*1024 {
		t.Fatalf("transferred bytes = %d, budget = %d", result.TransferredBytes, 32*1024)
	}
}

func TestContextCancellationDuringActiveStream(t *testing.T) {
	server := mock.New(mock.ScenarioNormalDownload)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	client := newTestClient(t, server, mlabpoc.Options{
		OnProgress: func(progress mlabpoc.Progress) {
			if progress.ApplicationBytes > 0 {
				cancel()
			}
		},
	})

	result := client.Run(ctx, mlabpoc.DirectionDownload)
	elapsed := time.Since(start)
	t.Logf("context cancellation elapsed: %s", elapsed)
	if result.Status != mlabpoc.StatusCancelled {
		t.Fatalf("status = %q, error = %q", result.Status, result.Error)
	}
	if elapsed > 8*time.Second {
		t.Fatalf("cancellation took %s, longer than SDK I/O bound", elapsed)
	}
	time.Sleep(100 * time.Millisecond)
	before := server.Snapshot()
	time.Sleep(300 * time.Millisecond)
	after := server.Snapshot()
	if after.Connections != before.Connections {
		t.Fatalf("new connection after cancel: before=%+v after=%+v", before, after)
	}
}

func TestClientCloseCancelsActiveRun(t *testing.T) {
	server := mock.New(mock.ScenarioNormalDownload)
	defer server.Close()
	var client *mlabpoc.Client
	var err error
	client, err = mlabpoc.NewClient(mlabpoc.Options{
		Server:         server.Host(),
		Scheme:         "ws",
		PrivacyConsent: true,
		OverallTimeout: 5 * time.Second,
		OnProgress: func(progress mlabpoc.Progress) {
			if progress.ApplicationBytes > 0 {
				client.Close()
			}
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	result := client.Run(context.Background(), mlabpoc.DirectionDownload)
	if result.Status != mlabpoc.StatusCancelled {
		t.Fatalf("status = %q, error = %q", result.Status, result.Error)
	}
	time.Sleep(100 * time.Millisecond)
	if snapshot := server.Snapshot(); snapshot.Active != 0 {
		t.Fatalf("active connection remained after Close: %+v", snapshot)
	}
}

func TestOverallTimeoutIsReported(t *testing.T) {
	server := mock.New(mock.ScenarioStall)
	defer server.Close()
	client := newTestClient(t, server, mlabpoc.Options{
		OverallTimeout: 500 * time.Millisecond,
	})

	result := client.Run(context.Background(), mlabpoc.DirectionDownload)
	if result.Status != mlabpoc.StatusTimeout {
		t.Fatalf("status = %q, error = %q", result.Status, result.Error)
	}
	if result.ErrorClass != mlabpoc.ErrorTimeout {
		t.Fatalf("error class = %q, want %q", result.ErrorClass, mlabpoc.ErrorTimeout)
	}
	if result.CancellationLatencyMs == nil {
		t.Fatal("timeout cancellation latency was not recorded")
	}
	t.Logf("overall timeout cancellation latency: %d ms", *result.CancellationLatencyMs)
}
