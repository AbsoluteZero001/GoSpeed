package mlabpoc_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/experiments/mlab-poc/mock"
	mlabpoc "github.com/AbsoluteZero001/GoSpeed/experiments/mlab-poc/provider"
	v2 "github.com/m-lab/locate/api/v2"
)

// fakeRemoteHost is a realistic M-Lab hostname. It is NEVER dialed in these
// tests: the remote gate rejects or authorizes before any connection is
// created, so no public network traffic occurs.
const fakeRemoteHost = "ndt-mlab1-lga01.mlab-oti.measurementlab.net"

func enabledSwitch() *mlabpoc.RemoteExperimentSwitch {
	sw := mlabpoc.NewRemoteExperimentSwitch()
	sw.Enable()
	return sw
}

func confirmedFor(host string) mlabpoc.RemoteRunConfirmation {
	return mlabpoc.RemoteRunConfirmation{
		Confirmed:     true,
		ConfirmedAt:   time.Now().UTC(),
		NoticeVersion: mlabpoc.RemoteRunNoticeVersion,
		TargetHost:    host,
	}
}

// --- P0-K-C: the remote gate --------------------------------------------------

func TestRemoteExperimentSwitchDefaultsToDisabled(t *testing.T) {
	sw := mlabpoc.NewRemoteExperimentSwitch()
	if sw.Enabled() {
		t.Fatal("experimental remote switch must default to disabled")
	}
	sw.Enable()
	if !sw.Enabled() {
		t.Fatal("Enable must turn the switch on")
	}
}

func TestRemoteGateIsClosedWithoutGate(t *testing.T) {
	server := mock.New(mock.ScenarioNormalDownload)
	defer server.Close()
	client, err := mlabpoc.NewClient(mlabpoc.Options{
		Server:  fakeRemoteHost,
		Scheme:  "wss",
		Consent: agreedConsent(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result := client.Run(context.Background(), mlabpoc.DirectionDownload)
	if result.Status != mlabpoc.StatusFailed {
		t.Fatalf("status = %q", result.Status)
	}
	if result.ErrorClass != mlabpoc.ErrorGateClosed {
		t.Fatalf("error class = %q, want remote_gate_closed", result.ErrorClass)
	}
}

func TestRemoteGateIsClosedWithDisabledSwitch(t *testing.T) {
	client, err := mlabpoc.NewClient(mlabpoc.Options{
		Server:  fakeRemoteHost,
		Scheme:  "wss",
		Consent: agreedConsent(),
		RemoteGate: &mlabpoc.RemoteGate{
			Switch: mlabpoc.NewRemoteExperimentSwitch(), // disabled
			AllowedHosts: map[string]bool{
				fakeRemoteHost: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result := client.Run(context.Background(), mlabpoc.DirectionDownload)
	if result.ErrorClass != mlabpoc.ErrorGateClosed {
		t.Fatalf("error class = %q, want remote_gate_closed", result.ErrorClass)
	}
	if !strings.Contains(result.Error, "switch is disabled") {
		t.Fatalf("error = %q", result.Error)
	}
}

func TestRemoteGateRejectsArbitraryRemoteHost(t *testing.T) {
	client, err := mlabpoc.NewClient(mlabpoc.Options{
		Server:  "speedtest.example.com:443",
		Scheme:  "wss",
		Consent: agreedConsent(),
		RemoteGate: &mlabpoc.RemoteGate{
			Switch:       enabledSwitch(),
			AllowedHosts: map[string]bool{fakeRemoteHost: true},
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result := client.Run(context.Background(), mlabpoc.DirectionDownload)
	if result.ErrorClass != mlabpoc.ErrorGateClosed {
		t.Fatalf("error class = %q, want remote_gate_closed", result.ErrorClass)
	}
	if !strings.Contains(result.Error, "did not come from validated Locate discovery") {
		t.Fatalf("error = %q", result.Error)
	}
}

func TestRemoteGateRequiresPerRunConfirmation(t *testing.T) {
	client, err := mlabpoc.NewClient(mlabpoc.Options{
		Server:  fakeRemoteHost,
		Scheme:  "wss",
		Consent: agreedConsent(),
		RemoteGate: &mlabpoc.RemoteGate{
			Switch:       enabledSwitch(),
			AllowedHosts: map[string]bool{fakeRemoteHost: true},
			// No confirmation attached.
		},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	result := client.Run(context.Background(), mlabpoc.DirectionDownload)
	if result.ErrorClass != mlabpoc.ErrorGateClosed {
		t.Fatalf("error class = %q, want remote_gate_closed", result.ErrorClass)
	}
	if !strings.Contains(result.Error, "not explicitly confirmed") {
		t.Fatalf("error = %q", result.Error)
	}
}

func TestRemoteGateRejectsWrongNoticeVersionAndStaleConfirmation(t *testing.T) {
	stale := confirmedFor(fakeRemoteHost)
	stale.NoticeVersion = "wrong-version"
	gate := &mlabpoc.RemoteGate{
		Switch:       enabledSwitch(),
		AllowedHosts: map[string]bool{fakeRemoteHost: true},
	}
	gate.Confirm(stale)
	if err := gate.Authorize(fakeRemoteHost); err == nil {
		t.Fatal("a confirmation with the wrong notice version must be rejected")
	}

	old := confirmedFor(fakeRemoteHost)
	old.ConfirmedAt = time.Now().Add(-11 * time.Minute)
	gate2 := &mlabpoc.RemoteGate{
		Switch:       enabledSwitch(),
		AllowedHosts: map[string]bool{fakeRemoteHost: true},
	}
	gate2.Confirm(old)
	if err := gate2.Authorize(fakeRemoteHost); err == nil {
		t.Fatal("a stale confirmation must be rejected")
	}
}

func TestRemoteGateAuthorizeSucceedsForValidatedHost(t *testing.T) {
	gate := &mlabpoc.RemoteGate{
		Switch:       enabledSwitch(),
		AllowedHosts: map[string]bool{fakeRemoteHost: true},
	}
	gate.Confirm(confirmedFor(fakeRemoteHost))
	if err := gate.Authorize(fakeRemoteHost); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	// A different host from the same validated list also passes.
	if err := gate.Authorize("NDT-MLAB1-LGA01.MLAB-OTI.MEASUREMENTLAB.NET"); err != nil {
		t.Fatalf("Authorize (case-insensitive): %v", err)
	}
}

func TestRemoteGateAcceptsLoopbackWithoutGate(t *testing.T) {
	// Loopback targets (all local mock testing) never consult the gate.
	server := mock.New(mock.ScenarioNormalDownload)
	defer server.Close()
	client := newTestClient(t, server, mlabpoc.Options{})
	result := client.Run(context.Background(), mlabpoc.DirectionDownload)
	if result.Status != mlabpoc.StatusCompleted {
		t.Fatalf("status = %q, error = %q", result.Status, result.Error)
	}
}

func TestNewRemoteGateDerivesAllowedHostsFromTargets(t *testing.T) {
	target := v2.Target{
		Machine:  "mlab1-lga01",
		Hostname: fakeRemoteHost,
		URLs: map[string]string{
			"wss:///ndt/v7/download": "wss://" + fakeRemoteHost + "/ndt/v7/download",
			"wss:///ndt/v7/upload":   "wss://" + fakeRemoteHost + "/ndt/v7/upload",
		},
	}
	gate := mlabpoc.NewRemoteGate(enabledSwitch(), []v2.Target{target})
	gate.Confirm(confirmedFor(fakeRemoteHost))
	if err := gate.Authorize(fakeRemoteHost); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if err := gate.Authorize("other.host.example.net"); err == nil {
		t.Fatal("hosts outside the validated list must be rejected")
	}
}

// --- per-run confirmation prompt ----------------------------------------------

func TestConfirmRemoteRunDefaultsToNo(t *testing.T) {
	testCases := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty input cancels", "\n", false},
		{"n cancels", "n\n", false},
		{"no cancels", "no\n", false},
		{"y confirms", "y\n", true},
		{"YES confirms", "Yes\n", true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			confirmation, err := mlabpoc.ConfirmRemoteRun(strings.NewReader(tc.input), &out,
				fakeRemoteHost,
				mlabpoc.DefaultDownloadBudgetBytes, mlabpoc.DefaultUploadBudgetBytes,
				mlabpoc.DefaultTotalBudgetBytes, mlabpoc.DefaultOverallTimeout)
			if err != nil {
				t.Fatalf("ConfirmRemoteRun: %v", err)
			}
			if confirmation.Confirmed != tc.want {
				t.Fatalf("confirmed = %v, want %v", confirmation.Confirmed, tc.want)
			}
			if tc.want {
				if confirmation.NoticeVersion != mlabpoc.RemoteRunNoticeVersion {
					t.Fatalf("notice version = %q", confirmation.NoticeVersion)
				}
				if confirmation.TargetHost != fakeRemoteHost {
					t.Fatalf("target host = %q", confirmation.TargetHost)
				}
			} else if !confirmation.ConfirmedAt.IsZero() {
				t.Fatal("refused confirmation must not carry a timestamp")
			}
			// The notice must disclose public data, budget and timeout risks.
			notice := out.String()
			for _, fragment := range []string{"公网 IP", "MiB", "超时", "budget_exceeded", "timeout", "ICMP", "Jitter"} {
				if !strings.Contains(notice, fragment) {
					t.Fatalf("remote run notice missing %q", fragment)
				}
			}
		})
	}
}

func TestRemoteRunNoticeShowsBudgetAndTimeout(t *testing.T) {
	notice := mlabpoc.RemoteRunNotice(fakeRemoteHost,
		mlabpoc.DefaultDownloadBudgetBytes, mlabpoc.DefaultUploadBudgetBytes,
		mlabpoc.DefaultTotalBudgetBytes, 15*time.Second)
	for _, fragment := range []string{fakeRemoteHost, "24 MiB", "16 MiB", "40 MiB", "15s"} {
		if !strings.Contains(notice, fragment) {
			t.Fatalf("remote run notice missing %q", fragment)
		}
	}
}

// TestSequentialPromptsShareOneReader reproduces the mlabremote pipe flow:
// consent and the per-run confirmation must both see their answers when the
// same io.Reader serves both prompts. The old bufio.Scanner wrappers
// swallowed the whole pipe buffer, so the second prompt always saw EOF and
// an explicit "y" turned into an implicit cancel.
func TestSequentialPromptsShareOneReader(t *testing.T) {
	reader := strings.NewReader("y\ny\n")
	var out strings.Builder

	record, err := mlabpoc.PromptConsent(reader, &out)
	if err != nil {
		t.Fatalf("PromptConsent: %v", err)
	}
	if !record.Agreed {
		t.Fatal("first prompt must consume the first y")
	}

	confirmation, err := mlabpoc.ConfirmRemoteRun(reader, &out, fakeRemoteHost,
		mlabpoc.DefaultDownloadBudgetBytes, mlabpoc.DefaultUploadBudgetBytes,
		mlabpoc.DefaultTotalBudgetBytes, mlabpoc.DefaultOverallTimeout)
	if err != nil {
		t.Fatalf("ConfirmRemoteRun: %v", err)
	}
	if !confirmation.Confirmed {
		t.Fatal("second prompt must consume the second y, not see EOF")
	}
	if confirmation.TargetHost != fakeRemoteHost {
		t.Fatalf("target host = %q", confirmation.TargetHost)
	}
}
