package mock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	v2 "github.com/m-lab/locate/api/v2"
)

// LocateScenario selects the behaviour of the Locate mock server. Scenarios
// mirror the P0-K-A verification matrix:
//
//	LocateNormal          1. valid target list
//	LocateEmptyResults    6. "results": null  -> official ErrNoAvailableServers
//	LocateEmptyArray      6. "results": []    -> official client returns no error (audited edge)
//	LocateInvalidTarget   7. target without usable measurement URLs
//	LocateInsecureURL     8. target URLs served over http:// (insecure)
//	LocateHTTP429         9. rate limited (RFC 7807 problem document)
//	LocateHTTP500        10. server error (non-JSON body)
//	LocateBodyStall       4. response body blocks forever
//	LocateSlowHeaders     3. headers never arrive in time (HTTP timeout)
//
// Scenario 2 (DNS / connection failure) needs no server: the test points the
// Locate client at a refused loopback port or injects a failing DNS
// transport, which keeps every test strictly local.
type LocateScenario string

const (
	LocateNormal        LocateScenario = "normal"
	LocateEmptyResults  LocateScenario = "empty_results"
	LocateEmptyArray    LocateScenario = "empty_array"
	LocateInvalidTarget LocateScenario = "invalid_target"
	LocateInsecureURL   LocateScenario = "insecure_url"
	LocateHTTP429       LocateScenario = "http_429"
	LocateHTTP500       LocateScenario = "http_500"
	LocateBodyStall     LocateScenario = "body_stall"
	LocateSlowHeaders   LocateScenario = "slow_headers"
)

// LocateServer is a localhost mock of the M-Lab Locate v2 Nearest endpoint.
type LocateServer struct {
	scenario LocateScenario
	// ndtScheme/ndtHost describe the NDT7 mock that target URLs point at.
	// Local tests use ws://127.0.0.1:port; production validation uses wss.
	ndtScheme string
	ndtHost   string

	server   *httptest.Server
	requests atomic.Int64
	active   atomic.Int64
}

// NewLocate starts a Locate mock. ndtHost is the host:port of the NDT7 mock
// server that returned target URLs should point at; ndtScheme is the scheme
// of those URLs (ws for local mocks, wss for production-shape fixtures).
func NewLocate(scenario LocateScenario, ndtScheme, ndtHost string) *LocateServer {
	s := &LocateServer{scenario: scenario, ndtScheme: ndtScheme, ndtHost: ndtHost}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Close stops the mock server.
func (s *LocateServer) Close() { s.server.Close() }

// BaseURL returns the value for locate.Client.BaseURL. The official client
// appends the service name ("ndt/ndt7") to the path.
func (s *LocateServer) BaseURL() string { return s.server.URL + "/v2/nearest/" }

// Snapshot reports how many Locate requests were received and how many are
// still active (stall detection).
type LocateSnapshot struct {
	Requests int64 `json:"requests"`
	Active   int64 `json:"active"`
}

func (s *LocateServer) Snapshot() LocateSnapshot {
	return LocateSnapshot{Requests: s.requests.Load(), Active: s.active.Load()}
}

func (s *LocateServer) handle(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	s.active.Add(1)
	defer s.active.Add(-1)

	switch s.scenario {
	case LocateNormal:
		s.writeJSON(w, http.StatusOK, nearestResult(
			s.target(s.ndtScheme, s.ndtHost)))
	case LocateEmptyResults:
		// No "results" field: the official client maps this to
		// ErrNoAvailableServers.
		s.writeJSON(w, http.StatusOK, map[string]any{})
	case LocateEmptyArray:
		// Empty array: the official client returns ([]Target{}, nil).
		s.writeJSON(w, http.StatusOK, map[string]any{"results": []any{}})
	case LocateInvalidTarget:
		target := s.target(s.ndtScheme, s.ndtHost)
		target.URLs = map[string]string{"wss:///ndt/v7/download": ""}
		s.writeJSON(w, http.StatusOK, nearestResult(target))
	case LocateInsecureURL:
		target := s.target(s.ndtScheme, s.ndtHost)
		target.URLs = map[string]string{
			"wss:///ndt/v7/download": "http://" + s.ndtHost + "/ndt/v7/download",
			"wss:///ndt/v7/upload":   "http://" + s.ndtHost + "/ndt/v7/upload",
		}
		s.writeJSON(w, http.StatusOK, nearestResult(target))
	case LocateHTTP429:
		// The official client only recognises a problem document nested in
		// the NearestResult envelope ("error" key). A bare RFC 7807 document
		// would be swallowed into ErrNoAvailableServers instead - documented
		// in the P0-K report as an audited edge case.
		s.writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error": map[string]any{
				"type":   "about:blank",
				"title":  "Too Many Requests",
				"status": 429,
				"detail": "mock rate limit reached; pause before scheduling a new request",
			},
		})
	case LocateHTTP500:
		// Non-JSON body: the official client surfaces a JSON decode error.
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal server error"))
	case LocateBodyStall:
		// Send headers, then never send a body. A normal (non-hijacked)
		// handler observes client disconnects through r.Context().
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	case LocateSlowHeaders:
		// Never respond within the client timeout window.
		select {
		case <-r.Context().Done():
		case <-s.serverCloseChan():
		}
	default:
		http.Error(w, "unknown locate scenario", http.StatusInternalServerError)
	}
}

func (s *LocateServer) serverCloseChan() <-chan struct{} {
	// httptest.Server has no exported close channel; closing via Close()
	// interrupts handlers anyway. A nil channel blocks forever, which is the
	// intended behaviour for the slow-headers scenario.
	return nil
}

func (s *LocateServer) target(scheme, host string) v2.Target {
	return v2.Target{
		Machine:  "mlab1-xxx",
		Hostname: host,
		Location: &v2.Location{City: "Mock City", Country: "MX"},
		URLs: map[string]string{
			scheme + ":///ndt/v7/download": scheme + "://" + host + "/ndt/v7/download",
			scheme + ":///ndt/v7/upload":   scheme + "://" + host + "/ndt/v7/upload",
		},
	}
}

func nearestResult(results ...v2.Target) map[string]any {
	return map[string]any{"results": results}
}

func (s *LocateServer) writeJSON(w http.ResponseWriter, status int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "mock encode error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}
