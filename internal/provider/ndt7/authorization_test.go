package ndt7_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7/mock"
)

// fakeCredential builds an unsigned JWT shaped like the one a Locate answer
// carries. It is fabricated locally and never parsed by anything under
// test; it exists so a leak assertion has something recognisable to find.
func fakeCredential(expiry time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]int64{"exp": expiry.Unix()})
	if err != nil {
		panic(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(claims) + ".fake-signature"
}

// rawRunner returns a consented runner WITHOUT the per-run authorization
// wrapper the behavioural tests use. Every test here needs to drive that
// gate by hand, including the ones that must show it stays shut.
func rawRunner(t *testing.T) *ndt7.Runner {
	t.Helper()
	r := ndt7.NewRunnerWithConsent(recordedConsent())
	// An in-memory daily counter keeps these tests off the developer's real
	// per-user counter file; the AUP limit has its own tests.
	r.SetDailyRunStore(ndt7.NewMemoryDailyRunStore())
	return r
}

func downloadRequest(endpoint, nodeID string) provider.RunRequest {
	return provider.RunRequest{
		Node:         nodeOf(endpoint, nodeID),
		Capabilities: provider.CapabilitySet{Supported: map[provider.Capability]bool{provider.CapDownload: true}},
		Directions:   []provider.Direction{provider.DirectionDownload},
		Duration:     5 * time.Second,
		Budget:       provider.BudgetLimits{DownloadBytes: rateTestBudget},
	}
}

// assertNoTraffic fails when the mock server saw anything at all. "Zero
// bytes" is the property that matters here, so it is asserted at the
// connection, not at the reported result.
func assertNoTraffic(t *testing.T, srv *mock.Server) {
	t.Helper()
	snap := srv.Snapshot()
	if snap.Connections != 0 || snap.Requests != 0 || snap.BytesWritten != 0 {
		t.Fatalf("a refused run touched the network: %+v", snap)
	}
}

// TestStoredDiscoveryConsentAloneNeverMeasures pins the split between the
// two decisions.
//
// The runner has a valid, current consent record - the record a previous
// Locate discovery wrote - and no per-measurement authorization. The run
// must be refused before the socket opens.
func TestStoredDiscoveryConsentAloneNeverMeasures(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	r := rawRunner(t)
	req := downloadRequest("ws://"+srv.Host(), "consent-only")

	result, err := r.Run(context.Background(), req)
	if !errors.Is(err, ndt7.ErrRunAuthorizationRequired) {
		t.Fatalf("Run err = %v, want ErrRunAuthorizationRequired", err)
	}
	if result.Status != provider.StatusConsentNeeded {
		t.Fatalf("status = %q, want %q", result.Status, provider.StatusConsentNeeded)
	}
	if result.ErrorCode != "measurement_not_authorized" {
		t.Fatalf("error code = %q, want measurement_not_authorized", result.ErrorCode)
	}
	if result.Download != nil || result.Upload != nil {
		t.Fatalf("a refused run must publish no transfer: %+v", result)
	}
	assertNoTraffic(t, srv)
}

// TestMissingAuthorizationProducesZeroTraffic is the same guarantee stated
// as the property a reviewer cares about: a run nobody authorized moves no
// bytes, whatever the result claims.
func TestMissingAuthorizationProducesZeroTraffic(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	r := rawRunner(t)
	for i := 0; i < 3; i++ {
		if _, err := r.Run(context.Background(), downloadRequest("ws://"+srv.Host(), "no-auth")); !errors.Is(err, ndt7.ErrRunAuthorizationRequired) {
			t.Fatalf("attempt %d: err = %v, want ErrRunAuthorizationRequired", i, err)
		}
	}
	assertNoTraffic(t, srv)
}

// TestRefusedConfirmationLeavesNoAuthorization covers the refusal path: a
// grant issued for a dialog the user then dismissed must not survive.
func TestRefusedConfirmationLeavesNoAuthorization(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	r := rawRunner(t)
	if _, err := r.AuthorizeRun("refused", provider.DirectionDownload); err != nil {
		t.Fatalf("AuthorizeRun: %v", err)
	}
	if got := r.PendingRunAuthorizations(); got != 1 {
		t.Fatalf("PendingRunAuthorizations = %d, want 1", got)
	}
	r.RevokeRunAuthorization()
	if got := r.PendingRunAuthorizations(); got != 0 {
		t.Fatalf("PendingRunAuthorizations after revoke = %d, want 0", got)
	}
	if _, err := r.Run(context.Background(), downloadRequest("ws://"+srv.Host(), "refused")); !errors.Is(err, ndt7.ErrRunAuthorizationRequired) {
		t.Fatalf("Run err = %v, want ErrRunAuthorizationRequired", err)
	}
	assertNoTraffic(t, srv)
}

// TestAuthorizationPermitsExactlyOneRun is the positive case and the
// single-use case in one: the authorized run really reaches the server, and
// the next unconfirmed run is refused with zero additional traffic.
func TestAuthorizationPermitsExactlyOneRun(t *testing.T) {
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, streamDuration)
	defer srv.Close()

	r := rawRunner(t)
	req := downloadRequest("ws://"+srv.Host(), "authorized-once")

	grant, err := r.AuthorizeRun(req.Node.ID, provider.DirectionDownload)
	if err != nil {
		t.Fatalf("AuthorizeRun: %v", err)
	}
	if grant.NodeID != req.Node.ID || grant.Direction != provider.DirectionDownload {
		t.Fatalf("grant = %+v, want it bound to %q/download", grant, req.Node.ID)
	}

	result, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("authorized Run: %v (status=%q)", err, result.Status)
	}
	if result.Status != provider.StatusCompleted {
		t.Fatalf("status = %q, want completed", result.Status)
	}
	if got := srv.Snapshot().Connections; got != 1 {
		t.Fatalf("the authorized run made %d connections, want exactly 1", got)
	}
	if got := r.PendingRunAuthorizations(); got != 0 {
		t.Fatalf("PendingRunAuthorizations after the run = %d, want 0", got)
	}

	// The same node, the same direction, no new confirmation.
	if _, err := r.Run(context.Background(), req); !errors.Is(err, ndt7.ErrRunAuthorizationRequired) {
		t.Fatalf("reused authorization err = %v, want ErrRunAuthorizationRequired", err)
	}
	if got := srv.Snapshot().Connections; got != 1 {
		t.Fatalf("the reused authorization opened a second connection (%d total)", got)
	}
}

// TestAuthorizationIsBoundToNodeAndDirection shows that a confirmation for
// one measurement cannot be redirected at another.
func TestAuthorizationIsBoundToNodeAndDirection(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	r := rawRunner(t)
	endpoint := "ws://" + srv.Host()
	if _, err := r.AuthorizeRun("node-a", provider.DirectionDownload); err != nil {
		t.Fatalf("AuthorizeRun: %v", err)
	}

	if _, err := r.Run(context.Background(), downloadRequest(endpoint, "node-b")); !errors.Is(err, ndt7.ErrRunAuthorizationMismatch) {
		t.Fatalf("other node err = %v, want ErrRunAuthorizationMismatch", err)
	}
	upload := provider.RunRequest{
		Node:         nodeOf(endpoint, "node-a"),
		Capabilities: provider.CapabilitySet{Supported: map[provider.Capability]bool{provider.CapUpload: true}},
		Directions:   []provider.Direction{provider.DirectionUpload},
		Duration:     5 * time.Second,
		Budget:       provider.BudgetLimits{UploadBytes: rateTestBudget},
	}
	if _, err := r.Run(context.Background(), upload); !errors.Is(err, ndt7.ErrRunAuthorizationMismatch) {
		t.Fatalf("other direction err = %v, want ErrRunAuthorizationMismatch", err)
	}
	assertNoTraffic(t, srv)

	// A refused mismatch must not burn the authorization the user gave: it
	// still covers the run it was actually taken for.
	if got := r.PendingRunAuthorizations(); got != 1 {
		t.Fatalf("PendingRunAuthorizations = %d, want the original authorization to survive", got)
	}
}

// TestCancelledTransferDoesNotProduceAReusableAuthorization pins the rule
// for a cancellation that arrives while the run is in flight: the socket was
// opened, the server recorded it, and the confirmation is spent.
func TestCancelledTransferDoesNotProduceAReusableAuthorization(t *testing.T) {
	srv := mock.NewWithChunkDelay(mock.ScenarioDownload, 0, 30*time.Millisecond)
	defer srv.Close()

	r := rawRunner(t)
	if _, err := r.AuthorizeRun("cancel-in-flight", provider.DirectionDownload); err != nil {
		t.Fatalf("AuthorizeRun: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := downloadRequest("ws://"+srv.Host(), "cancel-in-flight")
	req.Duration = 30 * time.Second

	// Cancel once the server really has a connection, rather than after a
	// guessed delay: the cancellation only means something if the run
	// already passed the authorization gate and the transfer gate.
	type outcome struct {
		result provider.FinalResult
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, err := r.Run(ctx, req)
		finished <- outcome{result, err}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for srv.Snapshot().Connections == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()

	got := <-finished
	if got.result.Status != provider.StatusCancelled {
		t.Fatalf("status = %q, want cancelled (err=%v)", got.result.Status, got.err)
	}
	if got := r.PendingRunAuthorizations(); got != 0 {
		t.Fatalf("a cancelled transfer left %d reusable authorization(s)", got)
	}
	if _, err := r.Run(context.Background(), req); !errors.Is(err, ndt7.ErrRunAuthorizationRequired) {
		t.Fatalf("retry after cancellation err = %v, want ErrRunAuthorizationRequired", err)
	}
}

// TestPreCancelledRunSpendsNothing states the other half of the rule. A run
// refused before it starts touches no network, consumes no daily slot and
// creates no authorization; the confirmation the user already gave is left
// alone, and a GUI that abandons it revokes it explicitly.
func TestPreCancelledRunSpendsNothing(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	counter := ndt7.NewMemoryDailyRunStore()
	r := rawRunner(t)
	r.SetDailyRunStore(counter)

	if _, err := r.AuthorizeRun("pre-cancelled", provider.DirectionDownload); err != nil {
		t.Fatalf("AuthorizeRun: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := r.Run(ctx, downloadRequest("ws://"+srv.Host(), "pre-cancelled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run err = %v, want context.Canceled", err)
	}
	if got := r.PendingRunAuthorizations(); got != 1 {
		t.Fatalf("a run that never started changed the pending authorizations to %d, want 1", got)
	}
	used, err := counter.Used(time.Now())
	if err != nil {
		t.Fatalf("Used: %v", err)
	}
	if used != 0 {
		t.Fatalf("a run that never started counted %d measurement(s)", used)
	}
	assertNoTraffic(t, srv)

	// The user's confirmation is still the one they gave, so the run it was
	// taken for is no longer refused - until the GUI tells the runner the
	// user abandoned it.
	if _, err := r.Run(context.Background(), downloadRequest("ws://"+srv.Host(), "pre-cancelled")); errors.Is(err, ndt7.ErrRunAuthorizationRequired) || errors.Is(err, ndt7.ErrRunAuthorizationMismatch) {
		t.Fatalf("the surviving confirmation did not authorize its own run: %v", err)
	}
	r.RevokeRunAuthorization()
	if _, err := r.Run(context.Background(), downloadRequest("ws://"+srv.Host(), "pre-cancelled")); !errors.Is(err, ndt7.ErrRunAuthorizationRequired) {
		t.Fatalf("Run after revoke err = %v, want ErrRunAuthorizationRequired", err)
	}
}

// TestFailedRunDoesNotProduceAReusableAuthorization is the same rule for a
// run that fails at the transport rather than at the context.
func TestFailedRunDoesNotProduceAReusableAuthorization(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	endpoint := "ws://" + srv.Host()
	srv.Close() // nothing is listening any more

	r := rawRunner(t)
	if _, err := r.AuthorizeRun("failed", provider.DirectionDownload); err != nil {
		t.Fatalf("AuthorizeRun: %v", err)
	}
	if _, err := r.Run(context.Background(), downloadRequest(endpoint, "failed")); err == nil {
		t.Fatal("a run against a closed port must fail")
	}
	if got := r.PendingRunAuthorizations(); got != 0 {
		t.Fatalf("a failed run left %d reusable authorization(s)", got)
	}
	if _, err := r.Run(context.Background(), downloadRequest(endpoint, "failed")); !errors.Is(err, ndt7.ErrRunAuthorizationRequired) {
		t.Fatalf("retry after failure err = %v, want ErrRunAuthorizationRequired", err)
	}
}

// TestAuthorizationIssuesAreAccountedFor checks the count a GUI displays.
func TestAuthorizationIssuesAreAccountedFor(t *testing.T) {
	r := rawRunner(t)
	if got := r.PendingRunAuthorizations(); got != 0 {
		t.Fatalf("a fresh runner has %d pending authorizations", got)
	}
	for i := 0; i < 3; i++ {
		if _, err := r.AuthorizeRun("node", provider.DirectionDownload); err != nil {
			t.Fatalf("AuthorizeRun %d: %v", i, err)
		}
	}
	if got := r.PendingRunAuthorizations(); got != 3 {
		t.Fatalf("PendingRunAuthorizations = %d, want 3", got)
	}
	first, err := r.AuthorizeRun("node", provider.DirectionDownload)
	if err != nil {
		t.Fatalf("AuthorizeRun: %v", err)
	}
	if got := r.PendingRunAuthorizations(); got != 4 {
		t.Fatalf("PendingRunAuthorizations = %d, want 4", got)
	}
	// Sequence numbers increase, so a confirmation dialog can be tied to
	// the run it authorized.
	grant, err := r.AuthorizeRun("node", provider.DirectionUpload)
	if err != nil {
		t.Fatalf("AuthorizeRun: %v", err)
	}
	if grant.Seq <= first.Seq {
		t.Fatalf("grant sequence did not advance: %d then %d", first.Seq, grant.Seq)
	}
}

// TestAuthorizeRunRejectsUnusableRequests keeps the authorization itself
// honest: it cannot be issued for an empty node or a direction the protocol
// does not carry.
func TestAuthorizeRunRejectsUnusableRequests(t *testing.T) {
	r := rawRunner(t)
	if _, err := r.AuthorizeRun("   ", provider.DirectionDownload); err == nil {
		t.Fatal("an authorization without a node must be refused")
	}
	if _, err := r.AuthorizeRun("node", "sideways"); !errors.Is(err, ndt7.ErrUnsupportedDirection) {
		t.Fatalf("err = %v, want ErrUnsupportedDirection", err)
	}
	if got := r.PendingRunAuthorizations(); got != 0 {
		t.Fatalf("a refused authorization left %d pending", got)
	}
}

// TestAuthorizationErrorsCarryNoCredential checks the text that reaches
// the GUI and any log file. The node list is built from a real Locate
// answer, token included, so a message that echoed an endpoint would
// publish a usable credential.
func TestAuthorizationErrorsCarryNoCredential(t *testing.T) {
	token := fakeCredential(time.Now().Add(time.Hour))
	endpoint := "wss://ndt-mlab2-hkg03.mlab-oti.measurement-lab.org:443/ndt/v7/download?access_token=" + token + "&index=0"
	r := rawRunner(t)

	if _, err := r.AuthorizeRun("ndt-mlab2-hkg03", provider.DirectionDownload); err != nil {
		t.Fatalf("AuthorizeRun: %v", err)
	}
	req := downloadRequest(endpoint, "ndt-mlab2-hkg03")
	req.Node.Name = "ndt-mlab2-hkg03.mlab-oti.measurement-lab.org (Hong Kong, HK)"

	_, mismatchErr := r.Run(context.Background(), downloadRequest(endpoint, "some-other-node"))
	if mismatchErr == nil {
		t.Fatal("expected the mismatched node to be refused")
	}
	_, missingErr := r.Run(context.Background(), req)
	if missingErr == nil {
		t.Fatal("expected the consumed authorization to be refused")
	}
	grant, err := r.AuthorizeRun("ndt-mlab2-hkg03", provider.DirectionDownload)
	if err != nil {
		t.Fatalf("AuthorizeRun: %v", err)
	}

	for _, text := range []string{
		mismatchErr.Error(),
		missingErr.Error(),
		fmt.Sprintf("%+v", grant),
	} {
		if strings.Contains(text, token) {
			t.Fatalf("authorization text leaks the access_token: %s", text)
		}
		if strings.Contains(text, "access_token") {
			t.Fatalf("authorization text names the credential: %s", text)
		}
	}
}

// TestRefusedRunCarriesNoEndpointInItsResult covers the other text path:
// provider.FinalResult travels into the GUI and into result files.
func TestRefusedRunCarriesNoEndpointInItsResult(t *testing.T) {
	token := fakeCredential(time.Now().Add(time.Hour))
	endpoint := "wss://ndt-mlab2-hkg03.mlab-oti.measurement-lab.org:443/ndt/v7/download?access_token=" + token + "&index=0"

	r := rawRunner(t)
	result, err := r.Run(context.Background(), downloadRequest(endpoint, "ndt-mlab2-hkg03"))
	if !errors.Is(err, ndt7.ErrRunAuthorizationRequired) {
		t.Fatalf("Run err = %v, want ErrRunAuthorizationRequired", err)
	}
	raw, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatalf("marshal result: %v", marshalErr)
	}
	if strings.Contains(string(raw), token) || strings.Contains(string(raw), "access_token") {
		t.Fatalf("the refused result leaks the credential: %s", raw)
	}
}
