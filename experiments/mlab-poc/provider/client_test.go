package mlabpoc_test

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/experiments/mlab-poc/mock"
	mlabpoc "github.com/AbsoluteZero001/GoSpeed/experiments/mlab-poc/provider"
)

func agreedConsent() *mlabpoc.ConsentRecord {
	record := mlabpoc.NewConsentRecord("GoSpeed-p0j-test", "0.0.0-test")
	record.Agreed = true
	record.AgreedAt = time.Now().UTC()
	return &record
}

func newTestClient(t *testing.T, server *mock.Server, options mlabpoc.Options) *mlabpoc.Client {
	t.Helper()
	options.Server = server.Host()
	options.Scheme = "ws"
	options.Consent = agreedConsent()
	if options.OverallTimeout == 0 {
		options.OverallTimeout = 5 * time.Second
	}
	client, err := mlabpoc.NewClient(options)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

// --- P0-J-C: privacy consent -------------------------------------------------

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

	// An explicitly not-agreed record must behave the same.
	notAgreed := mlabpoc.NewConsentRecord("test", "0")
	if notAgreed.Agreed {
		t.Fatal("NewConsentRecord must default to not agreed")
	}
	client2, err := mlabpoc.NewClient(mlabpoc.Options{
		Server:  server.Host(),
		Scheme:  "ws",
		Consent: &notAgreed,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result2 := client2.Run(context.Background(), mlabpoc.DirectionDownload)
	if result2.Status != mlabpoc.StatusConsentNeeded {
		t.Fatalf("status = %q, want %q", result2.Status, mlabpoc.StatusConsentNeeded)
	}
	if snapshot := server.Snapshot(); snapshot.Requests != 0 {
		t.Fatalf("server received %d requests before consent", snapshot.Requests)
	}
}

func TestPromptConsentDefaultsToNo(t *testing.T) {
	testCases := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty input refuses", "\n", false},
		{"n refuses", "n\n", false},
		{"no refuses", "no\n", false},
		{"random text refuses", "ok\n", false},
		{"yes agrees", "yes\n", true},
		{"y agrees", "Y\n", true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			record, err := mlabpoc.PromptConsent(strings.NewReader(tc.input), &strings.Builder{})
			if err != nil {
				t.Fatalf("PromptConsent: %v", err)
			}
			if record.Agreed != tc.want {
				t.Fatalf("agreed = %v, want %v", record.Agreed, tc.want)
			}
			if tc.want && record.AgreedAt.IsZero() {
				t.Fatal("agreed record must carry a timestamp")
			}
			if !tc.want && !record.AgreedAt.IsZero() {
				t.Fatal("refused record must not carry a timestamp")
			}
			if record.PolicyVersion != mlabpoc.ConsentNoticeVersion {
				t.Fatalf("policy version = %q", record.PolicyVersion)
			}
		})
	}

	record, err := mlabpoc.PromptConsent(strings.NewReader(""), &strings.Builder{})
	if err != nil {
		t.Fatalf("PromptConsent EOF: %v", err)
	}
	if record.Agreed {
		t.Fatal("EOF must mean no consent")
	}
}

func TestConsentPageHasRequiredDisclosures(t *testing.T) {
	html, err := mlabpoc.RenderConsentHTML()
	if err != nil {
		t.Fatalf("RenderConsentHTML: %v", err)
	}
	required := []string{
		"measurementlab.net/privacy",
		"measurementlab.net/aup",
		"公网 IP",
		"长期保留",
		"M-Lab",
		"热点",
		"取消",
		`name="consent"`,
	}
	for _, fragment := range required {
		if !strings.Contains(html, fragment) {
			t.Fatalf("consent page missing required disclosure %q", fragment)
		}
	}
	// The checkbox must never be pre-checked.
	if strings.Contains(html, "checked") {
		t.Fatal("consent page must not contain a pre-checked checkbox")
	}
}

// --- basic protocol behaviour (carried over from P0-I) -----------------------

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
	if result.SocketBytesRead < result.TransferredBytes {
		t.Fatalf("wire bytes %d must bound app payload %d", result.SocketBytesRead, result.TransferredBytes)
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
	if result.SocketBytesWritten < result.TransferredBytes {
		t.Fatalf("wire bytes %d must bound app payload %d", result.SocketBytesWritten, result.TransferredBytes)
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
	if result.Quality.Trust != "insufficient" {
		t.Fatalf("quality trust = %q", result.Quality.Trust)
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

// --- P0-J-B: wire-level byte budgets -----------------------------------------

func TestDownloadBudgetExhaustionStopsConnection(t *testing.T) {
	server := mock.New(mock.ScenarioNormalDownload)
	defer server.Close()
	const budget = 32 * 1024
	client := newTestClient(t, server, mlabpoc.Options{
		DownloadBudgetBytes: budget,
	})

	result := client.Run(context.Background(), mlabpoc.DirectionDownload)
	if result.Status != mlabpoc.StatusBudgetExceeded {
		t.Fatalf("status = %q, error = %q", result.Status, result.Error)
	}
	if result.ErrorClass != mlabpoc.ErrorBudgetExceeded {
		t.Fatalf("error class = %q", result.ErrorClass)
	}
	if !result.BudgetExceeded {
		t.Fatal("BudgetExceeded flag not set")
	}
	if result.BudgetLimitBytes != budget {
		t.Fatalf("budget limit = %d, want %d", result.BudgetLimitBytes, budget)
	}
	if result.SocketBytesRead < budget || result.SocketBytesRead > budget+8*1024 {
		t.Fatalf("socket bytes read = %d, want [%d, %d]", result.SocketBytesRead, budget, budget+8*1024)
	}
	if result.TransferredBytes > result.SocketBytesRead {
		t.Fatalf("app payload %d exceeded wire bytes %d", result.TransferredBytes, result.SocketBytesRead)
	}
	if result.BudgetRemainingBytes == nil || *result.BudgetRemainingBytes != 0 {
		t.Fatalf("budget remaining = %v", result.BudgetRemainingBytes)
	}
	if result.Quality.Completeness != "partial" || result.Quality.Trust != "insufficient" {
		t.Fatalf("budget-aborted result must be marked partial/insufficient: %+v", result.Quality)
	}
	if result.Status == mlabpoc.StatusCompleted {
		t.Fatal("budget-aborted result must not be completed")
	}
	waitFor(t, 2*time.Second, func() bool {
		return server.Snapshot().Active == 0
	}, "connection was not force-closed after budget exhaustion")
}

func TestUploadBudgetExhaustionStopsSending(t *testing.T) {
	server := mock.New(mock.ScenarioNormalUpload)
	defer server.Close()
	const budget = 64 * 1024
	client := newTestClient(t, server, mlabpoc.Options{
		UploadBudgetBytes: budget,
	})

	result := client.Run(context.Background(), mlabpoc.DirectionUpload)
	if result.Status != mlabpoc.StatusBudgetExceeded {
		t.Fatalf("status = %q, error = %q", result.Status, result.Error)
	}
	if !result.BudgetExceeded {
		t.Fatal("BudgetExceeded flag not set")
	}
	// Writes are refused before they reach the network, so the wire total can
	// never exceed the budget.
	if result.SocketBytesWritten <= 0 || result.SocketBytesWritten > budget {
		t.Fatalf("socket bytes written = %d, want (0, %d]", result.SocketBytesWritten, budget)
	}
	if result.TransferredBytes > result.SocketBytesWritten {
		t.Fatalf("app payload %d exceeded wire bytes %d", result.TransferredBytes, result.SocketBytesWritten)
	}
	if result.Quality.Completeness != "partial" {
		t.Fatalf("quality = %+v", result.Quality)
	}
	waitFor(t, 2*time.Second, func() bool {
		return server.Snapshot().Active == 0
	}, "connection was not force-closed after budget exhaustion")
}

func TestPlanTotalBudgetStopsBeforeUpload(t *testing.T) {
	server := mock.NewPair(mock.ScenarioNormalDownload, mock.ScenarioNormalUpload)
	defer server.Close()
	const totalBudget = 32 * 1024
	client := newTestClient(t, server, mlabpoc.Options{})

	results := client.RunPlan(context.Background(), mlabpoc.Plan{
		Directions:       []mlabpoc.Direction{mlabpoc.DirectionDownload, mlabpoc.DirectionUpload},
		TotalBudgetBytes: totalBudget,
	})
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1 (upload must not start after budget exhaustion)", len(results))
	}
	download := results[0]
	if download.Status != mlabpoc.StatusBudgetExceeded {
		t.Fatalf("status = %q, want %q", download.Status, mlabpoc.StatusBudgetExceeded)
	}
	if download.BudgetLimitBytes != totalBudget {
		t.Fatalf("effective budget = %d, want %d", download.BudgetLimitBytes, totalBudget)
	}
	if snapshot := server.Snapshot(); snapshot.Connections != 1 {
		t.Fatalf("connections = %d, want 1", snapshot.Connections)
	}
}

// --- P0-J-A: cancellation mechanics ------------------------------------------

func TestBlockedReadCancelIsPrompt(t *testing.T) {
	server := mock.New(mock.ScenarioStall)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTestClient(t, server, mlabpoc.Options{})

	cancelAt := make(chan time.Time, 1)
	timer := time.AfterFunc(300*time.Millisecond, func() {
		cancel()
		cancelAt <- time.Now()
	})
	defer timer.Stop()

	start := time.Now()
	result := client.Run(ctx, mlabpoc.DirectionDownload)
	elapsed := time.Since(<-cancelAt)
	t.Logf("blocked-read cancel latency: %.3f ms (total run %s)", float64(elapsed.Nanoseconds())/1e6, time.Since(start))
	if result.Status != mlabpoc.StatusCancelled {
		t.Fatalf("status = %q, error = %q", result.Status, result.Error)
	}
	if result.ErrorClass != mlabpoc.ErrorCancelled {
		t.Fatalf("error class = %q", result.ErrorClass)
	}
	// Acceptance target: cancel ends the blocked run far below the 7 s SDK
	// I/O deadline. The ceiling is scaled under the race detector (see
	// timing_race_test.go); the status/error assertions above carry the
	// functional correctness either way.
	if elapsed > 500*time.Millisecond*time.Duration(timingAllowanceFactor) {
		t.Fatalf("blocked-read cancel took %s, acceptance target is %d ms",
			elapsed, 500*timingAllowanceFactor)
	}
	waitFor(t, 2*time.Second, func() bool {
		return server.Snapshot().Active == 0
	}, "connection remained open after cancellation")
}

func TestBlockedWriteCancelIsPrompt(t *testing.T) {
	server := mock.New(mock.ScenarioUploadStall)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTestClient(t, server, mlabpoc.Options{
		OverallTimeout: 8 * time.Second,
	})

	cancelAt := make(chan time.Time, 1)
	timer := time.AfterFunc(1500*time.Millisecond, func() {
		cancel()
		cancelAt <- time.Now()
	})
	defer timer.Stop()

	result := client.Run(ctx, mlabpoc.DirectionUpload)
	elapsed := time.Since(<-cancelAt)
	t.Logf("upload cancel latency: %.3f ms", float64(elapsed.Nanoseconds())/1e6)
	if result.Status != mlabpoc.StatusCancelled {
		t.Fatalf("status = %q, error = %q", result.Status, result.Error)
	}
	if elapsed > 1500*time.Millisecond*time.Duration(timingAllowanceFactor) {
		t.Fatalf("upload cancel took %s", elapsed)
	}
	waitFor(t, 2*time.Second, func() bool {
		return server.Snapshot().Active == 0
	}, "connection remained open after cancellation")
}

func TestOverallTimeoutStopsPromptly(t *testing.T) {
	server := mock.New(mock.ScenarioStall)
	defer server.Close()
	client := newTestClient(t, server, mlabpoc.Options{
		OverallTimeout: 500 * time.Millisecond,
	})

	start := time.Now()
	result := client.Run(context.Background(), mlabpoc.DirectionDownload)
	elapsed := time.Since(start)
	t.Logf("overall timeout run ended after %s, cancel latency %d ms",
		elapsed, *result.CancellationLatencyMs)
	if result.Status != mlabpoc.StatusTimeout {
		t.Fatalf("status = %q, error = %q", result.Status, result.Error)
	}
	if result.ErrorClass != mlabpoc.ErrorTimeout {
		t.Fatalf("error class = %q, want %q", result.ErrorClass, mlabpoc.ErrorTimeout)
	}
	if result.CancellationLatencyMs == nil {
		t.Fatal("timeout cancellation latency was not recorded")
	}
	// P0-I needed ~6.5 s (SDK I/O deadline); with force-close the 500 ms
	// timeout must end promptly. The ceiling is scaled under the race
	// detector; the status/error assertions carry functional correctness.
	if elapsed > 2*time.Second*time.Duration(timingAllowanceFactor) {
		t.Fatalf("overall timeout run took %s", elapsed)
	}
	waitFor(t, 2*time.Second, func() bool {
		return server.Snapshot().Active == 0
	}, "connection remained open after timeout")
}

func TestClientCloseCancelsActiveRun(t *testing.T) {
	server := mock.New(mock.ScenarioNormalDownload)
	defer server.Close()
	var client *mlabpoc.Client
	var err error
	client, err = mlabpoc.NewClient(mlabpoc.Options{
		Server:         server.Host(),
		Scheme:         "ws",
		Consent:        agreedConsent(),
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
	waitFor(t, 2*time.Second, func() bool {
		return server.Snapshot().Active == 0
	}, "active connection remained after Close")
}

// --- plan sequencing ----------------------------------------------------------

func TestPlanBothDirectionsComplete(t *testing.T) {
	server := mock.NewPair(mock.ScenarioNormalDownload, mock.ScenarioNormalUpload)
	defer server.Close()
	client := newTestClient(t, server, mlabpoc.Options{})

	results := client.RunPlan(context.Background(), mlabpoc.Plan{
		Directions: []mlabpoc.Direction{mlabpoc.DirectionDownload, mlabpoc.DirectionUpload},
	})
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if results[0].Status != mlabpoc.StatusCompleted || results[1].Status != mlabpoc.StatusCompleted {
		t.Fatalf("statuses = %q, %q", results[0].Status, results[1].Status)
	}
}

func TestPlanDoesNotStartNextDirectionAfterCancel(t *testing.T) {
	server := mock.NewPair(mock.ScenarioNormalDownload, mock.ScenarioNormalUpload)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newTestClient(t, server, mlabpoc.Options{
		OnProgress: func(progress mlabpoc.Progress) {
			if progress.ApplicationBytes > 0 {
				cancel()
			}
		},
	})

	results := client.RunPlan(ctx, mlabpoc.Plan{
		Directions: []mlabpoc.Direction{mlabpoc.DirectionDownload, mlabpoc.DirectionUpload},
	})
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1 (cancelled run must not start upload)", len(results))
	}
	if results[0].Status != mlabpoc.StatusCancelled {
		t.Fatalf("status = %q", results[0].Status)
	}
	if snapshot := server.Snapshot(); snapshot.Connections != 1 {
		t.Fatalf("connections = %d, want 1 (no second direction may connect)", snapshot.Connections)
	}
}

// --- resource hygiene ---------------------------------------------------------

func TestGoroutinesDoNotLeak(t *testing.T) {
	baseline := runtime.NumGoroutine()

	scenarios := []struct {
		name      string
		download  mock.Scenario
		upload    mock.Scenario
		options   mlabpoc.Options
		direction mlabpoc.Direction
	}{
		{
			name:      "blocked read cancel",
			download:  mock.ScenarioStall,
			upload:    mock.ScenarioNormalUpload,
			direction: mlabpoc.DirectionDownload,
			options:   mlabpoc.Options{},
		},
		{
			name:      "download budget",
			download:  mock.ScenarioNormalDownload,
			upload:    mock.ScenarioNormalUpload,
			direction: mlabpoc.DirectionDownload,
			options:   mlabpoc.Options{DownloadBudgetBytes: 32 * 1024},
		},
		{
			name:      "upload budget",
			download:  mock.ScenarioNormalDownload,
			upload:    mock.ScenarioNormalUpload,
			direction: mlabpoc.DirectionUpload,
			options:   mlabpoc.Options{UploadBudgetBytes: 64 * 1024},
		},
		{
			name:      "handshake failure",
			download:  mock.ScenarioHandshakeFailure,
			upload:    mock.ScenarioHandshakeFailure,
			direction: mlabpoc.DirectionDownload,
			options:   mlabpoc.Options{},
		},
		{
			name:      "overall timeout",
			download:  mock.ScenarioStall,
			upload:    mock.ScenarioNormalUpload,
			direction: mlabpoc.DirectionDownload,
			options:   mlabpoc.Options{OverallTimeout: 400 * time.Millisecond},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			server := mock.NewPair(scenario.download, scenario.upload)
			defer server.Close()
			client := newTestClient(t, server, scenario.options)
			client.Run(context.Background(), scenario.direction)
		})
	}

	// Give runtime and httptest cleanup time to settle, then require the
	// goroutine count to return to the baseline.
	waitFor(t, 5*time.Second, func() bool {
		return runtime.NumGoroutine() <= baseline
	}, "goroutine leak detected")
}
