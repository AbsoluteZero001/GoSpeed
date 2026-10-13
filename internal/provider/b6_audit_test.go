package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// B6 sensitive data audit.
//
// The question is not "is the happy path clean" but "can a secret that is
// present in a node address, a server response or an error string reach the
// frontend through any published payload".
//
// Defence is deliberately layered, and this file tests every layer:
//
//	L1 configuration  internal/nodes refuses userinfo, query and fragment
//	L2 provider output the native detail payload is stripped before publishing
//	L3 transport       Go's net/http replaces a password with *** in errors
//
// Every case below uses a fake secret, never a real credential.

// auditSecrets are the literal markers searched for. They are fictional.
var auditSecrets = []string{
	"s3cr3tPassw0rd",
	"glpat-FakeToken123",
	"access_token=fake-access-9f2a",
	"token=fake-token-7b31",
	"api_key=fake-key-4c88",
	"fake-session-cookie",
	"fake-bearer-eyJhbGciOi",
}

// containsAnySecret reports whether any audit marker appears in the text.
func containsAnySecret(text string) bool {
	for _, secret := range auditSecrets {
		if strings.Contains(text, secret) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// L1: the configuration layer refuses every credential carrier
//
// The rule itself is owned by internal/nodes and is covered there
// (nodes_test.go: "credentials in url", "query in url"). It is repeated in the
// provider's own table only to record why this package can rely on it:
//
//	internal/nodes rejects parsed.User   (userinfo: user:password@)
//	internal/nodes rejects parsed.RawQuery (every query string: ?token=,
//	                                       ?access_token=, ?api_key=)
//	internal/nodes rejects parsed.Fragment
//
// A GoSpeed node therefore cannot be configured with a credential carrier at
// all. This package still redacts, because provider.Node is a plain struct with
// no validation and a future caller may build one from a source that has not
// passed through internal/nodes.

// ---------------------------------------------------------------------------
// L2: the provider must strip before publishing
// ---------------------------------------------------------------------------

// TestAuditProviderStripsUserinfoFromPublishedDetail is the L2 rule.
func TestAuditProviderStripsUserinfoFromPublishedDetail(t *testing.T) {
	testServer := realGoSpeedServer(t)
	endpoint := strings.Replace(testServer.URL, "://", "://user:s3cr3tPassw0rd@", 1)

	result := runAuditRun(t, endpoint, 512<<10)

	if result.Detail == nil {
		t.Fatal("no native detail to audit")
	}
	payload := string(result.Detail.Payload)
	if containsAnySecret(payload) {
		t.Fatalf("the published detail leaked a secret:\n%s", payload)
	}
	if strings.Contains(payload, "@") {
		t.Fatalf("the published detail kept a userinfo component:\n%s", payload)
	}
	// The address itself must survive so a consumer can still identify the node.
	if !strings.Contains(payload, "127.0.0.1") {
		t.Fatalf("redaction removed too much; the node address is gone:\n%s", payload)
	}
}

// TestAuditPathSegmentIsReportedVerbatim documents the deliberate boundary: a
// node may legitimately live under a path prefix, and that path is reported as
// configured. Only credentials are stripped.
func TestAuditPathSegmentIsReportedVerbatim(t *testing.T) {
	redacted, changed := redactURLCredentials("https://speed.example.com/prefix/path")
	if changed {
		t.Error("a clean address must not be altered")
	}
	if redacted != "https://speed.example.com/prefix/path" {
		t.Errorf("address = %q, want it unchanged", redacted)
	}
}

// TestAuditQueryCredentialsAreStrippedFromPublishedDetail closes the gap that
// internal/nodes closes by refusing query strings.
//
// provider.Node is a plain struct with no validation, so a caller that builds
// one from a source other than the node file (a discovery service, a future
// provider, a script) can carry ?token=, ?access_token= or ?api_key= straight
// into a published result. The configuration layer cannot help there, so the
// provider must strip these too.
func TestAuditQueryCredentialsAreStrippedFromPublishedDetail(t *testing.T) {
	cases := []struct {
		name   string
		suffix string
		wantGo string
	}{
		{"access_token", "?access_token=fake-access-9f2a", "fake-access-9f2a"},
		{"token", "?token=fake-token-7b31", "fake-token-7b31"},
		{"api_key", "?api_key=fake-key-4c88", "fake-key-4c88"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// redactURLCredentials is the single place that decides what leaves
			// the provider, so the rule is asserted directly.
			address := "https://speed.example.com" + testCase.suffix
			got, _ := redactURLCredentials(address)
			if strings.Contains(got, testCase.wantGo) {
				t.Errorf("address %q still carries the credential: %q", address, got)
			}
			if strings.Contains(got, "?") {
				t.Errorf("address %q kept a query string: %q", address, got)
			}
			if got != "https://speed.example.com" {
				t.Errorf("address = %q, want the credential free base address", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// L3: transport level error strings
// ---------------------------------------------------------------------------

// TestAuditTransportErrorsNeverCarryThePassword checks the third layer against a
// real connection failure, which is where Go formats the request URL.
func TestAuditTransportErrorsNeverCarryThePassword(t *testing.T) {
	listener := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := listener.URL
	listener.Close()

	measurement := NewGoSpeedHTTP(nil)
	t.Cleanup(measurement.CloseIdleConnections)

	result, runErr := measurement.Run(context.Background(), RunRequest{
		Node: Node{ID: "audit", Endpoint: strings.Replace(dead, "://", "://user:s3cr3tPassw0rd@", 1)},
		Capabilities: CapabilitySet{Supported: map[Capability]bool{
			CapDownload: true, CapLatency: true,
		}},
		Directions: []Direction{DirectionDownload},
		Duration:   200 * time.Millisecond,
	})
	if runErr == nil {
		t.Fatal("expected the run to fail")
	}

	if containsAnySecret(runErr.Error()) {
		t.Errorf("the returned error leaked a secret: %v", runErr)
	}
	if containsAnySecret(result.ErrorMessage) {
		t.Errorf("ErrorMessage leaked a secret: %q", result.ErrorMessage)
	}
	for _, warning := range result.Method.Warnings {
		if containsAnySecret(warning) {
			t.Errorf("a warning leaked a secret: %q", warning)
		}
	}
	if result.Detail != nil {
		var native speedtest.Result
		if err := json.Unmarshal(result.Detail.Payload, &native); err == nil {
			if native.Latency != nil {
				for _, sampleErr := range native.Latency.Errors {
					if containsAnySecret(sampleErr) {
						t.Errorf("a latency sample error leaked a secret: %q", sampleErr)
					}
				}
			}
			if containsAnySecret(native.ErrorMessage) {
				t.Errorf("the native error message leaked a secret: %q", native.ErrorMessage)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// the whole published surface, scanned at once
// ---------------------------------------------------------------------------

// TestAuditEntirePublishedSurfaceIsSecretFree serializes everything a consumer
// of one run can receive and scans it as a single blob, which is the strongest
// version of the audit: it cannot miss a nested field.
func TestAuditEntirePublishedSurfaceIsSecretFree(t *testing.T) {
	testServer := realGoSpeedServer(t)
	endpoint := strings.Replace(testServer.URL, "://", "://user:s3cr3tPassw0rd@", 1)

	completed := runAuditRun(t, endpoint, 512<<10)
	assertSecretFree(t, "completed run", completed)

	// And the failing run, which populates every error carrying field.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)
	failedResult := runAuditRun(t, strings.Replace(failing.URL, "://", "://user:s3cr3tPassw0rd@", 1), 256<<10)
	assertSecretFree(t, "failed run", failedResult)
}

// assertSecretFree serializes a result and fails if any marker survives.
func assertSecretFree(t *testing.T, label string, result FinalResult) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("%s: marshal: %v", label, err)
	}
	if containsAnySecret(string(encoded)) {
		t.Fatalf("%s: the published result leaked a secret:\n%s", label, encoded)
	}
}

// runAuditRun performs one small download against the given endpoint.
func runAuditRun(t *testing.T, endpoint string, budget int64) FinalResult {
	t.Helper()
	measurement := NewGoSpeedHTTP(nil)
	t.Cleanup(measurement.CloseIdleConnections)

	result, _ := measurement.Run(context.Background(), RunRequest{
		Node:         Node{ID: "audit", Endpoint: endpoint, Scope: ScopeLocal, Provider: IDGoSpeedHTTP},
		Capabilities: allCapabilities(),
		Budget:       BudgetLimits{DownloadBytes: budget},
	})
	return result
}
