package main

// The offline half of the 26 item GUI acceptance list.
//
// Each test below is named after the acceptance item it covers and proves
// the part of that item that can be proven without a human and without the
// public network. The items that need the public network or a real window
// are NOT simulated here: they stay in docs/WAN-01-GUI-ACCEPTANCE.md with
// their honest status.
//
// What this file never does:
//   - it never contacts M-Lab (no Locate query, no measurement),
//   - it never spends a daily measurement slot (the daily counter in these
//     tests is a local file or an in-memory counter, and no measurement ever
//     reaches a real server),
//   - it never weakens a gate to make a test pass.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7/mock"
)

// TestAcceptance01_StartupIsInert proves a fresh client reaches for nothing.
//
// The acceptance item asks that a first start does not contact M-Lab. The
// desktop has no HTTP client of its own on that path at all - the
// structural half of that is asserted by
// TestNDT7DiscoveryPathIssuesNoRequestOfItsOwn - and the behavioural half
// is asserted here: without a consent the discovery is refused before the
// provider builds a request.
func TestAcceptance01_StartupIsInert(t *testing.T) {
	gateway := newTestNDT7Gateway(t)
	state := gateway.state()
	if state.Agreed || !state.ConsentRequired {
		t.Fatalf("state = %+v, want a fresh client to be undecided", state)
	}
	if _, err := gateway.discover(context.Background()); !errors.Is(err, errNDT7ConsentRequired) {
		t.Fatalf("discovery error = %v, want the consent gate to refuse it", err)
	}
	if state.DailyUsed != 0 {
		t.Fatalf("daily used = %d, want 0 on a fresh client", state.DailyUsed)
	}
}

// TestAcceptance02_RunWithoutConsentIsRefused proves the backend refuses a
// measurement that the user never agreed to. The mock server counts the
// connections: it must stay at zero.
func TestAcceptance02_RunWithoutConsentIsRefused(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "acceptance-02")
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
		t.Fatalf("connections = %d, want none", got)
	}
}

// TestAcceptance03_NoticeIsCompleteAndVersioned proves the disclosure the
// GUI shows is the whole thing and names its version.
//
// The GUI renders the text the backend hands it, so the property that
// matters is that this text is complete and versioned.
func TestAcceptance03_NoticeIsCompleteAndVersioned(t *testing.T) {
	notice := newTestNDT7Gateway(t).state().Notice
	if !strings.Contains(notice, ndt7.ConsentVersion()) {
		t.Fatalf("the notice must name its version %q", ndt7.ConsentVersion())
	}
	// Every numbered item of the disclosure has to be there.
	for i := 1; i <= 10; i++ {
		marker := fmt.Sprintf("\n%d. ", i)
		if !strings.Contains(notice, marker) {
			t.Fatalf("the notice is missing item %d", i)
		}
	}
	for _, fact := range []string{
		"Public IP disclosure",
		"per-run authorization is not remembered",
		fmt.Sprintf("at most %d NDT7 measurements per calendar day", ndt7.MaxDailyRuns),
		"still counts against the daily limit",
		"What GoSpeed keeps on this computer",
		"itself an external request",
	} {
		if !strings.Contains(notice, fact) {
			t.Fatalf("the notice does not state %q", fact)
		}
	}
}

// TestAcceptance04_ConsentIsPersistedAndHoldsNoCredential proves the stored
// decision exists on disk, survives a reload, and contains nothing but the
// decision itself.
func TestAcceptance04_ConsentIsPersistedAndHoldsNoCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ndt7-consent.json")
	store := &fileConsentStore{path: path}
	record := ndt7.ConsentRecord{
		Agreed:        true,
		AgreedAt:      time.Now(),
		PolicyVersion: ndt7.ConsentVersion(),
		ClientName:    ndt7.ClientName,
		ClientVersion: ndt7.ClientVersion,
	}
	if err := store.save(record); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, forbidden := range []string{"access_token", "wss://", "http://", "mlab-oti", "measurement-lab"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("the stored consent carries %q: %s", forbidden, raw)
		}
	}
	loaded, err := store.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded == nil || !loaded.Agreed || loaded.PolicyVersion != ndt7.ConsentVersion() {
		t.Fatalf("loaded = %+v, want the stored decision back", loaded)
	}
}

// TestAcceptance05_RestartKeepsTheCurrentConsent proves a restart does not
// ask again for a wording the user already accepted.
func TestAcceptance05_RestartKeepsTheCurrentConsent(t *testing.T) {
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
		t.Fatal("a consent under the current wording must survive a restart")
	}
}

// TestAcceptance06_VersionChangeForcesReconsent proves an agreement under
// an older wording is not reused.
func TestAcceptance06_VersionChangeForcesReconsent(t *testing.T) {
	store := &memoryConsentStore{}
	if err := store.save(ndt7.ConsentRecord{
		Agreed:        true,
		AgreedAt:      time.Now().Add(-time.Hour),
		PolicyVersion: "ndt7-notice-v2",
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
		t.Fatal("a consent under an older wording must not survive")
	}
	state := gateway.state()
	if !state.ConsentRequired || state.PolicyVersion != ndt7.ConsentVersion() {
		t.Fatalf("state = %+v, want the current wording to be asked again", state)
	}
}

// TestAcceptance07_DiscoveryIsRefusedWithoutConsent covers the offline half
// of the discovery item.
//
// The public part of this item - a real Locate query against M-Lab - is
// deliberately NOT executed: it would leave this machine and ask a third
// party for the client's address. What can be proven offline is that the
// query is refused without a consent, and that the answer never reaches the
// GUI in a form that carries a credential.
func TestAcceptance07_DiscoveryIsRefusedWithoutConsent(t *testing.T) {
	gateway := newTestNDT7Gateway(t)
	if _, err := gateway.discover(context.Background()); !errors.Is(err, errNDT7ConsentRequired) {
		t.Fatalf("discovery error = %v, want the consent gate to refuse it", err)
	}
	if views := gateway.nodes.allViews(); len(views) != 0 {
		t.Fatalf("node views = %d, want none", len(views))
	}
}

// TestAcceptance08_FrontendNeverSeesTheToken proves every payload the GUI
// can receive is free of a credential and of a service URL.
func TestAcceptance08_FrontendNeverSeesTheToken(t *testing.T) {
	const token = "ACCEPTANCETOKEN"
	gateway := newTestNDT7Gateway(t)
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	node := mockNode(srv, "acceptance-08")
	node.Endpoint = "ws://" + srv.Host() + "/ndt/v7/download?access_token=" + token
	publishMockNodes(t, gateway, node)

	payloads := map[string]any{
		"consent state": gateway.state(),
		"node views":    gateway.nodes.allViews(),
	}
	for name, payload := range payloads {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("%s: Marshal: %v", name, err)
		}
		text := strings.ToLower(string(encoded))
		for _, forbidden := range []string{token, "access_token", "wss://", "ws://"} {
			if strings.Contains(text, strings.ToLower(forbidden)) {
				t.Fatalf("the %s exposes %q: %s", name, forbidden, encoded)
			}
		}
	}
}

// TestAcceptance09_DownloadAuthorizationIsBound proves a download
// authorization runs a download and nothing else.
func TestAcceptance09_DownloadAuthorizationIsBound(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "acceptance-09")
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, ref, "download")
	if finished.Status != StateCompleted {
		t.Fatalf("status = %q, error = %q", finished.Status, finished.Error)
	}
	if finished.Unified == nil || finished.Unified.Download == nil {
		t.Fatalf("unified = %+v, want a download result", finished.Unified)
	}
	if finished.Unified.Upload != nil {
		t.Fatalf("unified = %+v, want no upload result in a download run", finished.Unified)
	}
}

// TestAcceptance10_UploadAuthorizationIsBound proves an upload
// authorization runs an upload and nothing else.
func TestAcceptance10_UploadAuthorizationIsBound(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioUpload, 1<<20, 100*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "acceptance-10")
	if _, err := gateway.authorizeRun(ref, provider.DirectionUpload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, ref, "upload")
	if finished.Status != StateCompleted {
		t.Fatalf("status = %q, error = %q", finished.Status, finished.Error)
	}
	if finished.Unified == nil || finished.Unified.Upload == nil {
		t.Fatalf("unified = %+v, want an upload result", finished.Unified)
	}
	if finished.Unified.Download != nil {
		t.Fatalf("unified = %+v, want no download result in an upload run", finished.Unified)
	}
}

// TestAcceptance11_AuthorizationIsSingleUse proves one confirmation buys one
// measurement.
func TestAcceptance11_AuthorizationIsSingleUse(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "acceptance-11")
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	if finished := startNDT7(t, rec, runner, ref, "download"); finished.Status != StateCompleted {
		t.Fatalf("first run status = %q, error = %q", finished.Status, finished.Error)
	}
	// The session dropped the node references, so the same node is published
	// again to reach the authorization gate - which refuses it.
	ref = publishMockNode(t, gateway, srv, "acceptance-11")
	finished := startNDT7(t, rec, runner, ref, "download")
	if finished.Status == StateCompleted {
		t.Fatal("a second measurement must not be covered by one confirmation")
	}
	if finished.Unified == nil || finished.Unified.ErrorCode != "measurement_not_authorized" {
		t.Fatalf("unified = %+v, want the measurement to be reported as not authorized", finished.Unified)
	}
	if got := srv.Snapshot().Connections; got != 1 {
		t.Fatalf("connections = %d, want the refused run to have opened none", got)
	}
}

// TestAcceptance12_AuthorizationIsBoundToTheNode proves a confirmation does
// not travel to another node.
func TestAcceptance12_AuthorizationIsBoundToTheNode(t *testing.T) {
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
	refs := publishMockNodes(t, gateway, mockNode(first, "acceptance-12-a"), mockNode(second, "acceptance-12-b"))
	if _, err := gateway.authorizeRun(refs[0], provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, refs[1], "download")
	if finished.Status == StateCompleted {
		t.Fatal("a confirmation for one node must not cover another node")
	}
	if finished.Unified == nil || finished.Unified.ErrorCode != "measurement_authorization_mismatch" {
		t.Fatalf("unified = %+v, want the mismatch to be reported", finished.Unified)
	}
	if got := second.Snapshot().Connections; got != 0 {
		t.Fatalf("connections = %d, want the refused run to have opened none", got)
	}
}

// TestAcceptance13_AuthorizationIsBoundToTheDirection proves a download
// confirmation does not pay for an upload.
func TestAcceptance13_AuthorizationIsBoundToTheDirection(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "acceptance-13")
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, ref, "upload")
	if finished.Status == StateCompleted {
		t.Fatal("a download confirmation must not cover an upload measurement")
	}
	if finished.Unified == nil || finished.Unified.ErrorCode != "measurement_authorization_mismatch" {
		t.Fatalf("unified = %+v, want the mismatch to be reported", finished.Unified)
	}
	if got := srv.Snapshot().Connections; got != 0 {
		t.Fatalf("connections = %d, want the refused run to have opened none", got)
	}
}

// TestAcceptance14_ForgedReferenceIsRejected proves an invented reference
// resolves to nothing and that the request carries no address of its own.
func TestAcceptance14_ForgedReferenceIsRejected(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	publishMockNode(t, gateway, srv, "acceptance-14")

	for _, forged := range []string{"pnode-9999", "", "../../etc/passwd", "wss://evil.example/ndt/v7/download?access_token=x"} {
		// A request the backend refuses outright never becomes a run at
		// all; a request it accepts has to be refused by the provider.
		if _, err := runner.Start(StartOptions{
			SelectionMode:   SelectionModeManual,
			Provider:        string(ndt7ProviderID),
			ProviderNodeRef: forged,
			Direction:       "download",
		}); err != nil {
			continue
		}
		finished := waitFinished(t, rec, 30*time.Second)
		if finished.Status == StateCompleted {
			t.Fatalf("the forged reference %q must not measure anything", forged)
		}
	}
	if got := srv.Snapshot().Connections; got != 0 {
		t.Fatalf("connections = %d, want none", got)
	}
}

// TestAcceptance15_RemainingCountIsReported proves the counter the GUI shows
// matches what the backend enforces.
func TestAcceptance15_RemainingCountIsReported(t *testing.T) {
	gateway := newTestNDT7Gateway(t)
	state := gateway.state()
	if !state.DailyAvailable {
		t.Fatalf("state = %+v, want the counter to be readable", state)
	}
	if state.DailyLimit != ndt7.MaxDailyRuns || state.DailyUsed != 0 || state.DailyRemaining != ndt7.MaxDailyRuns {
		t.Fatalf("state = %+v, want the full day to be available", state)
	}
	for i := 1; i <= ndt7.MaxDailyRuns; i++ {
		if _, err := gateway.daily.Reserve(time.Now()); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		state = gateway.state()
		if state.DailyUsed != i || state.DailyRemaining != ndt7.MaxDailyRuns-i {
			t.Fatalf("state = %+v, want %d used and %d remaining", state, i, ndt7.MaxDailyRuns-i)
		}
	}
}

// TestAcceptance16_DailyCeilingStopsTheRun proves the ceiling is enforced
// before a connection is opened, and that the refusal names the reason.
func TestAcceptance16_DailyCeilingStopsTheRun(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "acceptance-16")
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
}

// TestAcceptance17_ReloadDoesNotResetTheCounter proves the count survives a
// restart of the process, because it lives in a file.
func TestAcceptance17_ReloadDoesNotResetTheCounter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ndt7-daily-runs.json")
	first := newTestNDT7GatewayWith(t, ndt7GatewayOptions{
		daily:   ndt7.NewFileDailyRunStore(path),
		consent: &memoryConsentStore{},
	})
	for i := 0; i < 2; i++ {
		if _, err := first.daily.Reserve(time.Now()); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
	}
	// A new process reads the same file.
	second := newTestNDT7GatewayWith(t, ndt7GatewayOptions{
		daily:   ndt7.NewFileDailyRunStore(path),
		consent: &memoryConsentStore{},
	})
	state := second.state()
	if state.DailyUsed != 2 || state.DailyRemaining != ndt7.MaxDailyRuns-2 {
		t.Fatalf("state = %+v, want the count to survive the reload", state)
	}
}

// TestAcceptance18_CancelRevokesTheAuthorization proves a cancelled
// measurement leaves no authorization and no node reference behind.
func TestAcceptance18_CancelRevokesTheAuthorization(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 50*time.Millisecond, 20*time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "acceptance-18")
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
	if err := runner.CancelTest(); err != nil {
		t.Fatalf("CancelTest: %v", err)
	}
	finished := waitFinished(t, rec, 30*time.Second)
	if finished.Status != StateCancelled {
		t.Fatalf("status = %q, error = %q, want a cancelled run", finished.Status, finished.Error)
	}
	if pending := gateway.provider().PendingRunAuthorizations(); pending != 0 {
		t.Fatalf("pending authorizations = %d, want none", pending)
	}
	if views := gateway.nodes.allViews(); len(views) != 0 {
		t.Fatalf("node views = %d, want the node references dropped", len(views))
	}
}

// TestAcceptance19_NormalEndClearsTheNodeReferences proves a completed
// measurement leaves nothing that could carry into the next one.
func TestAcceptance19_NormalEndClearsTheNodeReferences(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "acceptance-19")
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, ref, "download")
	if finished.Status != StateCompleted {
		t.Fatalf("status = %q, error = %q", finished.Status, finished.Error)
	}
	if pending := gateway.provider().PendingRunAuthorizations(); pending != 0 {
		t.Fatalf("pending authorizations = %d, want none", pending)
	}
	if views := gateway.nodes.allViews(); len(views) != 0 {
		t.Fatalf("node views = %d, want the node references dropped", len(views))
	}
}

// TestAcceptance20_RestartLeavesNoReusableAuthorization proves a restart
// cannot resurrect a confirmation.
func TestAcceptance20_RestartLeavesNoReusableAuthorization(t *testing.T) {
	gateway := newTestNDT7Gateway(t)
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "acceptance-20")
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
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

// TestAcceptance21_ErrorsDoNotLeakTheToken proves the failure path quotes
// no part of the credential.
func TestAcceptance21_ErrorsDoNotLeakTheToken(t *testing.T) {
	const token = "ACCEPTANCETOKEN"
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.New(mock.ScenarioHandshakeFailure, 0)
	defer srv.Close()
	node := mockNode(srv, "acceptance-21")
	node.Endpoint = "ws://" + srv.Host() + "/ndt/v7/download?access_token=" + token
	ref := publishMockNodes(t, gateway, node)[0]
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, ref, "download")
	if finished.Error == "" {
		t.Fatal("a failed run must carry a reason")
	}
	if strings.Contains(finished.Error, token) {
		t.Fatalf("the error text carries the token: %q", finished.Error)
	}
	encoded, err := json.Marshal(finished)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), token) {
		t.Fatalf("the finished event carries the token: %s", encoded)
	}
}

// TestAcceptance22_DownloadResultIsHonest proves a download result says
// what it measured and what it did not.
func TestAcceptance22_DownloadResultIsHonest(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "acceptance-22")
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, ref, "download")
	unified := finished.Unified
	if unified == nil {
		t.Fatalf("finished = %+v, want a unified verdict", finished)
	}
	if unified.Download == nil || unified.Download.Mbps == nil || *unified.Download.Mbps <= 0 {
		t.Fatalf("unified = %+v, want a measured download rate", unified)
	}
	if unified.Download.ElapsedMs <= 0 || unified.Download.TransferredBytes <= 0 {
		t.Fatalf("unified = %+v, want the measured window and bytes", unified)
	}
	// The local mock does not send a TCPInfo frame, so there is no server
	// side MinRTT to report here and the latency stays absent. What must
	// hold either way: a latency that is present is a TCP MinRTT, and the
	// jitter stays absent. A real M-Lab server does send TCPInfo, and the
	// acceptance list marks that part as a public-network check.
	if unified.Latency != nil && unified.Latency.Type != provider.MetricTCPMinRTT {
		t.Fatalf("unified = %+v, want a TCP MinRTT latency and nothing else", unified)
	}
	if unified.Jitter != nil {
		t.Fatalf("unified = %+v, want no jitter: NDT7 has none", unified)
	}
	if unified.Method.Protocol != ndt7.Protocol {
		t.Fatalf("unified = %+v, want the ndt7 protocol recorded", unified)
	}
	if unified.Method.ConsentVersion != ndt7.ConsentVersion() {
		t.Fatalf("unified = %+v, want the consent version recorded", unified)
	}
	if unified.Ownership != provider.OwnershipThirdParty {
		t.Fatalf("unified = %+v, want a third party node", unified)
	}
	// The run's transferred bytes and the observed bytes are the same
	// number for a single direction run: both count the traffic that
	// really moved, not only the bytes inside the measurement window.
	if unified.Download.ObservedBytes == nil {
		t.Fatal("the observed byte count is missing")
	}
	if *unified.Download.ObservedBytes < unified.Download.TransferredBytes {
		t.Fatalf("observed bytes %d must bound the window bytes %d",
			*unified.Download.ObservedBytes, unified.Download.TransferredBytes)
	}
	if unified.TransferredBytes != *unified.Download.ObservedBytes {
		t.Fatalf("the run's transferred bytes = %d, want the observed %d",
			unified.TransferredBytes, *unified.Download.ObservedBytes)
	}
}

// TestAcceptance23_UploadResultIsHonest proves an upload result says what
// it measured.
func TestAcceptance23_UploadResultIsHonest(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioUpload, 1<<20, 100*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "acceptance-23")
	if _, err := gateway.authorizeRun(ref, provider.DirectionUpload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, ref, "upload")
	unified := finished.Unified
	if unified == nil || unified.Upload == nil {
		t.Fatalf("finished = %+v, want an upload verdict", finished)
	}
	if unified.Upload.Mbps == nil || *unified.Upload.Mbps <= 0 {
		t.Fatalf("unified = %+v, want a measured upload rate", unified)
	}
	if unified.Latency != nil && unified.Latency.Type != provider.MetricTCPMinRTT {
		t.Fatalf("unified = %+v, want a TCP MinRTT latency and nothing else", unified)
	}
	if unified.Jitter != nil {
		t.Fatalf("unified = %+v, want no jitter", unified)
	}
}

// TestAcceptance24_NoLiveSamplesAreFabricated proves the NDT7 path emits
// no progress samples.
//
// The GUI draws its live curve from progress events. NDT7 reports one
// measurement window at the end of the run, so there is nothing to sample -
// and the GUI says so instead of drawing an empty or invented curve.
func TestAcceptance24_NoLiveSamplesAreFabricated(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	if _, err := gateway.recordConsent(true); err != nil {
		t.Fatalf("recordConsent: %v", err)
	}
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, time.Second)
	defer srv.Close()
	ref := publishMockNode(t, gateway, srv, "acceptance-24")
	if _, err := gateway.authorizeRun(ref, provider.DirectionDownload); err != nil {
		t.Fatalf("authorizeRun: %v", err)
	}
	finished := startNDT7(t, rec, runner, ref, "download")
	if samples := len(rec.progress); samples != 0 {
		t.Fatalf("progress samples = %d, want none: the GUI must not draw an invented curve", samples)
	}
	// The rate still reaches the GUI - through the final verdict.
	if finished.Unified == nil || finished.Unified.Download == nil {
		t.Fatalf("finished = %+v, want the final rate", finished)
	}
}

// TestAcceptance25_SelfHostedProviderIsUnaffected proves the GoSpeed HTTP
// path still works next to the M-Lab one, against a local GoSpeed server.
func TestAcceptance25_SelfHostedProviderIsUnaffected(t *testing.T) {
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
		t.Fatalf("unified = %+v, want the GoSpeed HTTP provider", finished.Unified)
	}
	if finished.Result == nil {
		t.Fatal("the self hosted run must keep its native result")
	}
	// The live curve of the self hosted path still has samples: the two
	// providers behave differently on purpose.
	if len(rec.progress) == 0 {
		t.Fatal("the self hosted provider must still emit progress samples")
	}
}

// TestAcceptance26_StateAfterRestart proves a fresh runner is idle, has no
// session, and starts from the stored decision rather than from a leftover
// measurement.
func TestAcceptance26_StateAfterRestart(t *testing.T) {
	rec := newRecorder()
	gateway := newTestNDT7Gateway(t)
	runner := newTestNDT7Runner(t, rec, gateway)
	status := runner.Status()
	if status.Busy {
		t.Fatalf("status = %+v, want an idle runner", status)
	}
	if status.State != StateIdle {
		t.Fatalf("status = %+v, want the idle state", status)
	}
	// A restart carries no result of a previous run.
	finished := waitFinishedTimeout(t, rec, 300*time.Millisecond)
	if finished != nil {
		t.Fatalf("a fresh runner must not report a finished event: %+v", finished)
	}
	// The M-Lab panel starts from the stored decision.
	if gateway.state().Agreed {
		t.Fatal("a fresh client must start without a consent")
	}
}

// waitFinishedTimeout waits for a finished event and returns nil when the
// timeout expires without one.
func waitFinishedTimeout(t *testing.T, rec *recorder, timeout time.Duration) *FinishedEvent {
	t.Helper()
	select {
	case finished := <-rec.finished:
		return &finished
	case <-time.After(timeout):
		return nil
	}
}
