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

// auditSecret is the credential fragment these tests plant in a node address.
const auditSecret = "sup3rsecret"

// auditNode builds a node whose endpoint embeds credentials.
func auditNode(endpoint string) Node {
	return Node{ID: "audit", Name: "Audit", Endpoint: endpoint, Scope: ScopeLocal, Provider: IDGoSpeedHTTP}
}

// credentialURL injects a credential into a base URL.
func credentialURL(base string) string {
	return strings.Replace(base, "://", "://user:"+auditSecret+"@", 1)
}

// containsSecret reports whether any of the needles appears in haystack.
func containsSecret(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

// TestAuditFailingRunDoesNotLeakCredentials drives a real measurement that
// fails, and then scans the complete serialized FinalResult.
//
// A failure is the interesting case: the engine records per sample errors, per
// connection errors and a top level error message, and network error strings
// routinely quote the request URL. This test is the audit that proves none of
// those paths can publish the credential component of a configured address.
func TestAuditFailingRunDoesNotLeakCredentials(t *testing.T) {
	// A server that always fails, so the run fills every error path.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream failure"))
	}))
	t.Cleanup(failing.Close)

	measurement := NewGoSpeedHTTP(nil)
	t.Cleanup(measurement.CloseIdleConnections)

	node := auditNode(credentialURL(failing.URL))
	result, runErr := measurement.Run(context.Background(), RunRequest{
		Node: node,
		Capabilities: CapabilitySet{Supported: map[Capability]bool{
			CapDownload: true, CapUpload: true, CapLatency: true,
		}},
		Directions: []Direction{DirectionDownload, DirectionUpload},
		Duration:   300 * time.Millisecond,
	})
	if runErr == nil {
		t.Fatal("expected the run to fail against a failing server")
	}
	assertNoCredentialLeak(t, "failed run", result, runErr)
}

// TestAuditUnreachableRunDoesNotLeakCredentials drives the transport error path,
// where Go's own error strings quote the request URL.
func TestAuditUnreachableRunDoesNotLeakCredentials(t *testing.T) {
	// Reserve and release a port so the address is almost certainly refused.
	listener := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := listener.URL
	listener.Close()

	measurement := NewGoSpeedHTTP(nil)
	t.Cleanup(measurement.CloseIdleConnections)

	node := auditNode(credentialURL(dead))
	result, runErr := measurement.Run(context.Background(), RunRequest{
		Node:         node,
		Capabilities: CapabilitySet{Supported: map[Capability]bool{CapDownload: true, CapLatency: true}},
		Directions:   []Direction{DirectionDownload},
		Duration:     200 * time.Millisecond,
	})
	if runErr == nil {
		t.Fatal("expected the run to fail against a closed port")
	}
	assertNoCredentialLeak(t, "unreachable run", result, runErr)
}

// TestAuditPartialConnectionFailureDoesNotLeakCredentials covers the per
// connection report path, which carries its own error strings.
func TestAuditPartialConnectionFailureDoesNotLeakCredentials(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/capabilities", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"protocol_version": 1, "server_version": "0.3.0",
			"capabilities": []string{"download"}, "limits": map[string]any{},
		})
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, _ *http.Request) {
		// Truncate early so the connection is recorded as failed.
		_, _ = w.Write(make([]byte, 16<<10))
	})
	testServer := httptest.NewServer(mux)
	t.Cleanup(testServer.Close)

	measurement := NewGoSpeedHTTP(nil)
	t.Cleanup(measurement.CloseIdleConnections)

	node := auditNode(credentialURL(testServer.URL))
	result, runErr := measurement.Run(context.Background(), RunRequest{
		Node:         node,
		Capabilities: CapabilitySet{Supported: map[Capability]bool{CapDownload: true, CapLatency: true}},
		Directions:   []Direction{DirectionDownload},
		Duration:     200 * time.Millisecond,
		Connections:  2,
	})
	_ = runErr
	assertNoCredentialLeak(t, "partial failure", result, runErr)
}

// TestAuditCleanRunDoesNotLeakCredentials proves a successful run is clean too.
func TestAuditCleanRunDoesNotLeakCredentials(t *testing.T) {
	testServer := realGoSpeedServer(t)
	measurement := NewGoSpeedHTTP(nil)
	t.Cleanup(measurement.CloseIdleConnections)

	node := auditNode(credentialURL(testServer.URL))
	result, err := measurement.Run(context.Background(), RunRequest{
		Node:         node,
		Capabilities: allCapabilities(),
		Budget:       BudgetLimits{DownloadBytes: measurableByteBudget},
	})
	if err != nil {
		t.Fatalf("Run: %v (%s)", err, result.ErrorMessage)
	}
	assertNoCredentialLeak(t, "clean run", result, nil)
}

// assertNoCredentialLeak serializes everything a consumer can receive and
// checks the credential never appears.
func assertNoCredentialLeak(t *testing.T, label string, result FinalResult, runErr error) {
	t.Helper()

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("%s: marshal FinalResult: %v", label, err)
	}
	forbidden := []string{auditSecret, "user:" + auditSecret}

	if containsSecret(string(encoded), forbidden...) {
		t.Fatalf("%s: the unified result leaked the node credential:\n%s", label, encoded)
	}

	if result.Detail != nil {
		detail, err := json.Marshal(result.Detail.Payload)
		if err != nil {
			t.Fatalf("%s: marshal detail: %v", label, err)
		}
		if containsSecret(string(detail), forbidden...) {
			t.Fatalf("%s: the native detail leaked the node credential:\n%s", label, detail)
		}
	}

	if runErr != nil && containsSecret(runErr.Error(), forbidden...) {
		t.Fatalf("%s: the returned error leaked the node credential: %v", label, runErr)
	}

	// The fields that carry free text from the engine must each be clean, so a
	// regression in any single path is attributable.
	if containsSecret(result.ErrorMessage, forbidden...) {
		t.Errorf("%s: ErrorMessage leaked the credential: %q", label, result.ErrorMessage)
	}
	for _, warning := range result.Method.Warnings {
		if containsSecret(warning, forbidden...) {
			t.Errorf("%s: Method.Warnings leaked the credential: %q", label, warning)
		}
	}
	if result.Detail != nil {
		var native speedtest.Result
		if err := json.Unmarshal(result.Detail.Payload, &native); err == nil {
			if containsSecret(native.ErrorMessage, forbidden...) {
				t.Errorf("%s: native ErrorMessage leaked: %q", label, native.ErrorMessage)
			}
			if native.Latency != nil {
				for _, sampleErr := range native.Latency.Errors {
					if containsSecret(sampleErr, forbidden...) {
						t.Errorf("%s: native latency sample error leaked: %q", label, sampleErr)
					}
				}
			}
			for _, transfer := range []*speedtest.TransferResult{native.Download, transferOf(native.Upload)} {
				if transfer == nil {
					continue
				}
				for _, report := range transfer.ConnectionReports {
					if containsSecret(report.Error, forbidden...) {
						t.Errorf("%s: connection report error leaked: %q", label, report.Error)
					}
				}
			}
			// The address may stay, but only without its userinfo component.
			if native.Target.ServerAddress != "" {
				if containsSecret(native.Target.ServerAddress, forbidden...) {
					t.Errorf("%s: target.server_address leaked: %q", label, native.Target.ServerAddress)
				}
				if strings.Contains(native.Target.ServerAddress, "@") {
					t.Errorf("%s: target.server_address kept a userinfo component: %q", label, native.Target.ServerAddress)
				}
			}
		}
	}
}

// transferOf avoids taking the address of an embedded field of a nil pointer.
func transferOf(upload *speedtest.UploadResult) *speedtest.TransferResult {
	if upload == nil {
		return nil
	}
	return &upload.TransferResult
}
