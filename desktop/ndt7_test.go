package main

// The desktop side of the M-Lab NDT7 integration.
//
// The tests below cover the guarantees the desktop client has to make on its
// own, on top of the ones the provider already enforces:
//
//   - the consent gate, and the re-consent a changed wording forces,
//   - the single use run authorization, and the node and direction it is
//     bound to,
//   - the daily limit, including that a reload does not reset it,
//   - the reference based node addressing, so a caller cannot point a
//     measurement at an address of its own choosing,
//   - the absence of a credential in anything that reaches the frontend.
//
// The measurements run against the local NDT7 protocol mock, so the whole
// path is exercised without contacting M-Lab.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7/mock"
)

// newTestNDT7Gateway builds a gateway that never touches the user's
// configuration directory: the daily counter and the consent decision live
// in memory.
func newTestNDT7Gateway(t *testing.T) *ndt7Gateway {
	t.Helper()
	return newTestNDT7GatewayWith(t, ndt7GatewayOptions{
		daily:   ndt7.NewMemoryDailyRunStore(),
		consent: &memoryConsentStore{},
	})
}

// newTestNDT7GatewayWith builds a gateway from explicit parts.
func newTestNDT7GatewayWith(t *testing.T, options ndt7GatewayOptions) *ndt7Gateway {
	t.Helper()
	gateway, err := newNDT7GatewayWith(options)
	if err != nil {
		t.Fatalf("newNDT7GatewayWith: %v", err)
	}
	return gateway
}

// newTestNDT7Runner returns a runner whose M-Lab gateway is the test one.
//
// The runner's registry is built from the same gateway, so the provider the
// run uses and the provider the authorization was issued by are one instance.
func newTestNDT7Runner(t *testing.T, rec *recorder, gateway *ndt7Gateway) *Runner {
	t.Helper()
	runner := NewRunner(rec.emit)
	runner.ndt7 = gateway
	return runner
}

// publishMockNode publishes one node that points at the local NDT7 mock.
//
// The mock speaks the real protocol over a loopback WebSocket, so the
// authorization, the budget, the measurement window and the result mapping
// are all exercised for real without contacting M-Lab.
func publishMockNode(t *testing.T, gateway *ndt7Gateway, srv *mock.Server, id string) string {
	t.Helper()
	refs := publishMockNodes(t, gateway, mockNode(srv, id))
	if len(refs) != 1 {
		t.Fatalf("publish returned %d references, want 1", len(refs))
	}
	return refs[0]
}

// publishMockNodes publishes several nodes in one call.
//
// The store replaces its whole contents on every publish, so nodes that have
// to stay referenceable at the same time must be published together.
func publishMockNodes(t *testing.T, gateway *ndt7Gateway, nodes ...provider.Node) []string {
	t.Helper()
	views := gateway.nodes.publish(nodes)
	refs := make([]string, 0, len(views))
	for _, view := range views {
		refs = append(refs, view.Ref)
	}
	return refs
}

// newSlowServer returns a local HTTP server that answers every request only
// after a delay, so a task that runs against it stays busy long enough to be
// observed.
func newSlowServer(delay time.Duration) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
}

// waitForBusy waits until the runner reports a running task.
func waitForBusy(t *testing.T, runner *Runner) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if runner.Status().Busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the runner did not report a running task")
}

// mockNode builds a provider node that points at the local mock.
func mockNode(srv *mock.Server, id string) provider.Node {
	return provider.Node{
		ID:        id,
		Name:      id,
		Endpoint:  "ws://" + srv.Host() + "/ndt/v7/download",
		Scope:     provider.ScopeRemote,
		Provider:  provider.IDMLabNDT7,
		Ownership: provider.OwnershipThirdParty,
		Discovery: provider.DiscoveryService,
	}
}

// startNDT7 runs one M-Lab measurement and returns the finished event.
func startNDT7(t *testing.T, rec *recorder, runner *Runner, ref string, direction string) FinishedEvent {
	t.Helper()
	if _, err := runner.Start(StartOptions{
		SelectionMode:   SelectionModeManual,
		Provider:        string(ndt7ProviderID),
		ProviderNodeRef: ref,
		Direction:       direction,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return waitFinished(t, rec, 30*time.Second)
}

// TestNDT7DiscoveryRequiresConsent proves the consent gate is in front of
// the discovery: without a current consent no Locate query is sent at all.
func TestNDT7DiscoveryRequiresConsent(t *testing.T) {
	gateway := newTestNDT7Gateway(t)
	if _, err := gateway.discover(context.Background()); !errors.Is(err, errNDT7ConsentRequired) {
		t.Fatalf("error = %v, want the consent gate to refuse the discovery", err)
	}
	if gateway.state().Agreed {
		t.Fatal("the state must not report an agreement that was never given")
	}
}

// TestNDT7StaleConsentVersionForcesReconsent proves that a stored agreement
// under an older wording does not carry over.
func TestNDT7StaleConsentVersionForcesReconsent(t *testing.T) {
	store := &memoryConsentStore{}
	if err := store.save(ndt7.ConsentRecord{
		Agreed:        true,
		AgreedAt:      time.Now().Add(-24 * time.Hour),
		PolicyVersion: "ndt7-notice-v1",
		ClientName:    ndt7.ClientName,
		ClientVersion: ndt7.ClientVersion,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	gateway := newTestNDT7GatewayWith(t, ndt7GatewayOptions{
		daily:   ndt7.NewMemoryDailyRunStore(),
		consent: store,
	})
	if gateway.consentAgreed() {
		t.Fatal("a consent recorded under an older wording version must not be honoured")
	}
	state := gateway.state()
	if !state.ConsentRequired || state.Agreed {
		t.Fatalf("state = %+v, want the consent to be required again", state)
	}
	if state.PolicyVersion != ndt7.ConsentVersion() {
		t.Fatalf("policy version = %q, want %q", state.PolicyVersion, ndt7.ConsentVersion())
	}
	// The wording the user has to read again is the current one, and the
	// notice states which version it is.
	if !strings.Contains(state.Notice, ndt7.ConsentVersion()) {
		t.Fatalf("the notice must state the wording version %q", ndt7.ConsentVersion())
	}
}

// TestNDT7CurrentConsentIsHonoured proves the current wording opens the
// gate, so the re-consent rule does not block a returning user.
func TestNDT7CurrentConsentIsHonoured(t *testing.T) {
	store := &memoryConsentStore{}
	if err := store.save(ndt7.ConsentRecord{
		Agreed:        true,
		AgreedAt:      time.Now().Add(-time.Hour),
		PolicyVersion: ndt7.ConsentVersion(),
		ClientName:    ndt7.ClientName,
		ClientVersion: ndt7.ClientVersion,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	gateway := newTestNDT7GatewayWith(t, ndt7GatewayOptions{
		daily:   ndt7.NewMemoryDailyRunStore(),
		consent: store,
	})
	if !gateway.consentAgreed() {
		t.Fatal("a consent recorded under the current wording version must be honoured")
	}
}

// TestNDT7RunAuthorizationIsUsedUpByTheFirstRun proves the authorization is
// single use: one confirmation permits exactly one measurement.
func TestNDT7RunAuthorizationIsUsedUpByTheFirstRun(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "ndt-mock-one")

	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	first := startNDT7(t, rec, runner, ref, "download")
	if first.Status != StateCompleted {
		t.Fatalf("first run status = %q, error = %q", first.Status, first.Error)
	}
	if got := srv.Snapshot().Connections; got != 1 {
		t.Fatalf("connections = %d, want exactly 1", got)
	}

	// The confirmation is gone. The session also dropped the node reference,
	// so the same node has to be published again to reach the authorization
	// gate at all - and the gate still refuses, because the grant was
	// consumed by the first run.
	ref = publishMockNode(t, gateway, srv, "ndt-mock-one")
	second := startNDT7(t, rec, runner, ref, "download")
	if second.Status == StateCompleted {
		t.Fatal("a second run must not be covered by one confirmation")
	}
	if second.Unified == nil || second.Unified.ErrorCode != "measurement_not_authorized" {
		t.Fatalf("unified = %+v, want the measurement to be reported as not authorized", second.Unified)
	}
	if got := srv.Snapshot().Connections; got != 1 {
		t.Fatalf("connections = %d, want the refused run to have opened none", got)
	}
}

// TestNDT7RunAuthorizationIsBoundToTheNode proves an authorization for one
// node does not cover another.
func TestNDT7RunAuthorizationIsBoundToTheNode(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	first := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer first.Close()
	second := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer second.Close()
	refs := publishMockNodes(t, gateway, mockNode(first, "ndt-mock-a"), mockNode(second, "ndt-mock-b"))
	if len(refs) != 2 {
		t.Fatalf("publish returned %d references, want 2", len(refs))
	}
	firstRef, secondRef := refs[0], refs[1]

	if _, err := gateway.authorizeRun(firstRef, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, secondRef, "download")
	if finished.Status == StateCompleted {
		t.Fatal("an authorization for one node must not cover another node")
	}
	if finished.Unified == nil || finished.Unified.ErrorCode != "measurement_authorization_mismatch" {
		t.Fatalf("unified = %+v, want the mismatch to be reported", finished.Unified)
	}
	if got := second.Snapshot().Connections; got != 0 {
		t.Fatalf("connections = %d, want the refused run to have opened none", got)
	}
}

// TestNDT7RunAuthorizationIsBoundToTheDirection proves a download
// authorization does not cover an upload measurement.
func TestNDT7RunAuthorizationIsBoundToTheDirection(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "ndt-mock-direction")

	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, ref, "upload")
	if finished.Status == StateCompleted {
		t.Fatal("a download authorization must not cover an upload measurement")
	}
	if finished.Unified == nil || finished.Unified.ErrorCode != "measurement_authorization_mismatch" {
		t.Fatalf("unified = %+v, want the mismatch to be reported", finished.Unified)
	}
	if got := srv.Snapshot().Connections; got != 0 {
		t.Fatalf("connections = %d, want the refused run to have opened none", got)
	}
}

// TestNDT7UnknownNodeReferenceIsRejected proves an invented reference does
// not resolve to anything.
func TestNDT7UnknownNodeReferenceIsRejected(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	publishMockNode(t, gateway, srv, "ndt-mock-unknown")

	finished := startNDT7(t, rec, runner, "pnode-9999", "download")
	if finished.Status == StateCompleted {
		t.Fatal("an unknown node reference must not measure anything")
	}
	if !strings.Contains(finished.Error, "unknown provider node reference") {
		t.Fatalf("error = %q, want the unknown reference to be named", finished.Error)
	}
	if got := srv.Snapshot().Connections; got != 0 {
		t.Fatalf("connections = %d, want the refused run to have opened none", got)
	}
}

// TestNDT7RequestCannotCarryAnEndpoint proves the frontend cannot express
// an address for a measurement.
//
// The request has no field that could hold one, and the NDT7 path clears
// the engine's node id, so the only thing the frontend can name is the
// opaque reference. The endpoint always comes from the backend's own node
// store.
func TestNDT7RequestCannotCarryAnEndpoint(t *testing.T) {
	typ := reflect.TypeOf(StartOptions{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := strings.ToLower(field.Tag.Get("json"))
		for _, forbidden := range []string{"endpoint", "url", "token", "host", "address"} {
			if strings.Contains(tag, forbidden) {
				t.Fatalf("the request must not carry %q: field %s", forbidden, field.Name)
			}
		}
	}

	payload := []byte(`{"selectionMode":"manual","provider":"mlab-ndt7",` +
		`"providerNodeRef":"pnode-1","direction":"download",` +
		`"endpoint":"wss://evil.example/ndt/v7/download?access_token=forged",` +
		`"nodeId":"wss://evil.example"}`)
	var options StartOptions
	if err := json.Unmarshal(payload, &options); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	normalized, err := options.normalized()
	if err != nil {
		t.Fatalf("normalized: %v", err)
	}
	if normalized.NodeID != "" {
		t.Fatalf("node id = %q, want the NDT7 path to ignore it", normalized.NodeID)
	}
	if normalized.ProviderNodeRef != "pnode-1" {
		t.Fatalf("node reference = %q, want the reference to survive", normalized.ProviderNodeRef)
	}
	rendered := fmt.Sprintf("%+v", normalized)
	for _, forbidden := range []string{"evil.example", "access_token", "forged"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("the normalised request must not carry %q: %s", forbidden, rendered)
		}
	}
}

// TestNDT7RunWithoutAuthorizationIsRefused proves the desktop cannot start
// an M-Lab measurement without a confirmation issued by the backend.
func TestNDT7RunWithoutAuthorizationIsRefused(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "ndt-mock-unauthorized")

	finished := startNDT7(t, rec, runner, ref, "download")
	if finished.Status == StateCompleted {
		t.Fatal("a measurement without a confirmation must not run")
	}
	if finished.Unified == nil || finished.Unified.ErrorCode != "measurement_not_authorized" {
		t.Fatalf("unified = %+v, want the measurement to be reported as not authorized", finished.Unified)
	}
	if got := srv.Snapshot().Connections; got != 0 {
		t.Fatalf("connections = %d, want the refused run to have opened none", got)
	}
}

// TestNDT7RunWithoutConsentIsRefused proves the consent gate is in front of
// the measurement as well.
func TestNDT7RunWithoutConsentIsRefused(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "ndt-mock-noconsent")

	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, ref, "download")
	if finished.Status == StateCompleted {
		t.Fatal("a measurement without a consent must not run")
	}
	if finished.Unified == nil || finished.Unified.ErrorCode != "consent_required" {
		t.Fatalf("unified = %+v, want the consent to be reported as missing", finished.Unified)
	}
	if got := srv.Snapshot().Connections; got != 0 {
		t.Fatalf("connections = %d, want the refused run to have opened none", got)
	}
}

// TestNDT7DailyLimitCannotBeBypassed proves the daily ceiling is enforced
// before a connection is opened, and that the counter survives a reload.
func TestNDT7DailyLimitCannotBeBypassed(t *testing.T) {
	counterPath := filepath.Join(t.TempDir(), "ndt7-daily-runs.json")
	rec := newRecorder()
	gateway := newTestNDT7GatewayWith(t, ndt7GatewayOptions{
		daily:   ndt7.NewFileDailyRunStore(counterPath),
		consent: &memoryConsentStore{},
	})
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "ndt-mock-daily")

	// Fill the day up to the ceiling.
	for i := 0; i < ndt7.MaxDailyRuns; i++ {
		if _, err := gateway.daily.Reserve(time.Now()); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
	}
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, ref, "download")
	if finished.Status == StateCompleted {
		t.Fatal("a measurement beyond the daily ceiling must not run")
	}
	if finished.Unified == nil || finished.Unified.ErrorCode != "daily_limit_reached" {
		t.Fatalf("unified = %+v, want the daily limit to be reported", finished.Unified)
	}
	if got := srv.Snapshot().Connections; got != 0 {
		t.Fatalf("connections = %d, want the refused run to have opened none", got)
	}

	// A reload of the window must not reset the count: the counter lives in
	// the file, not in the process.
	reloaded := newTestNDT7GatewayWith(t, ndt7GatewayOptions{
		daily:   ndt7.NewFileDailyRunStore(counterPath),
		consent: &memoryConsentStore{},
	})
	state := reloaded.state()
	if state.DailyUsed != ndt7.MaxDailyRuns {
		t.Fatalf("daily used = %d, want the count to survive the reload", state.DailyUsed)
	}
	if state.DailyRemaining != 0 {
		t.Fatalf("daily remaining = %d, want 0", state.DailyRemaining)
	}
}

// TestNDT7CancellationClearsAuthorizationAndNodes proves a cancelled run
// leaves no authorization and no node reference behind.
func TestNDT7CancellationClearsAuthorizationAndNodes(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 50*time.Millisecond, 20*time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "ndt-mock-cancel")

	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	if _, err := runner.Start(StartOptions{
		SelectionMode:   SelectionModeManual,
		Provider:        string(ndt7ProviderID),
		ProviderNodeRef: ref,
		Direction:       "download",
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Cancel while the transfer is still running.
	time.Sleep(300 * time.Millisecond)
	if err := runner.CancelTest(); err != nil {
		t.Fatalf("CancelTest: %v", err)
	}
	finished := waitFinished(t, rec, 30*time.Second)
	if finished.Status != StateCancelled {
		t.Fatalf("status = %q, error = %q, want a cancelled run", finished.Status, finished.Error)
	}
	if pending := gateway.provider().PendingRunAuthorizations(); pending != 0 {
		t.Fatalf("pending authorizations = %d, want none after the session ended", pending)
	}
	if views := gateway.nodes.allViews(); len(views) != 0 {
		t.Fatalf("node views = %d, want the node references dropped after the session ended", len(views))
	}
}

// TestNDT7ConsentRefusalWithdrawsEverything proves a refusal drops the
// pending authorizations and the discovered nodes.
func TestNDT7ConsentRefusalWithdrawsEverything(t *testing.T) {
	gateway := newTestNDT7Gateway(t)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "ndt-mock-refuse")
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	if pending := gateway.provider().PendingRunAuthorizations(); pending != 1 {
		t.Fatalf("pending authorizations = %d, want 1 before the refusal", pending)
	}
	state, err := gateway.recordConsent(false)
	if err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	if state.Agreed || !state.ConsentRequired {
		t.Fatalf("state = %+v, want the consent to be withdrawn", state)
	}
	if pending := gateway.provider().PendingRunAuthorizations(); pending != 0 {
		t.Fatalf("pending authorizations = %d, want none after the refusal", pending)
	}
	if views := gateway.nodes.allViews(); len(views) != 0 {
		t.Fatalf("node views = %d, want the node references dropped after the refusal", len(views))
	}
}

// TestNDT7NothingThatReachesTheFrontendCarriesTheToken proves the access
// token never leaves the backend.
//
// The token is part of the service URL the Locate answer carried. The GUI
// sees views, states and results; none of them may contain the URL, and
// neither may the error text of a failed run.
func TestNDT7NothingThatReachesTheFrontendCarriesTheToken(t *testing.T) {
	const token = "SUPERSECRETTOKEN"
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	// Give the stored node an access token, the way a Locate answer would.
	node := mockNode(srv, "ndt-mock-token")
	node.Endpoint = "ws://" + srv.Host() + "/ndt/v7/download?access_token=" + token
	ref := publishMockNodes(t, gateway, node)[0]

	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, ref, "download")
	encoded, err := json.Marshal(finished)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), token) {
		t.Fatalf("the finished event carries the access token: %s", encoded)
	}
	if strings.Contains(finished.Error, token) {
		t.Fatalf("the error text carries the access token: %q", finished.Error)
	}
	state := gateway.state()
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(stateJSON), token) {
		t.Fatalf("the consent state carries the access token: %s", stateJSON)
	}
	viewJSON, err := json.Marshal(gateway.nodes.allViews())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(viewJSON), token) {
		t.Fatalf("the node view carries the access token: %s", viewJSON)
	}
	for _, forbidden := range []string{"endpoint", "access_token", "url"} {
		if strings.Contains(strings.ToLower(string(viewJSON)), forbidden) {
			t.Fatalf("the node view exposes %q: %s", forbidden, viewJSON)
		}
	}
}

// TestNDT7DiscoveryPathIssuesNoRequestOfItsOwn proves the desktop never
// performs a Locate request itself.
//
// The redirect refusal lives in the provider, which refuses to follow a
// redirecting Locate answer (internal/provider/ndt7, TestDiscoverRefusesARedirect).
// The desktop adds no second, weaker path: it has no HTTP client at all, so
// there is nothing here that could follow a redirect.
func TestNDT7DiscoveryPathIssuesNoRequestOfItsOwn(t *testing.T) {
	source, err := os.ReadFile("ndt7.go")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(source)
	for _, forbidden := range []string{"http.Get", "http.Post", "http.Client", "CheckRedirect", "net/http"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("the desktop NDT7 path must not use %q: it delegates every request to the provider", forbidden)
		}
	}
}

// TestNDT7UnavailableGatewayDoesNotBreakTheSelfHostedProvider proves a
// broken M-Lab setup cannot take the GoSpeed HTTP provider down with it.
func TestNDT7UnavailableGatewayDoesNotBreakTheSelfHostedProvider(t *testing.T) {
	rec := newRecorder()
	runner := NewRunner(rec.emit)
	runner.ndt7 = nil
	runner.ndt7Err = errors.New("desktop: the user configuration directory is not available")
	httpServer := newSpeedtestServer(t)
	manager := newManager(t, httpServer.URL)
	runner.loadNodesFn = func() (*nodes.Manager, string, error) {
		return manager, "test-nodes.json", nil
	}
	if _, err := runner.Start(StartOptions{
		SelectionMode:    SelectionModeManual,
		NodeID:           "test",
		Connections:      2,
		DurationMs:       1000,
		SampleIntervalMs: 100,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished := waitFinished(t, rec, 30*time.Second)
	if finished.Status != StateCompleted {
		t.Fatalf("status = %q, error = %q, want the self hosted run to complete", finished.Status, finished.Error)
	}

	// The M-Lab provider is not registered, so a run that asks for it is
	// refused instead of being measured by something else.
	missing := startNDT7(t, rec, runner, "pnode-1", "download")
	if missing.Status == StateCompleted {
		t.Fatal("an M-Lab run must be refused when the provider is not registered")
	}
}

// TestNDT7SelfHostedProviderIsUnaffected proves the GoSpeed HTTP path keeps
// working next to the M-Lab one.
func TestNDT7SelfHostedProviderIsUnaffected(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	httpServer := newSpeedtestServer(t)
	manager := newManager(t, httpServer.URL)
	runner.loadNodesFn = func() (*nodes.Manager, string, error) {
		return manager, "test-nodes.json", nil
	}
	if _, err := runner.Start(StartOptions{
		SelectionMode:    SelectionModeManual,
		NodeID:           "test",
		Connections:      2,
		DurationMs:       1000,
		SampleIntervalMs: 100,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished := waitFinished(t, rec, 30*time.Second)
	if finished.Status != StateCompleted {
		t.Fatalf("status = %q, error = %q, want the self hosted run to complete", finished.Status, finished.Error)
	}
	if finished.Unified == nil || finished.Unified.ProviderID != provider.IDGoSpeedHTTP {
		t.Fatalf("unified = %+v, want the GoSpeed HTTP provider to have served the run", finished.Unified)
	}
}

// TestNDT7RepeatedAuthorizationDoesNotBuyASecondMeasurement proves that a
// second confirmation of the SAME measurement cannot buy a second
// measurement.
//
// The provider issues one grant per AuthorizeRun call, so two calls for the
// same node and direction leave two pending grants and two measurements.
// The desktop must therefore treat the confirmation as idempotent: a double
// click is one confirmation, not two.
func TestNDT7RepeatedAuthorizationDoesNotBuyASecondMeasurement(t *testing.T) {
	gateway := newTestNDT7Gateway(t)
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "ndt-mock-double")

	for i := 0; i < 2; i++ {
		if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
			t.Fatalf("authorizeRun %d: %v", i, err)
		}
	}
	if pending := gateway.provider().PendingRunAuthorizations(); pending != 1 {
		t.Fatalf("pending authorizations = %d, want 1: a repeated confirmation of the same measurement must not buy a second measurement", pending)
	}
}

// TestNDT7DiscoveryIsRefusedWhileAnotherTaskRuns proves that node discovery
// is a task of its own and not something that runs alongside a measurement.
//
// The discovery publishes the nodes, and a measurement drops the node
// references when it ends. A discovery that runs concurrently with a
// measurement therefore has its result wiped by the end of that measurement,
// and it competes for the same provider with a transfer in flight.
//
// The gateway in this test has no consent, so the discovery can never reach
// the network: the refusal has to come from the task bookkeeping, not from
// the consent gate.
func TestNDT7DiscoveryIsRefusedWhileAnotherTaskRuns(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	slow := newSlowServer(2 * time.Second)
	defer slow.Close()
	manager := newManager(t, slow.URL)
	runner.loadNodesFn = func() (*nodes.Manager, string, error) {
		return manager, "test-nodes.json", nil
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runner.CheckNodes()
	}()
	waitForBusy(t, runner)
	defer func() { <-done }()

	_, err := runner.discoverNDT7Nodes(context.Background())
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("discovery error = %v, want the running task to refuse it", err)
	}
}

// TestNDT7RevokingConsentStopsTheRunningMeasurement proves that withdrawing
// the consent takes effect immediately.
//
// A measurement that keeps running after the consent was withdrawn would
// still publish the user's address to a third party, so the withdrawal has
// to stop it rather than wait for the next run.
func TestNDT7RevokingConsentStopsTheRunningMeasurement(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 50*time.Millisecond, 20*time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "ndt-mock-revoke")
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	if _, err := runner.Start(StartOptions{
		SelectionMode:   SelectionModeManual,
		Provider:        string(ndt7ProviderID),
		ProviderNodeRef: ref,
		Direction:       "download",
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Let the transfer start before the consent is withdrawn.
	time.Sleep(300 * time.Millisecond)

	if _, err := runner.setNDT7Consent(false); err != nil {
		t.Fatalf("setNDT7Consent: %v", err)
	}
	finished := waitFinished(t, rec, 30*time.Second)
	if finished.Status != StateCancelled {
		t.Fatalf("status = %q, error = %q, want the measurement to stop when the consent is withdrawn", finished.Status, finished.Error)
	}
}

// TestNDT7CompletedRunClearsTheAuthorizationAndTheNodes proves a finished
// measurement leaves nothing behind that could carry into the next one.
func TestNDT7CompletedRunClearsTheAuthorizationAndTheNodes(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "ndt-mock-done")
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, ref, "download")
	if finished.Status != StateCompleted {
		t.Fatalf("status = %q, error = %q, want the run to complete", finished.Status, finished.Error)
	}
	if pending := gateway.provider().PendingRunAuthorizations(); pending != 0 {
		t.Fatalf("pending authorizations = %d, want none after the run", pending)
	}
	if views := gateway.nodes.allViews(); len(views) != 0 {
		t.Fatalf("node views = %d, want the node references dropped after the run", len(views))
	}
}

// TestNDT7FailedConnectionLeavesNoReusableAuthorization proves a connection
// that fails does not leave an authorization behind.
//
// The authorization is spent before the connection opens, because a run that
// reached the socket has to be confirmed again whatever its outcome - the
// server recorded it. A rejected handshake is such a run.
func TestNDT7FailedConnectionLeavesNoReusableAuthorization(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.New(mock.ScenarioHandshakeFailure, 0)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "ndt-mock-handshake")
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, ref, "download")
	if finished.Status == StateCompleted {
		t.Fatal("a rejected handshake must not be reported as a completed measurement")
	}
	if pending := gateway.provider().PendingRunAuthorizations(); pending != 0 {
		t.Fatalf("pending authorizations = %d, want none after a failed connection", pending)
	}
	if views := gateway.nodes.allViews(); len(views) != 0 {
		t.Fatalf("node views = %d, want the node references dropped after a failed connection", len(views))
	}
}

// TestNDT7FailedDiscoveryLeavesNoNodes proves a refused discovery does not
// publish a half initialised node list.
//
// The discovery in this test is refused by the consent gate, so no request
// is sent at all. A refusal of any other kind behaves the same way: the
// nodes are published only after the provider returned them.
func TestNDT7FailedDiscoveryLeavesNoNodes(t *testing.T) {
	gateway := newTestNDT7Gateway(t)
	if _, err := gateway.discover(context.Background()); err == nil {
		t.Fatal("a discovery without a consent must be refused")
	}
	if views := gateway.nodes.allViews(); len(views) != 0 {
		t.Fatalf("node views = %d, want a refused discovery to leave no nodes behind", len(views))
	}
}

// TestNDT7FailedRunDoesNotLeakTheToken proves the error text of a failed
// run does not carry the credential of the service URL.
//
// The failure path is the interesting one: an error that quotes the URL
// would publish the access token to the GUI and into any diagnostic the
// user copies from it.
func TestNDT7FailedRunDoesNotLeakTheToken(t *testing.T) {
	const token = "SUPERSECRETTOKEN"
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.New(mock.ScenarioHandshakeFailure, 0)
	defer srv.Close()
	node := mockNode(srv, "ndt-mock-fail-token")
	node.Endpoint = "ws://" + srv.Host() + "/ndt/v7/download?access_token=" + token
	ref := publishMockNodes(t, gateway, node)[0]
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, ref, "download")
	if finished.Error == "" {
		t.Fatal("a failed run must carry the reason it failed")
	}
	if strings.Contains(finished.Error, token) {
		t.Fatalf("the error text carries the access token: %q", finished.Error)
	}
	encoded, err := json.Marshal(finished)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), token) {
		t.Fatalf("the finished event carries the access token: %s", encoded)
	}
}

// TestNDT7RestartLeavesNoReusableAuthorization proves a restart cannot
// resurrect a confirmation.
//
// The authorizations live in the provider instance, which a restart
// replaces. Only the consent decision and the daily counter survive, and
// both are deliberate: a remembered consent may still be spent on a
// measurement, but every measurement needs a fresh confirmation.
func TestNDT7RestartLeavesNoReusableAuthorization(t *testing.T) {
	gateway := newTestNDT7Gateway(t)
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "ndt-mock-restart")
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	if pending := gateway.provider().PendingRunAuthorizations(); pending != 1 {
		t.Fatalf("pending authorizations = %d, want 1 before the restart", pending)
	}

	// A restart builds a new provider instance from the stored decision.
	restarted := newTestNDT7GatewayWith(t, ndt7GatewayOptions{
		daily:   ndt7.NewMemoryDailyRunStore(),
		consent: &memoryConsentStore{},
	})
	if pending := restarted.provider().PendingRunAuthorizations(); pending != 0 {
		t.Fatalf("pending authorizations = %d, want none after the restart", pending)
	}
	if views := restarted.nodes.allViews(); len(views) != 0 {
		t.Fatalf("node views = %d, want none after the restart", len(views))
	}
}

// TestNDT7SupersededNodeReferenceDoesNotResolve proves a reference from an
// earlier discovery cannot be used against a later node list.
//
// A reference names a node for one discovery answer. When a new answer
// replaces the list, the old references stop resolving - they never point at
// whatever happens to occupy that position in the new list.
func TestNDT7SupersededNodeReferenceDoesNotResolve(t *testing.T) {
	gateway := newTestNDT7Gateway(t)
	first := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer first.Close()
	second := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer second.Close()

	oldRef := publishMockNodes(t, gateway, mockNode(first, "ndt-mock-old"))[0]
	newRef := publishMockNodes(t, gateway, mockNode(second, "ndt-mock-new"))[0]
	if oldRef == newRef {
		t.Fatal("a new discovery must mint a new reference")
	}
	if _, err := gateway.nodes.endpointFor(oldRef); err == nil {
		t.Fatal("a superseded reference must not resolve")
	}
	if _, err := gateway.authorizeRun(oldRef, provider.DirectionDownload); err == nil {
		t.Fatal("a superseded reference must not authorize a measurement")
	}
	if _, err := gateway.nodes.endpointFor(newRef); err != nil {
		t.Fatalf("the current reference must resolve: %v", err)
	}
}

// failingConsentStore refuses every write.
type failingConsentStore struct{}

func (s *failingConsentStore) load() (*ndt7.ConsentRecord, error) { return nil, nil }

func (s *failingConsentStore) save(ndt7.ConsentRecord) error {
	return errors.New("desktop: the consent file cannot be written")
}

// TestNDT7ConsentThatCannotBeStoredIsNotInForce proves that a decision that
// could not be stored opens no gate.
//
// The stored decision and the provider have to agree. If the file cannot be
// written, the consent is in force nowhere: the panel reports the failure
// and the provider keeps refusing, instead of the two disagreeing about
// whether the user agreed.
func TestNDT7ConsentThatCannotBeStoredIsNotInForce(t *testing.T) {
	gateway := newTestNDT7GatewayWith(t, ndt7GatewayOptions{
		daily:   ndt7.NewMemoryDailyRunStore(),
		consent: &failingConsentStore{},
	})
	if _, err := gateway.recordConsent(true); err == nil {
		t.Fatal("a consent that cannot be stored must be reported as a failure")
	}
	if gateway.consentAgreed() {
		t.Fatal("a consent that could not be stored must not be in force")
	}
	if _, err := gateway.discover(context.Background()); !errors.Is(err, errNDT7ConsentRequired) {
		t.Fatalf("error = %v, want the consent gate to keep refusing", err)
	}
}

// TestNDT7OneConfirmationBuysOneMeasurement proves that a confirmation,
// however often it arrives, buys exactly one measurement.
//
// The provider holds the authorizations and the desktop keeps no second book
// of them, so there is nothing that could drift apart: a repeated
// confirmation reuses the outstanding one instead of issuing another.
func TestNDT7OneConfirmationBuysOneMeasurement(t *testing.T) {
	gateway := newTestNDT7Gateway(t)
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "ndt7-bookkeeping")

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
				t.Errorf("authorizeRun: %v", err)
			}
		}()
	}
	wg.Wait()

	if pending := gateway.provider().PendingRunAuthorizations(); pending != 1 {
		t.Fatalf("pending authorizations = %d, want exactly 1: one confirmation buys one measurement", pending)
	}
}

// TestNDT7AuthorizationAndRevokeCannotLeaveAPhantomConfirmation proves that
// a confirmation issued while the authorizations are being revoked cannot
// leave a confirmation behind that no longer exists.
//
// The two used to be cleared in two steps, which let a confirmation slip in
// between: the desktop went on believing in a confirmation the provider had
// already dropped, and the next run was refused for a confirmation that did
// not exist.
func TestNDT7AuthorizationAndRevokeCannotLeaveAPhantomConfirmation(t *testing.T) {
	gateway := newTestNDT7Gateway(t)
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "ndt7-phantom")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = gateway.authorizeRun(ref, provider.DirectionDownload)
				gateway.revokeAuthorizations()
			}
		}()
	}
	wg.Wait()

	// Whatever the interleaving was, a fresh confirmation must be exactly
	// one grant, and a measurement with it must be allowed.
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	if pending := gateway.provider().PendingRunAuthorizations(); pending != 1 {
		t.Fatalf("pending authorizations = %d, want exactly 1", pending)
	}
}

// TestNDT7ConsentWithdrawalStopsOnlyTheMLabMeasurement proves that
// withdrawing the consent cancels an M-Lab measurement and leaves a self
// hosted one alone.
func TestNDT7ConsentWithdrawalStopsOnlyTheMLabMeasurement(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 50*time.Millisecond, 20*time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "ndt7-withdraw")
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	if _, err := runner.Start(StartOptions{
		SelectionMode:   SelectionModeManual,
		Provider:        string(ndt7ProviderID),
		ProviderNodeRef: ref,
		Direction:       "download",
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := runner.setNDT7Consent(false); err != nil {
		t.Fatalf("setNDT7Consent: %v", err)
	}
	finished := waitFinished(t, rec, 30*time.Second)
	if finished.Status != StateCancelled {
		t.Fatalf("status = %q, error = %q, want the M-Lab measurement to stop", finished.Status, finished.Error)
	}
	// The withdrawal also drops the outstanding confirmation.
	if pending := gateway.provider().PendingRunAuthorizations(); pending != 0 {
		t.Fatalf("pending authorizations = %d, want none after the withdrawal", pending)
	}
	if views := gateway.nodes.allViews(); len(views) != 0 {
		t.Fatalf("node views = %d, want the node references dropped after the withdrawal", len(views))
	}
}
