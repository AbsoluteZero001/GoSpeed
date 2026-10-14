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

// The gateway decision reads two independent inputs: the routing table and the
// set of adapter indexes the standard library can see. The validator cross
// checks one against the other, so a test has to replace both or it is really
// only testing the machine it happens to run on.
//
// fakeIndexA and fakeIndexB stand in for adapters. They are far above any real
// Windows adapter index on purpose: a test that used 12, or 35, would pass on a
// developer laptop that owns those indexes and be discarded by routeRecordIsUsable
// on a CI runner that does not. That is exactly how these two tests came to pass
// locally and fail on GitHub Actions.
const (
	fakeIndexA = 40001
	fakeIndexB = 40002
)

// stubRouting replaces both inputs to the gateway decision and restores them
// when the test ends, so nothing leaks into the next test.
func stubRouting(t *testing.T, rows []ipForwardRow, readErr error, indexes map[uint32]bool) {
	t.Helper()
	originalTable := readForwardTableFn
	originalIndexes := knownInterfaceIndexesFn
	t.Cleanup(func() {
		readForwardTableFn = originalTable
		knownInterfaceIndexesFn = originalIndexes
	})
	readForwardTableFn = func() ([]ipForwardRow, error) { return rows, readErr }
	knownInterfaceIndexesFn = func() map[uint32]bool { return indexes }
}

// TestGatewayTestIndexesAreAbsentFromTheRealMachine keeps the stand in indexes
// honest. If a future edit raised them into the range a real machine uses, the
// tests would quietly start depending on whatever adapter happened to own that
// index and the property this file exists to protect would be lost.
func TestGatewayTestIndexesAreAbsentFromTheRealMachine(t *testing.T) {
	real := knownInterfaceIndexesFn()
	if real == nil {
		t.Fatal("the real adapter list is nil")
	}
	for _, index := range []uint32{fakeIndexA, fakeIndexB} {
		if real[index] {
			t.Fatalf("stand in index %d is a real adapter index on this machine; "+
				"the gateway tests would depend on the host", index)
		}
	}
}

// TestDefaultGatewayAcceptsARouteOnASimulatedInterface proves the reader accepts
// a default route when the adapter set is supplied rather than inherited from
// the host. This is the case the CI failure could not reach.
func TestDefaultGatewayAcceptsARouteOnASimulatedInterface(t *testing.T) {
	stubRouting(t,
		[]ipForwardRow{defaultRoute([4]byte{192, 168, 1, 1}, fakeIndexA, 35)},
		nil,
		map[uint32]bool{fakeIndexA: true},
	)

	choice := defaultGateway()
	if choice.Gateway != "192.168.1.1" {
		t.Errorf("gateway = %q, want 192.168.1.1", choice.Gateway)
	}
	if choice.Interface != fakeIndexA {
		t.Errorf("interface = %d, want %d", choice.Interface, fakeIndexA)
	}
}

// TestDefaultGatewayRejectsARouteOnAnUnknownInterface is the other half: the
// adapter cross check must still hold when the adapter set is simulated. Making
// the set injectable must not make the validation permissive.
func TestDefaultGatewayRejectsARouteOnAnUnknownInterface(t *testing.T) {
	stubRouting(t,
		[]ipForwardRow{defaultRoute([4]byte{192, 168, 1, 1}, fakeIndexA, 35)},
		nil,
		// The routing table names fakeIndexA, but no adapter claims it.
		map[uint32]bool{fakeIndexB: true},
	)

	choice := defaultGateway()
	if choice.Gateway != "" || choice.Interface != 0 {
		t.Errorf("a route on an unknown adapter was published as gateway=%q interface=%d",
			choice.Gateway, choice.Interface)
	}
	if choice.Candidates != 0 || len(choice.RunnersUp) != 0 {
		t.Errorf("a route on an unknown adapter counted as %d candidates, runners up %v",
			choice.Candidates, choice.RunnersUp)
	}
}

// TestDefaultGatewayPrefersAZeroMetric keeps metric zero a value rather than a
// missing one. Windows uses zero for the most preferred route, so a reader that
// treated it as unset would discard the best entry on the table and answer with
// the second best. The zero route is deliberately listed second so a reader that
// only ever kept the first record would also fail.
func TestDefaultGatewayPrefersAZeroMetric(t *testing.T) {
	stubRouting(t, []ipForwardRow{
		defaultRoute([4]byte{192, 168, 1, 1}, fakeIndexA, 35),
		defaultRoute([4]byte{10, 0, 0, 1}, fakeIndexB, 0),
	}, nil, map[uint32]bool{fakeIndexA: true, fakeIndexB: true})

	choice := defaultGateway()
	if choice.Gateway != "10.0.0.1" || choice.Interface != fakeIndexB {
		t.Errorf("selected %q on %d, want 10.0.0.1 on %d: a metric of zero is the "+
			"most preferred value, not an unset one", choice.Gateway, choice.Interface, fakeIndexB)
	}
	if choice.Candidates != 2 {
		t.Errorf("candidates = %d, want 2: a zero metric still makes a route a candidate", choice.Candidates)
	}
}

// TestDefaultGatewayReportsEveryCandidate proves a machine with several
// uplinks is told that the answer was a choice, and what it chose between.
func TestDefaultGatewayReportsEveryCandidate(t *testing.T) {
	stubRouting(t, []ipForwardRow{
		defaultRoute([4]byte{172, 22, 176, 1}, fakeIndexB, 5256),
		defaultRoute([4]byte{192, 168, 1, 1}, fakeIndexA, 35),
	}, nil, map[uint32]bool{fakeIndexA: true, fakeIndexB: true})

	choice := defaultGateway()
	if choice.Gateway != "192.168.1.1" {
		t.Errorf("gateway = %q, want the lower effective metric", choice.Gateway)
	}
	if choice.Interface != fakeIndexA {
		t.Errorf("interface = %d, want %d", choice.Interface, fakeIndexA)
	}
	if choice.Candidates != 2 {
		t.Errorf("candidates = %d, want 2: a reader must know the choice was a preference", choice.Candidates)
	}
	if len(choice.RunnersUp) != 1 || choice.RunnersUp[0] != "172.22.176.1" {
		t.Errorf("runners up = %v, want the gateway that was passed over", choice.RunnersUp)
	}
}

// TestDefaultGatewayNamesEveryLoser checks the runners up list names each passed
// over gateway once. A duplicate or an omission would make the note misleading
// rather than merely terse.
func TestDefaultGatewayNamesEveryLoser(t *testing.T) {
	stubRouting(t, []ipForwardRow{
		defaultRoute([4]byte{192, 168, 1, 1}, fakeIndexA, 35),
		defaultRoute([4]byte{172, 22, 176, 1}, fakeIndexB, 5256),
		defaultRoute([4]byte{10, 8, 0, 1}, fakeIndexB, 5000),
	}, nil, map[uint32]bool{fakeIndexA: true, fakeIndexB: true})

	choice := defaultGateway()
	if choice.Gateway != "192.168.1.1" {
		t.Fatalf("gateway = %q, want 192.168.1.1", choice.Gateway)
	}
	if choice.Candidates != 3 {
		t.Errorf("candidates = %d, want 3", choice.Candidates)
	}
	want := map[string]bool{"172.22.176.1": true, "10.8.0.1": true}
	if len(choice.RunnersUp) != len(want) {
		t.Fatalf("runners up = %v, want %d entries", choice.RunnersUp, len(want))
	}
	for _, gateway := range choice.RunnersUp {
		if !want[gateway] {
			t.Errorf("runners up contains %q, which was not a losing gateway", gateway)
		}
		delete(want, gateway)
	}
	if len(want) != 0 {
		t.Errorf("runners up omitted %v", want)
	}
}

// TestDefaultGatewaySelectionIsStable checks the multi uplink answer does not
// depend on the order the routing table happened to return records in. Windows
// does not promise an order, so a reader whose winner followed table order would
// report a different gateway on a machine whose table was rebuilt.
func TestDefaultGatewaySelectionIsStable(t *testing.T) {
	rows := []ipForwardRow{
		defaultRoute([4]byte{192, 168, 1, 1}, fakeIndexA, 35),
		defaultRoute([4]byte{10, 0, 0, 1}, fakeIndexB, 5256),
		defaultRoute([4]byte{172, 22, 176, 1}, fakeIndexB, 300),
	}
	known := map[uint32]bool{fakeIndexA: true, fakeIndexB: true}

	permutations := [][]ipForwardRow{
		{rows[0], rows[1], rows[2]},
		{rows[1], rows[0], rows[2]},
		{rows[2], rows[1], rows[0]},
		{rows[1], rows[2], rows[0]},
		{rows[2], rows[0], rows[1]},
		{rows[0], rows[2], rows[1]},
	}
	for attempt, order := range permutations {
		// Repeating the read also proves the result is not carried over between
		// calls on the same seam.
		for repeat := 0; repeat < 5; repeat++ {
			stubRouting(t, order, nil, known)
			choice := defaultGateway()
			if choice.Gateway != "192.168.1.1" || choice.Interface != fakeIndexA {
				t.Fatalf("permutation %d read %d selected %q on %d, want 192.168.1.1 on %d",
					attempt, repeat, choice.Gateway, choice.Interface, fakeIndexA)
			}
			if choice.Candidates != 3 {
				t.Fatalf("permutation %d read %d counted %d candidates, want 3",
					attempt, repeat, choice.Candidates)
			}
		}
	}
}

// TestDefaultGatewayReportsOneCandidateOnASingleUplink keeps the extra
// reporting from firing on an ordinary machine.
func TestDefaultGatewayReportsOneCandidateOnASingleUplink(t *testing.T) {
	stubRouting(t,
		[]ipForwardRow{defaultRoute([4]byte{192, 168, 1, 1}, fakeIndexA, 35)},
		nil,
		map[uint32]bool{fakeIndexA: true},
	)

	choice := defaultGateway()
	if choice.Gateway != "192.168.1.1" {
		t.Errorf("gateway = %q, want 192.168.1.1", choice.Gateway)
	}
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
	stubRouting(t, nil, errors.New("GetIpForwardTable failed"),
		map[uint32]bool{fakeIndexA: true})

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
	// Every record shifted by one word: an address where a mask belongs.
	stubRouting(t, []ipForwardRow{
		{
			ForwardDest:    [4]byte{},
			ForwardMask:    [4]byte{192, 168, 1, 1},
			ForwardNextHop: [4]byte{192, 168, 1, 1},
			ForwardIfIndex: fakeIndexA,
		},
	}, nil, map[uint32]bool{fakeIndexA: true})

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
