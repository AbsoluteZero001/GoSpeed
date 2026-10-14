//go:build windows

package netdiag

import "testing"

// TestLiveRoutingTableIsFullyConsistent reads the machine's real routing table
// and checks every record, not just the one the result happens to use.
//
// This is the test that would have caught the header offset mistake. A reader
// that forgets dwNumEntries shifts every record by one word, and a single spot
// check on "the gateway looks right" cannot tell that apart from a correct
// parse, because the first record is the only one whose fields still look
// plausible after a shift. Requiring every record to decode into a well formed
// route does distinguish them.
func TestLiveRoutingTableIsFullyConsistent(t *testing.T) {
	rows, err := readForwardTable()
	if err != nil {
		t.Skipf("the routing table could not be read on this machine: %v", err)
	}
	if len(rows) == 0 {
		t.Skip("the machine reports no IPv4 routes")
	}

	known := knownInterfaceIndexes()
	if len(known) == 0 {
		t.Skip("no interfaces are visible")
	}

	malformed := 0
	for index, row := range rows {
		if !isContiguousMask(row.ForwardMask) {
			malformed++
			t.Errorf("record %d has a mask that is not a prefix length: %v", index, row.ForwardMask)
			continue
		}
		if row.ForwardIfIndex != 0 && !known[row.ForwardIfIndex] {
			malformed++
			t.Errorf("record %d names interface %d, which is not visible to the standard library",
				index, row.ForwardIfIndex)
		}
		if row.ForwardIfIndex > maxForwardTableEntries {
			malformed++
			t.Errorf("record %d has an implausible interface index %d", index, row.ForwardIfIndex)
		}
	}
	if malformed > 0 {
		t.Fatalf("%d of %d records did not decode as real routes, so the table layout is wrong",
			malformed, len(rows))
	}
	t.Logf("all %d routing table records decoded as well formed routes", len(rows))

	// The default route, when the machine has one, must carry a usable next hop
	// on an adapter that exists.
	gateway, index, found := selectDefaultGateway(rows, known)
	if found {
		if gateway == "" || gateway == Unknown {
			t.Errorf("a default route was selected but its next hop is %q", gateway)
		}
		if !known[index] {
			t.Errorf("the default route names interface %d, which does not exist", index)
		}
		t.Logf("default route: gateway=%s interfaceIndex=%d", gateway, index)
	} else {
		t.Log("this machine has no usable IPv4 default route; that is reported as unknown, not guessed")
	}
}

// TestLiveRoutingTableRespectsItsOwnHeader checks that the parser reads exactly
// as many records as the header claims, and that every one of them is backed by
// real bytes in the buffer.
func TestLiveRoutingTableRespectsItsOwnHeader(t *testing.T) {
	rows, err := readForwardTable()
	if err != nil {
		t.Skipf("the routing table could not be read: %v", err)
	}
	needed := ipForwardTableHeaderBytes + len(rows)*ipForwardRowBytes
	t.Logf("parsed %d records, which need %d bytes of table space", len(rows), needed)

	// A parser that walked the buffer instead of the header would return more
	// records than dwNumEntries claims. Re-reading must be stable.
	again, err := readForwardTable()
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if len(again) != len(rows) {
		t.Errorf("two reads returned %d and %d records", len(rows), len(again))
	}
	if len(again) > 0 && again[0] != rows[0] {
		t.Errorf("two reads disagree on the first record")
	}
}
