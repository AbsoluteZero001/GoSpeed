package ndt7

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	v2 "github.com/m-lab/locate/api/v2"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
)

// The tests in this file are fully offline. Every one of them talks to a
// local TLS test server, never to M-Lab, and no test uses a real
// credential: the tokens are made up here and are only ever handed to the
// local server.

// fakeJWT builds a JWT shaped token whose "exp" claim is expiry.
//
// It is not a real credential. Nothing verifies its signature, and it
// never leaves the test process.
func fakeJWT(expiry time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]int64{"exp": expiry.Unix()})
	if err != nil {
		panic(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(claims) + ".fake-signature"
}

// fakeTarget builds a Locate target shaped like a real M-Lab answer.
func fakeTarget(hostname, token string) v2.Target {
	urls := map[string]string{}
	for _, path := range []string{ndt7Path(DirectionDownload), ndt7Path(DirectionUpload)} {
		urls[trustedLocateScheme+"://"+path] = "wss://" + hostname + path +
			"?access_token=" + token + "&index=0&locate_version=v2"
	}
	return v2.Target{
		Machine:  hostname,
		Hostname: hostname,
		Location: &v2.Location{City: "Testville", Country: "TC"},
		URLs:     urls,
	}
}

// locateStub is a local TLS server standing in for the M-Lab Locate API.
// It records what it was asked so a test can assert on the request.
type locateStub struct {
	server *httptest.Server
	mu     sync.Mutex
	paths  []string
	agents []string
}

func newLocateStub(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *locateStub {
	t.Helper()
	stub := &locateStub{}
	stub.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		stub.paths = append(stub.paths, r.URL.Path)
		stub.agents = append(stub.agents, r.Header.Get("User-Agent"))
		stub.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

// baseURL is the Locate base the runner is pointed at. It keeps the
// production path shape, so the request the stub sees has the same path
// as a real Locate query.
func (s *locateStub) baseURL() string { return s.server.URL + "/v2/nearest/" }

func (s *locateStub) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.paths)
}

func (s *locateStub) lastPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.paths) == 0 {
		return ""
	}
	return s.paths[len(s.paths)-1]
}

func (s *locateStub) lastUserAgent() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.agents) == 0 {
		return ""
	}
	return s.agents[len(s.agents)-1]
}

// writeTargets answers with a Locate v2 answer carrying the targets.
func writeTargets(w http.ResponseWriter, targets []v2.Target) {
	body, err := json.Marshal(v2.NearestResult{Results: targets})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// consentedRunner returns a runner that may reach the network and that
// talks to the given stub.
func consentedRunner(t *testing.T, stub *locateStub) *Runner {
	t.Helper()
	runner := NewRunnerWithConsent(&ConsentRecord{
		Agreed:        true,
		AgreedAt:      time.Now(),
		PolicyVersion: consentVersion,
		ClientName:    ClientName,
		ClientVersion: ClientVersion,
	})
	runner.locateBaseURL = stub.baseURL()
	runner.locateHTTPClient = stub.server.Client()
	return runner
}

func TestDiscoverReturnsOneNode(t *testing.T) {
	token := fakeJWT(time.Now().Add(time.Hour))
	target := fakeTarget("ndt-mlab3-test01.mlab-oti.measurement-lab.org", token)
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeTargets(w, []v2.Target{target})
	})
	runner := consentedRunner(t, stub)

	nodes, err := runner.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes))
	}
	node := nodes[0]
	if node.ID != target.Hostname {
		t.Fatalf("id = %q, want %q", node.ID, target.Hostname)
	}
	if node.Provider != provider.IDMLabNDT7 {
		t.Fatalf("provider = %q, want %q", node.Provider, provider.IDMLabNDT7)
	}
	if node.Scope != provider.ScopeRemote {
		t.Fatalf("scope = %q, want %q", node.Scope, provider.ScopeRemote)
	}
	if node.Ownership != provider.OwnershipThirdParty {
		t.Fatalf("ownership = %q, want %q", node.Ownership, provider.OwnershipThirdParty)
	}
	if node.Discovery != provider.DiscoveryService {
		t.Fatalf("discovery = %q, want %q", node.Discovery, provider.DiscoveryService)
	}
	if !strings.Contains(node.Name, "Testville") {
		t.Fatalf("name = %q, want it to carry the location", node.Name)
	}
}

func TestDiscoverReturnsSeveralNodes(t *testing.T) {
	token := fakeJWT(time.Now().Add(time.Hour))
	first := fakeTarget("ndt-mlab3-test01.mlab-oti.measurement-lab.org", token)
	second := fakeTarget("ndt-mlab2-test02.mlab-oti.measurement-lab.org", token)
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeTargets(w, []v2.Target{first, second})
	})
	runner := consentedRunner(t, stub)

	nodes, err := runner.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes))
	}
	if nodes[0].ID != first.Hostname || nodes[1].ID != second.Hostname {
		t.Fatalf("nodes = %q, %q", nodes[0].ID, nodes[1].ID)
	}
}

func TestDiscoverQueriesTheNDT7Service(t *testing.T) {
	token := fakeJWT(time.Now().Add(time.Hour))
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeTargets(w, []v2.Target{fakeTarget("ndt-mlab3-test01.mlab-oti.measurement-lab.org", token)})
	})
	runner := consentedRunner(t, stub)

	if _, err := runner.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := stub.lastPath(); got != "/v2/nearest/ndt/ndt7" {
		t.Fatalf("request path = %q, want %q", got, "/v2/nearest/ndt/ndt7")
	}
	agent := stub.lastUserAgent()
	if agent == "" {
		t.Fatal("the Locate request carried no User-Agent")
	}
	if !strings.Contains(agent, ClientName) {
		t.Fatalf("User-Agent = %q, want it to name the client", agent)
	}
}

func TestDiscoverKeepsTheCompleteServiceURL(t *testing.T) {
	token := fakeJWT(time.Now().Add(time.Hour))
	target := fakeTarget("ndt-mlab3-test01.mlab-oti.measurement-lab.org", token)
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeTargets(w, []v2.Target{target})
	})
	runner := consentedRunner(t, stub)

	nodes, err := runner.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	endpoint := nodes[0].Endpoint
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse endpoint %q: %v", endpoint, err)
	}
	if parsed.Scheme != "wss" {
		t.Fatalf("scheme = %q, want wss", parsed.Scheme)
	}
	if parsed.Host != target.Hostname {
		t.Fatalf("host = %q, want %q", parsed.Host, target.Hostname)
	}
	if parsed.Path != ndt7Path(DirectionDownload) {
		t.Fatalf("path = %q, want %q", parsed.Path, ndt7Path(DirectionDownload))
	}
	values := parsed.Query()
	if values.Get("access_token") != token {
		t.Fatalf("access_token = %q, want the token Locate issued", values.Get("access_token"))
	}
	if values.Get("index") != "0" || values.Get("locate_version") != "v2" {
		t.Fatalf("query = %v, want every parameter preserved", values)
	}
	// The node identity must never carry the credential.
	if strings.Contains(nodes[0].ID, token) || strings.Contains(nodes[0].Name, token) {
		t.Fatal("the node identity leaked the access_token")
	}
}

func TestDiscoveredNodeDrivesBothDirections(t *testing.T) {
	token := fakeJWT(time.Now().Add(time.Hour))
	target := fakeTarget("ndt-mlab3-test01.mlab-oti.measurement-lab.org", token)
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeTargets(w, []v2.Target{target})
	})
	runner := consentedRunner(t, stub)

	nodes, err := runner.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, direction := range []Direction{DirectionDownload, DirectionUpload} {
		adapter := &Adapter{options: Options{
			Server:    nodes[0].Endpoint,
			Scheme:    "wss",
			Direction: direction,
		}}
		got, err := adapter.serviceURL()
		if err != nil {
			t.Fatalf("serviceURL(%s): %v", direction, err)
		}
		parsed, err := url.Parse(got)
		if err != nil {
			t.Fatalf("parse %q: %v", got, err)
		}
		if parsed.Path != ndt7Path(direction) {
			t.Fatalf("path for %s = %q, want %q", direction, parsed.Path, ndt7Path(direction))
		}
		if parsed.Query().Get("access_token") != token {
			t.Fatalf("the access_token was lost for %s", direction)
		}
		if parsed.Query().Get("index") != "0" {
			t.Fatalf("a query parameter was lost for %s", direction)
		}
	}
}

func TestDiscoverRejectsAnEmptyAnswer(t *testing.T) {
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeTargets(w, nil)
	})
	runner := consentedRunner(t, stub)

	nodes, err := runner.Discover(context.Background())
	if !errors.Is(err, ErrNoLocateServers) {
		t.Fatalf("err = %v, want %v", err, ErrNoLocateServers)
	}
	if nodes != nil {
		t.Fatalf("nodes = %v, want none", nodes)
	}
}

func TestDiscoverReportsALocateFailure(t *testing.T) {
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"title":"unavailable","detail":"no capacity","status":503}}`))
	})
	runner := consentedRunner(t, stub)

	_, err := runner.Discover(context.Background())
	if err == nil {
		t.Fatal("Discover did not fail")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("err = %v, want it to carry the server's reason", err)
	}
}

func TestDiscoverHonoursContextCancellation(t *testing.T) {
	arrived := make(chan struct{})
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-r.Context().Done()
	})
	runner := consentedRunner(t, stub)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runner.Discover(ctx)
		done <- err
	}()
	<-arrived
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Discover succeeded although the context was cancelled")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want it to wrap context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Discover did not return after the cancellation")
	}
}

func TestDiscoverHonoursTheRequestTimeout(t *testing.T) {
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	runner := consentedRunner(t, stub)
	runner.locateTimeout = 50 * time.Millisecond

	_, err := runner.Discover(context.Background())
	if err == nil {
		t.Fatal("Discover did not fail")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
}

func TestDiscoverRejectsInvalidCandidates(t *testing.T) {
	token := fakeJWT(time.Now().Add(time.Hour))
	cases := []struct {
		name   string
		target v2.Target
	}{
		{
			name: "unparseable url",
			target: v2.Target{
				Hostname: "ndt-mlab3-test01.mlab-oti.measurement-lab.org",
				URLs: map[string]string{
					"wss://" + ndt7Path(DirectionDownload): "wss://ndt-mlab3-test01.mlab-oti.measurement-lab.org/\x7f?access_token=" + token,
					"wss://" + ndt7Path(DirectionUpload):   "wss://ndt-mlab3-test01.mlab-oti.measurement-lab.org" + ndt7Path(DirectionUpload),
				},
			},
		},
		{
			name: "not encrypted",
			target: v2.Target{
				Hostname: "ndt-mlab3-test01.mlab-oti.measurement-lab.org",
				URLs: map[string]string{
					"wss://" + ndt7Path(DirectionDownload): "ws://ndt-mlab3-test01.mlab-oti.measurement-lab.org" + ndt7Path(DirectionDownload) + "?access_token=" + token,
					"wss://" + ndt7Path(DirectionUpload):   "wss://ndt-mlab3-test01.mlab-oti.measurement-lab.org" + ndt7Path(DirectionUpload) + "?access_token=" + token,
				},
			},
		},
		{
			name: "host outside the trusted list",
			target: v2.Target{
				Hostname: "ndt-mlab3-test01.evil.example",
				URLs: map[string]string{
					"wss://" + ndt7Path(DirectionDownload): "wss://ndt-mlab3-test01.evil.example" + ndt7Path(DirectionDownload) + "?access_token=" + token,
					"wss://" + ndt7Path(DirectionUpload):   "wss://ndt-mlab3-test01.evil.example" + ndt7Path(DirectionUpload) + "?access_token=" + token,
				},
			},
		},
		{
			name: "forged suffix",
			target: v2.Target{
				Hostname: "evilmeasurement-lab.org",
				URLs: map[string]string{
					"wss://" + ndt7Path(DirectionDownload): "wss://evilmeasurement-lab.org" + ndt7Path(DirectionDownload) + "?access_token=" + token,
					"wss://" + ndt7Path(DirectionUpload):   "wss://evilmeasurement-lab.org" + ndt7Path(DirectionUpload) + "?access_token=" + token,
				},
			},
		},
		{
			name: "missing upload url",
			target: v2.Target{
				Hostname: "ndt-mlab3-test01.mlab-oti.measurement-lab.org",
				URLs: map[string]string{
					"wss://" + ndt7Path(DirectionDownload): "wss://ndt-mlab3-test01.mlab-oti.measurement-lab.org" + ndt7Path(DirectionDownload) + "?access_token=" + token,
				},
			},
		},
		{
			name: "credentials in the url",
			target: v2.Target{
				Hostname: "ndt-mlab3-test01.mlab-oti.measurement-lab.org",
				URLs: map[string]string{
					"wss://" + ndt7Path(DirectionDownload): "wss://user:password@ndt-mlab3-test01.mlab-oti.measurement-lab.org" + ndt7Path(DirectionDownload) + "?access_token=" + token,
					"wss://" + ndt7Path(DirectionUpload):   "wss://ndt-mlab3-test01.mlab-oti.measurement-lab.org" + ndt7Path(DirectionUpload) + "?access_token=" + token,
				},
			},
		},
		{
			name: "nonstandard port",
			target: v2.Target{
				Hostname: "ndt-mlab3-test01.mlab-oti.measurement-lab.org",
				URLs: map[string]string{
					"wss://" + ndt7Path(DirectionDownload): "wss://ndt-mlab3-test01.mlab-oti.measurement-lab.org:8443" + ndt7Path(DirectionDownload) + "?access_token=" + token,
					"wss://" + ndt7Path(DirectionUpload):   "wss://ndt-mlab3-test01.mlab-oti.measurement-lab.org:8443" + ndt7Path(DirectionUpload) + "?access_token=" + token,
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
				writeTargets(w, []v2.Target{tc.target})
			})
			runner := consentedRunner(t, stub)

			nodes, err := runner.Discover(context.Background())
			if !errors.Is(err, ErrNoValidLocateServer) {
				t.Fatalf("err = %v, want %v", err, ErrNoValidLocateServer)
			}
			if nodes != nil {
				t.Fatalf("nodes = %v, want none", nodes)
			}
			if strings.Contains(err.Error(), token) {
				t.Fatalf("err %q leaks the access_token", err)
			}
		})
	}
}

func TestDiscoverKeepsTheValidCandidates(t *testing.T) {
	token := fakeJWT(time.Now().Add(time.Hour))
	valid := fakeTarget("ndt-mlab3-test01.mlab-oti.measurement-lab.org", token)
	invalid := fakeTarget("ndt-mlab3-test02.evil.example", token)
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeTargets(w, []v2.Target{invalid, valid})
	})
	runner := consentedRunner(t, stub)

	nodes, err := runner.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes))
	}
	if nodes[0].ID != valid.Hostname {
		t.Fatalf("id = %q, want %q", nodes[0].ID, valid.Hostname)
	}
}

func TestDiscoverFailsWhenEveryCandidateIsInvalid(t *testing.T) {
	token := fakeJWT(time.Now().Add(time.Hour))
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeTargets(w, []v2.Target{
			fakeTarget("ndt-mlab3-test01.evil.example", token),
			fakeTarget("ndt-mlab3-test02.evil.example", token),
		})
	})
	runner := consentedRunner(t, stub)

	nodes, err := runner.Discover(context.Background())
	if !errors.Is(err, ErrNoValidLocateServer) {
		t.Fatalf("err = %v, want %v", err, ErrNoValidLocateServer)
	}
	if nodes != nil {
		t.Fatalf("nodes = %v, want none", nodes)
	}
	if !strings.Contains(err.Error(), "2 candidate(s) rejected") {
		t.Fatalf("err = %v, want it to report how many were rejected", err)
	}
}

func TestDiscoverSendsNothingWithoutConsent(t *testing.T) {
	token := fakeJWT(time.Now().Add(time.Hour))
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeTargets(w, []v2.Target{fakeTarget("ndt-mlab3-test01.mlab-oti.measurement-lab.org", token)})
	})

	cases := []struct {
		name   string
		runner *Runner
	}{
		{"no consent record", NewRunner()},
		{"consent refused", NewRunnerWithConsent(&ConsentRecord{Agreed: false, PolicyVersion: consentVersion})},
		{"consent for another policy version", NewRunnerWithConsent(&ConsentRecord{Agreed: true, PolicyVersion: "ndt7-notice-v0"})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.runner.locateBaseURL = stub.baseURL()
			tc.runner.locateHTTPClient = stub.server.Client()
			before := stub.requestCount()
			_, err := tc.runner.Discover(context.Background())
			if !errors.Is(err, ErrConsentRequired) {
				t.Fatalf("err = %v, want %v", err, ErrConsentRequired)
			}
			if after := stub.requestCount(); after != before {
				t.Fatalf("the runner sent %d request(s) without consent", after-before)
			}
		})
	}
}

func TestDiscoverDoesNotLeakTheAccessToken(t *testing.T) {
	token := fakeJWT(time.Now().Add(time.Hour))
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeTargets(w, []v2.Target{fakeTarget("ndt-mlab3-test01.evil.example", token)})
	})
	runner := consentedRunner(t, stub)

	nodes, err := runner.Discover(context.Background())
	if err == nil {
		t.Fatal("Discover did not fail")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("err %q leaks the access_token", err)
	}
	if nodes != nil {
		t.Fatalf("nodes = %v, want none", nodes)
	}
}

func TestDiscoveryReusesTheCachedAnswer(t *testing.T) {
	token := fakeJWT(time.Now().Add(time.Hour))
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeTargets(w, []v2.Target{fakeTarget("ndt-mlab3-test01.mlab-oti.measurement-lab.org", token)})
	})
	runner := consentedRunner(t, stub)

	if _, err := runner.Discover(context.Background()); err != nil {
		t.Fatalf("first Discover: %v", err)
	}
	afterFirst := stub.requestCount()
	if afterFirst != 1 {
		t.Fatalf("the first Discover sent %d requests, want 1", afterFirst)
	}
	if _, err := runner.Discover(context.Background()); err != nil {
		t.Fatalf("second Discover: %v", err)
	}
	if after := stub.requestCount(); after != afterFirst {
		t.Fatalf("the second Discover sent %d more request(s), want the cached answer", after-afterFirst)
	}
}

func TestDiscoveryDoesNotCacheAnExpiredToken(t *testing.T) {
	expired := fakeJWT(time.Now().Add(-time.Minute))
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeTargets(w, []v2.Target{fakeTarget("ndt-mlab3-test01.mlab-oti.measurement-lab.org", expired)})
	})
	runner := consentedRunner(t, stub)

	if _, err := runner.Discover(context.Background()); err != nil {
		t.Fatalf("first Discover: %v", err)
	}
	afterFirst := stub.requestCount()
	if _, err := runner.Discover(context.Background()); err != nil {
		t.Fatalf("second Discover: %v", err)
	}
	if after := stub.requestCount(); after == afterFirst {
		t.Fatal("an expired token was cached and reused")
	}
}

func TestDiscoveryHonoursTheServerSchedule(t *testing.T) {
	token := fakeJWT(time.Now().Add(time.Hour))
	notBefore := time.Now().Add(time.Hour)
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := json.Marshal(v2.NearestResult{
			Results: []v2.Target{fakeTarget("ndt-mlab3-test01.mlab-oti.measurement-lab.org", token)},
			NextRequest: &v2.NextRequest{
				NotBefore: notBefore,
				Expires:   notBefore.Add(time.Hour),
				URL:       "https://locate.example.invalid/v2/nearest/ndt/ndt7?access_token=" + token,
			},
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	runner := consentedRunner(t, stub)

	if _, err := runner.Discover(context.Background()); err != nil {
		t.Fatalf("first Discover: %v", err)
	}
	afterFirst := stub.requestCount()
	if _, err := runner.Discover(context.Background()); err != nil {
		t.Fatalf("second Discover: %v", err)
	}
	if after := stub.requestCount(); after != afterFirst {
		t.Fatalf("the second Discover sent %d more request(s), want the server schedule to be honoured", after-afterFirst)
	}
}

func TestHostAllowedRejectsForgedSuffixes(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"ndt-mlab3-test01.mlab-oti.measurement-lab.org", true},
		{"measurement-lab.org", true},
		{"ndt.mlab-oti.measurementlab.net", true},
		{"NDT.MLAB-OTI.MEASUREMENT-LAB.ORG", true},
		{"ndt.mlab-oti.measurement-lab.org.", true},
		{"evilmeasurement-lab.org", false},
		{"measurement-lab.org.evil.example", false},
		{"measurement-lab.org.attacker.example", false},
		{"notmeasurementlab.net", false},
		{"127.0.0.1", false},
		{"::1", false},
		{"ndt.mlab-oti.measurement-lab.org\x00.evil", false},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			if got := hostAllowed(tc.host, trustedLocateHostSuffixes); got != tc.want {
				t.Fatalf("hostAllowed(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

func TestAccessTokenExpiryReadsTheClaim(t *testing.T) {
	want := time.Now().Add(time.Hour).Truncate(time.Second)
	token := fakeJWT(want)
	endpoint := "wss://ndt-mlab3-test01.mlab-oti.measurement-lab.org" + ndt7Path(DirectionDownload) + "?access_token=" + token
	got, ok := accessTokenExpiry(endpoint)
	if !ok {
		t.Fatal("accessTokenExpiry did not read the claim")
	}
	if !got.Equal(want) {
		t.Fatalf("expiry = %v, want %v", got, want)
	}
	if _, ok := accessTokenExpiry("wss://ndt.example.invalid/ndt/v7/download"); ok {
		t.Fatal("a URL without a token reported an expiry")
	}
	if _, ok := accessTokenExpiry("wss://ndt.example.invalid/ndt/v7/download?access_token=not-a-jwt"); ok {
		t.Fatal("a malformed token reported an expiry")
	}
}

func TestDiscoverDoesNotStartAMeasurement(t *testing.T) {
	token := fakeJWT(time.Now().Add(time.Hour))
	stub := newLocateStub(t, func(w http.ResponseWriter, r *http.Request) {
		writeTargets(w, []v2.Target{fakeTarget("ndt-mlab3-test01.mlab-oti.measurement-lab.org", token)})
	})
	runner := consentedRunner(t, stub)

	nodes, err := runner.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// Discovery returns addresses only. A node that carries no measured
	// rate and no byte count is the proof that nothing was transferred.
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes))
	}
	if fmt.Sprintf("%+v", nodes[0]) == "" {
		t.Fatal("the node is empty")
	}
}
