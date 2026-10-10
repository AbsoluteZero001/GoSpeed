package mlabpoc_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/m-lab/locate/api/v2"

	"github.com/AbsoluteZero001/GoSpeed/experiments/mlab-poc/mock"
	mlabpoc "github.com/AbsoluteZero001/GoSpeed/experiments/mlab-poc/provider"
)

// localLocateOptions returns LocateOptions suitable for a localhost mock:
// ws scheme and 127.0.0.1 host allow lists instead of the strict production
// wss + M-Lab hostname suffix defaults.
func localLocateOptions(baseURL string, schemes ...string) mlabpoc.LocateOptions {
	if len(schemes) == 0 {
		schemes = []string{"ws"}
	}
	return mlabpoc.LocateOptions{
		BaseURL:         baseURL,
		SchemeAllowList: schemes,
		HostSuffixes:    []string{"127.0.0.1", "localhost"},
	}
}

// --- P0-K-A: the ten Locate verification scenarios ---------------------------

func TestLocateScenario1NormalDiscovery(t *testing.T) {
	locateMock := mock.NewLocate(mock.LocateNormal, "ws", "127.0.0.1:1")
	defer locateMock.Close()
	record := agreedConsent()

	targets, err := mlabpoc.DiscoverServers(context.Background(), record, localLocateOptions(locateMock.BaseURL()))
	if err != nil {
		t.Fatalf("DiscoverServers: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(targets))
	}
	downloadURL, err := mlabpoc.TargetServiceURL(&targets[0], "ws", mlabpoc.DirectionDownload)
	if err != nil {
		t.Fatalf("TargetServiceURL download: %v", err)
	}
	if !strings.HasPrefix(downloadURL, "ws://127.0.0.1:1/ndt/v7/download") {
		t.Fatalf("download URL = %q", downloadURL)
	}
	if _, err := mlabpoc.TargetServiceURL(&targets[0], "ws", mlabpoc.DirectionUpload); err != nil {
		t.Fatalf("TargetServiceURL upload: %v", err)
	}
}

func TestLocateScenario2ConnectionFailure(t *testing.T) {
	// Connection failure without any server: port 1 on loopback is refused.
	record := agreedConsent()
	_, err := mlabpoc.DiscoverServers(context.Background(), record, mlabpoc.LocateOptions{
		BaseURL:         "http://127.0.0.1:1/v2/nearest/",
		SchemeAllowList: []string{"ws"},
		HostSuffixes:    []string{"127.0.0.1"},
	})
	var classified *mlabpoc.ClassifiedError
	if !errors.As(err, &classified) || classified.Class != mlabpoc.ErrorNetwork {
		t.Fatalf("err = %v, want network class", err)
	}
}

// failingDNSRoundTripper simulates a DNS resolution failure without any
// network activity.
type failingDNSRoundTripper struct{}

func (failingDNSRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, &net.DNSError{Err: "no such host", Name: "locate.measurementlab.net", IsNotFound: true}
}

func TestLocateScenario2DNSFailure(t *testing.T) {
	record := agreedConsent()
	_, err := mlabpoc.DiscoverServers(context.Background(), record, mlabpoc.LocateOptions{
		BaseURL: "https://locate.measurementlab.net/v2/nearest/",
		HTTPClient: &http.Client{
			Transport: failingDNSRoundTripper{},
		},
		SchemeAllowList: []string{"ws"},
		HostSuffixes:    []string{"127.0.0.1"},
	})
	var classified *mlabpoc.ClassifiedError
	if !errors.As(err, &classified) || classified.Class != mlabpoc.ErrorNetwork {
		t.Fatalf("err = %v, want network class", err)
	}
}

func TestLocateScenario3HTTPTimeout(t *testing.T) {
	locateMock := mock.NewLocate(mock.LocateSlowHeaders, "ws", "127.0.0.1:1")
	defer locateMock.Close()
	record := agreedConsent()

	start := time.Now()
	opts := localLocateOptions(locateMock.BaseURL())
	opts.Timeout = 300 * time.Millisecond
	_, err := mlabpoc.DiscoverServers(context.Background(), record, opts)
	elapsed := time.Since(start)
	var classified *mlabpoc.ClassifiedError
	if !errors.As(err, &classified) || classified.Class != mlabpoc.ErrorTimeout {
		t.Fatalf("err = %v, want timeout class", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("discovery took %v; HTTP timeout was not applied", elapsed)
	}
}

func TestLocateScenario4BodyStallCancelReleasesResources(t *testing.T) {
	locateMock := mock.NewLocate(mock.LocateBodyStall, "ws", "127.0.0.1:1")
	defer locateMock.Close()
	record := agreedConsent()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	opts := localLocateOptions(locateMock.BaseURL())
	opts.Timeout = 10 * time.Second // body has no deadline; only the cancel
	start := time.Now()
	_, err := mlabpoc.DiscoverServers(ctx, record, opts)
	elapsed := time.Since(start)
	var classified *mlabpoc.ClassifiedError
	if !errors.As(err, &classified) || classified.Class != mlabpoc.ErrorCancelled {
		t.Fatalf("err = %v, want cancelled class", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("cancel took %v to unblock the stalled body read", elapsed)
	}
	// The mock handler must observe the disconnect and release the request.
	waitFor(t, 3*time.Second, func() bool {
		return locateMock.Snapshot().Active == 0
	}, "locate mock handler did not observe the cancelled request")
}

func TestLocateScenario5UserCancelNeverReachesNDT7(t *testing.T) {
	locateMock := mock.NewLocate(mock.LocateSlowHeaders, "ws", "127.0.0.1:1")
	defer locateMock.Close()
	ndtMock := mock.New(mock.ScenarioNormalDownload)
	defer ndtMock.Close()
	record := agreedConsent()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	client, err := mlabpoc.NewClient(mlabpoc.Options{
		Scheme:  "ws",
		Consent: record,
		Discover: func(ctx context.Context) ([]v2.Target, error) {
			return mlabpoc.DiscoverServers(ctx, record, localLocateOptions(locateMock.BaseURL()))
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	results := client.RunPlan(ctx, mlabpoc.Plan{Directions: []mlabpoc.Direction{mlabpoc.DirectionDownload}})
	if len(results) != 1 || results[0].ErrorClass != mlabpoc.ErrorCancelled {
		t.Fatalf("results = %+v, want cancelled discovery", results)
	}
	if snapshot := ndtMock.Snapshot(); snapshot.Requests != 0 {
		t.Fatalf("NDT7 mock received %d requests after cancelled discovery", snapshot.Requests)
	}
	if snapshot := locateMock.Snapshot(); snapshot.Requests == 0 {
		t.Fatal("locate mock was never contacted; test setup broken")
	}
}

func TestLocateScenario6EmptyLists(t *testing.T) {
	record := agreedConsent()

	// "results": null -> official ErrNoAvailableServers.
	locateMock := mock.NewLocate(mock.LocateEmptyResults, "ws", "127.0.0.1:1")
	_, err := mlabpoc.DiscoverServers(context.Background(), record, localLocateOptions(locateMock.BaseURL()))
	locateMock.Close()
	var classified *mlabpoc.ClassifiedError
	if !errors.As(err, &classified) || classified.Class != mlabpoc.ErrorNoServer {
		t.Fatalf("null results: err = %v, want no_server class", err)
	}

	// "results": [] -> the official client returns NO error (audited edge);
	// DiscoverServers must still refuse to continue.
	locateMock2 := mock.NewLocate(mock.LocateEmptyArray, "ws", "127.0.0.1:1")
	defer locateMock2.Close()
	_, err = mlabpoc.DiscoverServers(context.Background(), record, localLocateOptions(locateMock2.BaseURL()))
	if !errors.As(err, &classified) || classified.Class != mlabpoc.ErrorNoServer {
		t.Fatalf("empty array: err = %v, want no_server class", err)
	}
}

func TestLocateScenario7InvalidTargetRejected(t *testing.T) {
	locateMock := mock.NewLocate(mock.LocateInvalidTarget, "ws", "127.0.0.1:1")
	defer locateMock.Close()
	ndtMock := mock.New(mock.ScenarioNormalDownload)
	defer ndtMock.Close()
	record := agreedConsent()

	client, err := mlabpoc.NewClient(mlabpoc.Options{
		Scheme:  "ws",
		Consent: record,
		Discover: func(ctx context.Context) ([]v2.Target, error) {
			return mlabpoc.DiscoverServers(ctx, record, localLocateOptions(locateMock.BaseURL()))
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	results := client.RunPlan(context.Background(), mlabpoc.Plan{Directions: []mlabpoc.Direction{mlabpoc.DirectionDownload}})
	if len(results) != 1 || results[0].ErrorClass != mlabpoc.ErrorServerInvalid {
		t.Fatalf("results = %+v, want server_invalid", results)
	}
	if snapshot := ndtMock.Snapshot(); snapshot.Requests != 0 {
		t.Fatalf("NDT7 mock received %d requests for an invalid target", snapshot.Requests)
	}
}

func TestLocateScenario8InsecureURLRejected(t *testing.T) {
	locateMock := mock.NewLocate(mock.LocateInsecureURL, "ws", "127.0.0.1:1")
	defer locateMock.Close()
	record := agreedConsent()

	// Strict production shape: the keys are wss but the URLs are http://.
	opts := localLocateOptions(locateMock.BaseURL(), "wss")
	_, err := mlabpoc.DiscoverServers(context.Background(), record, opts)
	var classified *mlabpoc.ClassifiedError
	if !errors.As(err, &classified) || classified.Class != mlabpoc.ErrorServerInvalid {
		t.Fatalf("err = %v, want server_invalid for insecure URL", err)
	}
}

func TestLocateScenario9HTTP429(t *testing.T) {
	locateMock := mock.NewLocate(mock.LocateHTTP429, "ws", "127.0.0.1:1")
	defer locateMock.Close()
	record := agreedConsent()

	_, err := mlabpoc.DiscoverServers(context.Background(), record, localLocateOptions(locateMock.BaseURL()))
	if err == nil {
		t.Fatal("429 must surface an error")
	}
	var classified *mlabpoc.ClassifiedError
	if !errors.As(err, &classified) || classified.Class != mlabpoc.ErrorUnknown {
		t.Fatalf("err = %v, want unknown class (documented mapping for non-200 problem documents)", err)
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("error should carry the problem detail: %v", err)
	}
}

func TestLocateScenario10HTTP500(t *testing.T) {
	locateMock := mock.NewLocate(mock.LocateHTTP500, "ws", "127.0.0.1:1")
	defer locateMock.Close()
	record := agreedConsent()

	_, err := mlabpoc.DiscoverServers(context.Background(), record, localLocateOptions(locateMock.BaseURL()))
	if err == nil {
		t.Fatal("500 must surface an error")
	}
	var classified *mlabpoc.ClassifiedError
	if !errors.As(err, &classified) || classified.Class != mlabpoc.ErrorUnknown {
		t.Fatalf("err = %v, want unknown class (JSON decode failure on a non-JSON body)", err)
	}
}

// --- P0-K-B: consent gates discovery as well ---------------------------------

func TestDiscoverRequiresConsent(t *testing.T) {
	locateMock := mock.NewLocate(mock.LocateNormal, "ws", "127.0.0.1:1")
	defer locateMock.Close()

	if _, err := mlabpoc.DiscoverServers(context.Background(), nil, localLocateOptions(locateMock.BaseURL())); err == nil {
		t.Fatal("discovery without a consent record must fail")
	} else {
		var classified *mlabpoc.ClassifiedError
		if !errors.As(err, &classified) || classified.Class != mlabpoc.ErrorConsent {
			t.Fatalf("err = %v, want consent class", err)
		}
	}
	notAgreed := mlabpoc.NewConsentRecord("test", "0")
	if _, err := mlabpoc.DiscoverServers(context.Background(), &notAgreed, localLocateOptions(locateMock.BaseURL())); err == nil {
		t.Fatal("discovery with a not-agreed record must fail")
	}
	if snapshot := locateMock.Snapshot(); snapshot.Requests != 0 {
		t.Fatalf("locate mock received %d requests without consent", snapshot.Requests)
	}
}

func TestRunPlanDoesNotDiscoverWithoutConsent(t *testing.T) {
	locateMock := mock.NewLocate(mock.LocateNormal, "ws", "127.0.0.1:1")
	defer locateMock.Close()
	ndtMock := mock.New(mock.ScenarioNormalDownload)
	defer ndtMock.Close()

	client, err := mlabpoc.NewClient(mlabpoc.Options{
		Scheme: "ws",
		Discover: func(ctx context.Context) ([]v2.Target, error) {
			return mlabpoc.DiscoverServers(ctx, nil, localLocateOptions(locateMock.BaseURL()))
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	results := client.RunPlan(context.Background(), mlabpoc.Plan{Directions: []mlabpoc.Direction{mlabpoc.DirectionDownload}})
	if len(results) != 1 || results[0].Status != mlabpoc.StatusConsentNeeded {
		t.Fatalf("results = %+v, want consent_required", results)
	}
	if snapshot := locateMock.Snapshot(); snapshot.Requests != 0 {
		t.Fatalf("locate mock received %d requests without consent", snapshot.Requests)
	}
	if snapshot := ndtMock.Snapshot(); snapshot.Requests != 0 {
		t.Fatalf("NDT7 mock received %d requests without consent", snapshot.Requests)
	}
}

func TestConsentNoticeV2DisclosesLocateAndBudget(t *testing.T) {
	notice := mlabpoc.DefaultConsentNotice()
	for _, fragment := range []string{"Locate", "24 MiB", "16 MiB", "40 MiB", "budget_exceeded", "30s", "measurementlab.net/privacy"} {
		if !strings.Contains(notice, fragment) {
			t.Fatalf("consent notice missing %q", fragment)
		}
	}
	html, err := mlabpoc.RenderConsentHTML()
	if err != nil {
		t.Fatalf("RenderConsentHTML: %v", err)
	}
	for _, fragment := range []string{"Locate", "24 MiB", "16 MiB", "40 MiB", "budget_exceeded", "30s", "measurementlab.net/privacy", "measurementlab.net/aup"} {
		if !strings.Contains(html, fragment) {
			t.Fatalf("consent page missing %q", fragment)
		}
	}
	if strings.Contains(html, "checked") {
		t.Fatal("consent page must not contain a pre-checked checkbox")
	}
}

// --- discovery feeds the NDT7 run --------------------------------------------

func TestDiscoveryFeedsNDT7Run(t *testing.T) {
	ndtMock := mock.NewPair(mock.ScenarioNormalDownload, mock.ScenarioNormalUpload)
	defer ndtMock.Close()
	locateMock := mock.NewLocate(mock.LocateNormal, "ws", ndtMock.Host())
	defer locateMock.Close()
	record := agreedConsent()

	client, err := mlabpoc.NewClient(mlabpoc.Options{
		Scheme:         "ws",
		Consent:        record,
		OverallTimeout: 10 * time.Second,
		Discover: func(ctx context.Context) ([]v2.Target, error) {
			return mlabpoc.DiscoverServers(ctx, record, localLocateOptions(locateMock.BaseURL()))
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	results := client.RunPlan(context.Background(), mlabpoc.Plan{
		Directions: []mlabpoc.Direction{mlabpoc.DirectionDownload, mlabpoc.DirectionUpload},
	})
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	for i, result := range results {
		if result.Status != mlabpoc.StatusCompleted {
			t.Fatalf("direction %d: status = %q, error = %q", i, result.Status, result.Error)
		}
		if result.ServerName != "127.0.0.1" {
			t.Fatalf("direction %d: server = %q", i, result.ServerName)
		}
	}
	if snapshot := ndtMock.Snapshot(); snapshot.Connections < 2 {
		t.Fatalf("NDT7 mock connections = %d, want >= 2", snapshot.Connections)
	}
}

// --- SDK-initiated Locate must stay disabled ---------------------------------

func TestSDKInitiatedLocateIsDisabled(t *testing.T) {
	server := mock.New(mock.ScenarioNormalDownload)
	defer server.Close()
	// No Server, no ServiceURL, no Discover: the official SDK would call its
	// built-in Locate here. The refusing locator must stop that.
	client, err := mlabpoc.NewClient(mlabpoc.Options{
		Scheme:  "ws",
		Consent: agreedConsent(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result := client.Run(context.Background(), mlabpoc.DirectionDownload)
	if result.Status != mlabpoc.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	if !strings.Contains(result.Error, "Locate is disabled") {
		t.Fatalf("error = %q", result.Error)
	}
	if snapshot := server.Snapshot(); snapshot.Requests != 0 {
		t.Fatalf("server received %d requests; SDK Locate path must never dial", snapshot.Requests)
	}
}
