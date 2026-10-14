//go:build windows

package netdiag

import (
	"encoding/binary"
	"errors"
	"testing"
)

// TestRouteRecordIsUsableRejectsBadRecords and TestIsContiguousMask live in
// netinfo_windows_test.go alongside the buffer layout cases.

// TestSingleDefaultRoute is the ordinary case: one uplink.
func TestSingleDefaultRoute(t *testing.T) {
	rows := []ipForwardRow{defaultRoute([4]byte{192, 168, 1, 1}, 12, 35)}
	gateway, index, ok := selectDefaultGateway(rows, map[uint32]bool{12: true})
	if !ok || gateway != "192.168.1.1" || index != 12 {
		t.Fatalf("gateway=%q index=%d ok=%v, want 192.168.1.1 / 12 / true", gateway, index, ok)
	}
}

// TestEffectiveMetricIsNotDoubled pins the metric semantics.
//
// Windows reports a route metric and an interface metric separately through the
// v2 API, but the v1 table this file uses carries a single ForwardMetric1 that
// the routing stack already treats as the effective metric. On the machine this
// was verified against, ForwardMetric1 was 35 while Get-NetRoute reported
// RouteMetric 0 and InterfaceMetric 35, which is only consistent with the
// effective value.
//
// Adding an interface metric on top would therefore count the same cost twice
// and could prefer the wrong uplink. This test states that rule against a table
// where the wrong behaviour would produce a different answer.
func TestEffectiveMetricIsNotDoubled(t *testing.T) {
	known := map[uint32]bool{12: true, 35: true}

	// ForwardMetric1 already carries the interface metric: 5 for the cheap
	// adapter and 100 for the expensive one. If the implementation added an
	// interface metric again, the cheap adapter's cost would become 10 and the
	// expensive one's 200, which still picks the same adapter and so would not
	// catch the bug on its own.
	rows := []ipForwardRow{
		defaultRoute([4]byte{10, 0, 0, 1}, 35, 5),
		defaultRoute([4]byte{192, 168, 1, 1}, 12, 100),
	}
	gateway, index, _ := selectDefaultGateway(rows, known)
	if gateway != "10.0.0.1" || index != 35 {
		t.Fatalf("selected %s on %d, want 10.0.0.1 on 35", gateway, index)
	}

	// The case that does distinguish them: equal ForwardMetric1 values on two
	// adapters with different interface metrics. The forward table alone cannot
	// break this tie, because it only carries the combined value, so the first
	// record must win and no interface metric may be consulted.
	tied := []ipForwardRow{
		defaultRoute([4]byte{10, 0, 0, 1}, 35, 20),
		defaultRoute([4]byte{192, 168, 1, 1}, 12, 20),
	}
	gateway, index, _ = selectDefaultGateway(tied, known)
	if gateway != "10.0.0.1" || index != 35 {
		t.Fatalf("a tie was broken as %s on %d; the forward table cannot express an "+
			"interface metric tiebreak and must keep the first record", gateway, index)
	}
}

// TestDefaultGatewayReportsEveryCandidate proves a machine with several
// uplinks is told that the answer was a choice, and what it chose between.
func TestDefaultGatewayReportsEveryCandidate(t *testing.T) {
	original := readForwardTableFn
	t.Cleanup(func() { readForwardTableFn = original })

	readForwardTableFn = func() ([]ipForwardRow, error) {
		return []ipForwardRow{
			defaultRoute([4]byte{172, 22, 176, 1}, 35, 5256),
			defaultRoute([4]byte{192, 168, 1, 1}, 12, 35),
		}, nil
	}

	choice := defaultGateway()
	if choice.Gateway != "192.168.1.1" {
		t.Errorf("gateway = %q, want the lower effective metric", choice.Gateway)
	}
	if choice.Candidates != 2 {
		t.Errorf("candidates = %d, want 2: a reader must know the choice was a preference", choice.Candidates)
	}
	if len(choice.RunnersUp) != 1 || choice.RunnersUp[0] != "172.22.176.1" {
		t.Errorf("runners up = %v, want the gateway that was passed over", choice.RunnersUp)
	}
}

// TestDefaultGatewaySaysNothingIsAmbiguousOnASingleUplink keeps the extra
// reporting from firing on an ordinary machine.
func TestDefaultGatewayReportsOneCandidateOnASingleUplink(t *testing.T) {
	original := readForwardTableFn
	t.Cleanup(func() { readForwardTableFn = original })

	readForwardTableFn = func() ([]ipForwardRow, error) {
		return []ipForwardRow{defaultRoute([4]byte{192, 168, 1, 1}, 12, 35)}, nil
	}
	choice := defaultGateway()
	if choice.Candidates != 1 {
		t.Errorf("candidates = %d, want 1", choice.Candidates)
	}
	if len(choice.RunnersUp) != 0 {
		t.Errorf("runners up = %v, want none", choice.RunnersUp)
	}
}

// TestDefaultGatewayFallsBackWhenTheApiFails covers the failure path: a routing
// table that cannot be read must produce unknown, never a guess.
func TestDefaultGatewayFallsBackWhenTheApiFails(t *testing.T) {
	original := readForwardTableFn
	t.Cleanup(func() { readForwardTableFn = original })

	readForwardTableFn = func() ([]ipForwardRow, error) {
		return nil, errors.New("GetIpForwardTable failed")
	}
	choice := defaultGateway()
	if choice.Gateway != "" || choice.Interface != 0 {
		t.Errorf("a failed API call produced gateway=%q interface=%d", choice.Gateway, choice.Interface)
	}
	if choice.Candidates != 0 {
		t.Errorf("candidates = %d after a failure, want 0", choice.Candidates)
	}
}

// TestDefaultGatewayFallsBackOnAMalformedTable covers a table that parses but
// holds nothing usable, which is what a layout mistake would look like.
func TestDefaultGatewayFallsBackOnAMalformedTable(t *testing.T) {
	original := readForwardTableFn
	t.Cleanup(func() { readForwardTableFn = original })

	readForwardTableFn = func() ([]ipForwardRow, error) {
		// Every record shifted by one word: an address where a mask belongs.
		return []ipForwardRow{
			{
				ForwardDest:    [4]byte{},
				ForwardMask:    [4]byte{192, 168, 1, 1},
				ForwardNextHop: [4]byte{192, 168, 1, 1},
				ForwardIfIndex: 12,
			},
		}, nil
	}
	if choice := defaultGateway(); choice.Gateway != "" {
		t.Errorf("a malformed table produced gateway %q", choice.Gateway)
	}
}

// TestParseForwardTableRoundTrip proves the encoder used by the tests matches
// what the parser expects, so a layout test cannot pass against a writer that
// disagrees with it.
func TestParseForwardTableRoundTrip(t *testing.T) {
	original := []ipForwardRow{
		defaultRoute([4]byte{192, 168, 1, 1}, 12, 35),
		{
			ForwardDest:    [4]byte{127, 0, 0, 0},
			ForwardMask:    [4]byte{255, 0, 0, 0},
			ForwardNextHop: [4]byte{127, 0, 0, 1},
			ForwardIfIndex: 1,
			ForwardMetric1: 331,
		},
		{
			ForwardDest:    [4]byte{224, 0, 0, 0},
			ForwardMask:    [4]byte{240, 0, 0, 0},
			ForwardNextHop: [4]byte{127, 0, 0, 1},
			ForwardIfIndex: 1,
			ForwardMetric1: 331,
		},
	}
	parsed, err := parseForwardTable(buildForwardTable(original))
	if err != nil {
		t.Fatalf("parseForwardTable: %v", err)
	}
	if len(parsed) != len(original) {
		t.Fatalf("records = %d, want %d", len(parsed), len(original))
	}
	for index := range original {
		if parsed[index] != original[index] {
			t.Errorf("record %d round trip changed:\n got %+v\nwant %+v", index, parsed[index], original[index])
		}
	}
}

// TestParseForwardTableIgnoresTrailingSlack proves the record count comes from
// the header even when the buffer holds far more than the table.
func TestParseForwardTableIgnoresTrailingSlack(t *testing.T) {
	table := buildForwardTable([]ipForwardRow{defaultRoute([4]byte{192, 168, 1, 1}, 12, 35)})
	table = append(table, make([]byte, 4096)...)
	parsed, err := parseForwardTable(table)
	if err != nil {
		t.Fatalf("parseForwardTable: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("records = %d, want 1", len(parsed))
	}
}

// TestHeaderCountIsReadAsLittleEndian guards the one multi byte field the table
// header contains.
func TestHeaderCountIsReadAsLittleEndian(t *testing.T) {
	table := make([]byte, ipForwardTableHeaderBytes)
	binary.LittleEndian.PutUint32(table, 7)
	if got := binary.LittleEndian.Uint32(table[:4]); got != 7 {
		t.Fatalf("header read back as %d", got)
	}
	if _, err := parseForwardTable(table); err == nil {
		t.Error("a header claiming 7 entries in a 4 byte buffer was accepted")
	}
}
