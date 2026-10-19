// This file holds the IN-PACKAGE tests for internal/provider/ndt7.
//
// They live here rather than in runner_test.go (which is an external
// `package ndt7_test` file) precisely so that they can call the
// unexported helpers directly. Testing them through the exported API
// would have forced us to widen the production surface with
// `...ForTest` shims, which is exactly the kind of change that makes a
// package's API grow for testing reasons alone.
//
// Everything asserted here is deterministic: no wall-clock racing, no
// sockets, no sleeps, and no dependence on how fast the machine runs.
package ndt7

import (
	"testing"
	"time"
)

// TestWindowRejectReasonCoversEveryDegenerateCase asserts the full
// decision table of the measurement-window guard.
//
// These four cases cannot be provoked reliably through a real
// WebSocket run, because reaching them means beating the SDK's 250 ms
// UpdateInterval. Asserting them here means the guard cannot silently
// regress to publishing a rate (or a fake zero rate) in any of them.
func TestWindowRejectReasonCoversEveryDegenerateCase(t *testing.T) {
	cases := []struct {
		name              string
		firstSeen         bool
		counterRegressed  bool
		timeRegressed     bool
		windowUs          int64
		wantRejected      bool
		wantReasonSnippet string
	}{
		{
			name:         "healthy window is publishable",
			firstSeen:    true,
			windowUs:     500_000,
			wantRejected: false,
		},
		{
			// The download loop flushes exactly one client frame when it
			// exits early, so a run can legitimately observe a single
			// frame. A single frame carries no delta at all.
			name:              "no client frame at all",
			firstSeen:         false,
			windowUs:          0,
			wantRejected:      true,
			wantReasonSnippet: "no client-origin measurement frame",
		},
		{
			name:              "byte counter moved backwards",
			firstSeen:         true,
			counterRegressed:  true,
			windowUs:          500_000,
			wantRejected:      true,
			wantReasonSnippet: "byte counter moved backwards",
		},
		{
			name:              "elapsed clock moved backwards",
			firstSeen:         true,
			timeRegressed:     true,
			windowUs:          500_000,
			wantRejected:      true,
			wantReasonSnippet: "elapsed-time counter moved backwards",
		},
		{
			// A zero-length window is the case the whole guard exists
			// for: dividing bytes by zero would either panic or produce
			// an infinity that a UI would happily render.
			name:              "zero-length window",
			firstSeen:         true,
			windowUs:          0,
			wantRejected:      true,
			wantReasonSnippet: "zero-length",
		},
		{
			name:              "negative window",
			firstSeen:         true,
			windowUs:          -1,
			wantRejected:      true,
			wantReasonSnippet: "zero-length",
		},
		{
			// Precedence matters: a regression must be reported as a
			// regression, not as a zero window, because the two call for
			// different investigations.
			name:              "regression outranks zero window",
			firstSeen:         true,
			counterRegressed:  true,
			windowUs:          0,
			wantRejected:      true,
			wantReasonSnippet: "byte counter moved backwards",
		},
		{
			name:              "clock regression outranks zero window",
			firstSeen:         true,
			timeRegressed:     true,
			windowUs:          0,
			wantRejected:      true,
			wantReasonSnippet: "elapsed-time counter moved backwards",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := windowRejectReason(tc.firstSeen, tc.counterRegressed, tc.timeRegressed, tc.windowUs)
			if tc.wantRejected {
				if got == "" {
					t.Fatalf("windowRejectReason(...) returned an empty string (publishable), want a rejection reason")
				}
				if !contains(got, tc.wantReasonSnippet) {
					t.Fatalf("reason %q does not mention %q", got, tc.wantReasonSnippet)
				}
				return
			}
			if got != "" {
				t.Fatalf("windowRejectReason(...) = %q, want an empty string (publishable)", got)
			}
		})
	}
}

// TestMbsFromBytesRefusesNonMeasurableInputs pins the arithmetic that
// produces the published rate.
//
// The adapter uses 0 as its "not measured" sentinel and the runner
// turns that into a nil *float64. That split is only sound if 0 can
// never be produced by a real calculation, which is what this asserts.
func TestMbsFromBytesRefusesNonMeasurableInputs(t *testing.T) {
	cases := []struct {
		name    string
		bytes   int64
		elapsed time.Duration
		want    float64
	}{
		{"zero window", 1024, 0, 0},
		{"negative window", 1024, -5 * time.Microsecond, 0},
		{"zero bytes over a real window", 0, time.Second, 0},
		{"one mebibyte per second", 1 << 20, time.Second, 8.388608},
		{"one megabyte per second", 1000000, time.Second, 8},
		{"ten gigabits per second", 1250e6, time.Second, 10000},
		{"sub-millisecond window", 1 << 20, 100 * time.Microsecond, 83886.08},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mbpsFromBytes(tc.bytes, tc.elapsed)
			// Relative tolerance: the exact values are not all
			// representable in binary floating point.
			tol := tc.want * 1e-9
			if tol < 1e-9 {
				tol = 1e-9
			}
			if diff := got - tc.want; diff > tol || diff < -tol {
				t.Fatalf("mbpsFromBytes(%d, %v) = %v, want %v (tol %v)",
					tc.bytes, tc.elapsed, got, tc.want, tol)
			}
		})
	}
}

// TestMbsFromBytesNeverReturnsInfinityOrNaN pins the property that
// matters most for a value that reaches a UI: for every input the
// function can actually receive from a published window, the result is
// finite and non-negative.
//
// A negative byte count is deliberately absent from the input table.
// mbpsFromBytes itself is a pure arithmetic helper and would return a
// negative number for one; the invariant is enforced one level up, in
// Adapter.Run, which refuses to publish a window whose deltaBytes went
// negative (a counter regression). Asserting it here instead would
// force a defensive clamp into production code purely to satisfy a
// test, which is the wrong place for the guarantee.
func TestMbsFromBytesNeverReturnsInfinityOrNaN(t *testing.T) {
	inputs := []struct {
		bytes   int64
		elapsed time.Duration
	}{
		// The pathological pair that would divide by ~zero.
		{1 << 62, 1},
		{1, 1},
		{1 << 62, time.Hour},
		// Degenerate, but both are handled before this point is reached.
		{0, 0},
		{0, time.Second},
		// A zero-length window is the case the guard exists for.
		{1024, 0},
		{1 << 20, -1},
	}
	for _, in := range inputs {
		got := mbpsFromBytes(in.bytes, in.elapsed)
		if got != got { // NaN
			t.Fatalf("mbpsFromBytes(%d, %v) = NaN", in.bytes, in.elapsed)
		}
		if got > 1e300 || got < -1e300 {
			t.Fatalf("mbpsFromBytes(%d, %v) = %v, want a finite value", in.bytes, in.elapsed, got)
		}
		if got < 0 {
			t.Fatalf("mbpsFromBytes(%d, %v) = %v, want >= 0", in.bytes, in.elapsed, got)
		}
	}
}

// contains is a tiny substring helper, kept local so this file does not
// add an import to the non-test package.
func contains(haystack, needle string) bool {
	return len(needle) == 0 || indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
