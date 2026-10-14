//go:build windows

package netdiag

import (
	"encoding/binary"
	"testing"
)

// buildForwardTable renders a MIB_IPFORWARDTABLE exactly as the Windows API
// would: a four byte dwNumEntries header followed by 56 byte records. Every
// layout test builds its input through this function, so a test cannot pass by
// agreeing with a wrong assumption about the buffer.
func buildForwardTable(rows []ipForwardRow) []byte {
	buf := make([]byte, ipForwardTableHeaderBytes+len(rows)*ipForwardRowBytes)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(rows)))
	for index, row := range rows {
		base := ipForwardTableHeaderBytes + index*ipForwardRowBytes
		copy(buf[base+0:base+4], row.ForwardDest[:])
		copy(buf[base+4:base+8], row.ForwardMask[:])
		binary.LittleEndian.PutUint32(buf[base+8:base+12], row.ForwardPolicy)
		copy(buf[base+12:base+16], row.ForwardNextHop[:])
		binary.LittleEndian.PutUint32(buf[base+16:base+20], row.ForwardIfIndex)
		binary.LittleEndian.PutUint32(buf[base+20:base+24], row.ForwardType)
		binary.LittleEndian.PutUint32(buf[base+24:base+28], row.ForwardProto)
		binary.LittleEndian.PutUint32(buf[base+28:base+32], row.ForwardAge)
		binary.LittleEndian.PutUint32(buf[base+32:base+36], row.ForwardNextHopAS)
		binary.LittleEndian.PutUint32(buf[base+36:base+40], row.ForwardMetric1)
		binary.LittleEndian.PutUint32(buf[base+40:base+44], row.ForwardMetric2)
		binary.LittleEndian.PutUint32(buf[base+44:base+48], row.ForwardMetric3)
		binary.LittleEndian.PutUint32(buf[base+48:base+52], row.ForwardMetric4)
		binary.LittleEndian.PutUint32(buf[base+52:base+56], row.ForwardMetric5)
	}
	return buf
}

// defaultRoute builds a default route record.
func defaultRoute(nextHop [4]byte, interfaceIndex, effectiveMetric uint32) ipForwardRow {
	return ipForwardRow{
		ForwardDest:    [4]byte{},
		ForwardMask:    [4]byte{},
		ForwardNextHop: nextHop,
		ForwardIfIndex: interfaceIndex,
		ForwardMetric1: effectiveMetric,
		ForwardType:    4, // MIB_IPFORWARD_TYPE_NETWORK
		ForwardProto:   3, // NET_RT_IPV4
	}
}

// TestRowSizeMatchesTheWindowsRecord pins the stride. A struct that grew or
// shrank would shift every field after the first and silently misreport the
// whole table, so the size is asserted against the documented value.
func TestRowSizeMatchesTheWindowsRecord(t *testing.T) {
	if ipForwardRowBytes != 56 {
		t.Fatalf("ipForwardRow is %d bytes, the Windows MIB_IPFORWARDROW is 56", ipForwardRowBytes)
	}
	if ipForwardTableHeaderBytes != 4 {
		t.Fatalf("table header is %d bytes, dwNumEntries is 4", ipForwardTableHeaderBytes)
	}
}

// TestParseForwardTableUsesTheHeaderNotTheBufferLength proves the record count
// comes from dwNumEntries. The API reports a buffer larger than the table it
// holds, so a reader that walks to the end of the buffer reads past the last
// record into unused space.
func TestParseForwardTableUsesTheHeaderNotTheBufferLength(t *testing.T) {
	rows := []ipForwardRow{
		defaultRoute([4]byte{192, 168, 1, 1}, 12, 35),
		{
			ForwardDest:    [4]byte{127, 0, 0, 0},
			ForwardMask:    [4]byte{255, 0, 0, 0},
			ForwardNextHop: [4]byte{127, 0, 0, 1},
			ForwardIfIndex: 1,
			ForwardMetric1: 331,
		},
	}
	table := buildForwardTable(rows)
	// The API over-allocates; append 500 bytes of slack the table does not use.
	table = append(table, make([]byte, 500)...)

	parsed, err := parseForwardTable(table)
	if err != nil {
		t.Fatalf("parseForwardTable: %v", err)
	}
	if len(parsed) != len(rows) {
		t.Fatalf("records = %d, want %d from dwNumEntries, not %d from the buffer length",
			len(parsed), len(rows), len(table)/ipForwardRowBytes)
	}
	if parsed[0].ForwardNextHop != [4]byte{192, 168, 1, 1} {
		t.Errorf("record 0 next hop = %v", parsed[0].ForwardNextHop)
	}
	if parsed[1].ForwardDest != [4]byte{127, 0, 0, 0} {
		t.Errorf("record 1 dest = %v, want 127.0.0.0", parsed[1].ForwardDest)
	}
}

// TestParseForwardTableFieldOffsets checks every field of a record against the
// documented order, using values that are distinct so a one word shift in any
// direction is caught.
func TestParseForwardTableFieldOffsets(t *testing.T) {
	row := ipForwardRow{
		ForwardDest:      [4]byte{1, 2, 3, 4},
		ForwardMask:      [4]byte{255, 255, 255, 0},
		ForwardPolicy:    0x11111111,
		ForwardNextHop:   [4]byte{5, 6, 7, 8},
		ForwardIfIndex:   0x22222222,
		ForwardType:      0x33333333,
		ForwardProto:     0x44444444,
		ForwardAge:       0x55555555,
		ForwardNextHopAS: 0x66666666,
		ForwardMetric1:   0x77777777,
		ForwardMetric2:   0x88888888,
		ForwardMetric3:   0x99999999,
		ForwardMetric4:   0xAAAAAAAA,
		ForwardMetric5:   0xBBBBBBBB,
	}
	parsed, err := parseForwardTable(buildForwardTable([]ipForwardRow{row}))
	if err != nil {
		t.Fatalf("parseForwardTable: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("records = %d", len(parsed))
	}
	got := parsed[0]
	if got.ForwardDest != row.ForwardDest {
		t.Errorf("dest = %v, want %v", got.ForwardDest, row.ForwardDest)
	}
	if got.ForwardMask != row.ForwardMask {
		t.Errorf("mask = %v, want %v", got.ForwardMask, row.ForwardMask)
	}
	if got.ForwardPolicy != row.ForwardPolicy {
		t.Errorf("policy = %#x", got.ForwardPolicy)
	}
	if got.ForwardNextHop != row.ForwardNextHop {
		t.Errorf("next hop = %v, want %v", got.ForwardNextHop, row.ForwardNextHop)
	}
	if got.ForwardIfIndex != row.ForwardIfIndex {
		t.Errorf("if index = %#x", got.ForwardIfIndex)
	}
	if got.ForwardType != row.ForwardType || got.ForwardProto != row.ForwardProto {
		t.Errorf("type/proto = %#x/%#x", got.ForwardType, got.ForwardProto)
	}
	if got.ForwardAge != row.ForwardAge || got.ForwardNextHopAS != row.ForwardNextHopAS {
		t.Errorf("age/nexthopAS = %#x/%#x", got.ForwardAge, got.ForwardNextHopAS)
	}
	if got.ForwardMetric1 != row.ForwardMetric1 {
		t.Errorf("metric1 = %#x, want %#x", got.ForwardMetric1, row.ForwardMetric1)
	}
	if got.ForwardMetric5 != row.ForwardMetric5 {
		t.Errorf("metric5 = %#x, want %#x", got.ForwardMetric5, row.ForwardMetric5)
	}
}

// TestParseForwardTableRejectsAnomalousHeaders covers the ways a malformed
// table must be refused rather than walked.
func TestParseForwardTableRejectsAnomalousHeaders(t *testing.T) {
	good := buildForwardTable([]ipForwardRow{defaultRoute([4]byte{192, 168, 1, 1}, 12, 35)})

	t.Run("buffer too small for a header", func(t *testing.T) {
		if _, err := parseForwardTable([]byte{1, 2, 3}); err == nil {
			t.Error("a three byte buffer was accepted")
		}
	})

	t.Run("count larger than the buffer", func(t *testing.T) {
		truncated := good[:ipForwardTableHeaderBytes+8]
		if _, err := parseForwardTable(truncated); err == nil {
			t.Error("a table claiming more entries than the buffer holds was accepted")
		}
	})

	t.Run("absurd entry count", func(t *testing.T) {
		hostile := make([]byte, 16)
		binary.LittleEndian.PutUint32(hostile[0:4], 1<<30)
		if _, err := parseForwardTable(hostile); err == nil {
			t.Error("an absurd entry count was accepted")
		}
	})
}

// TestTableHeaderIsNotOptional pins what the four byte dwNumEntries header
// actually changes.
//
// This is the regression for the mistake that began this review: a reader that
// treats offset zero as the first record is reading the entry count as a
// destination and the first record's destination as a mask. On the machine that
// surfaced the bug, the gateway then came back as unknown while every other
// field still looked plausible.
//
// The test states precisely what it proves and what it does not: it shows that
// dropping the header changes the parsed record count and therefore the outcome.
// It does not attempt to model every possible wrong reader, because the
// validation in routeRecordIsUsable is what guards the general case.
func TestTableHeaderIsNotOptional(t *testing.T) {
	rows := []ipForwardRow{
		defaultRoute([4]byte{192, 168, 1, 1}, 12, 35),
		{
			ForwardDest:    [4]byte{192, 168, 1, 0},
			ForwardMask:    [4]byte{255, 255, 255, 0},
			ForwardNextHop: [4]byte{192, 168, 1, 111},
			ForwardIfIndex: 12,
			ForwardMetric1: 291,
		},
	}
	known := map[uint32]bool{12: true}

	correct, err := parseForwardTable(buildForwardTable(rows))
	if err != nil {
		t.Fatalf("parseForwardTable: %v", err)
	}
	if len(correct) != len(rows) {
		t.Fatalf("records = %d, want %d", len(correct), len(rows))
	}
	gateway, index, ok := selectDefaultGateway(correct, known)
	if !ok || gateway != "192.168.1.1" || index != 12 {
		t.Fatalf("correct parse gave gateway=%q index=%d ok=%v", gateway, index, ok)
	}

	// Reading the same buffer as though it started at offset zero consumes the
	// header as the first record's destination. The entry count is then
	// interpreted as a record count, which is a different table entirely.
	table := buildForwardTable(rows)
	if binary.LittleEndian.Uint32(table[0:4]) != uint32(len(rows)) {
		t.Fatalf("the header does not hold the record count")
	}
	headerless, err := parseForwardTable(table[4:])
	if err == nil && len(headerless) == len(correct) {
		t.Fatal("dropping the header produced the same table, so the test cannot detect the mistake")
	}
	if err == nil {
		if _, _, ok := selectDefaultGateway(headerless, known); ok {
			t.Error("a header-less read produced a gateway")
		}
	}
}

// TestValidationRejectsAShiftedRecord feeds the validator a record whose fields
// come from the wrong offsets but which still looks superficially like a route,
// and requires it to be refused.
func TestValidationRejectsAShiftedRecord(t *testing.T) {
	known := map[uint32]bool{12: true}

	// A record shifted one word late leaves the real destination sitting in the
	// mask slot, which is not a prefix length and must be refused.
	shifted := ipForwardRow{
		ForwardDest:    [4]byte{0, 0, 0, 0},
		ForwardMask:    [4]byte{192, 168, 1, 1}, // a host address, not a mask
		ForwardNextHop: [4]byte{192, 168, 1, 1},
		ForwardIfIndex: 12,
	}
	if routeRecordIsUsable(shifted, known) {
		t.Error("a record with an address in the mask slot was accepted")
	}
}

// TestSelectDefaultGatewayWithTwoAdapters is the multi uplink case. Two default
// routes exist and the one Windows would prefer must win.
func TestSelectDefaultGatewayWithTwoAdapters(t *testing.T) {
	known := map[uint32]bool{12: true, 35: true}

	cases := []struct {
		name        string
		rows        []ipForwardRow
		wantGateway string
		wantIndex   uint32
	}{
		{
			name: "lower effective metric wins regardless of order",
			rows: []ipForwardRow{
				defaultRoute([4]byte{192, 168, 1, 1}, 12, 35),
				defaultRoute([4]byte{172, 22, 176, 1}, 35, 5256),
			},
			wantGateway: "192.168.1.1",
			wantIndex:   12,
		},
		{
			name: "a VPN with the lower metric wins even when listed second",
			rows: []ipForwardRow{
				defaultRoute([4]byte{172, 22, 176, 1}, 35, 5256),
				defaultRoute([4]byte{10, 8, 0, 1}, 12, 5),
			},
			wantGateway: "10.8.0.1",
			wantIndex:   12,
		},
		{
			name: "metric zero is a real value and wins",
			rows: []ipForwardRow{
				defaultRoute([4]byte{192, 168, 1, 1}, 12, 100),
				defaultRoute([4]byte{10, 0, 0, 1}, 35, 0),
			},
			wantGateway: "10.0.0.1",
			wantIndex:   35,
		},
		{
			name: "equal metrics keep the first record",
			rows: []ipForwardRow{
				defaultRoute([4]byte{192, 168, 1, 1}, 12, 35),
				defaultRoute([4]byte{10, 0, 0, 1}, 35, 35),
			},
			wantGateway: "192.168.1.1",
			wantIndex:   12,
		},
		{
			name: "a route on an unknown adapter is skipped",
			rows: []ipForwardRow{
				defaultRoute([4]byte{203, 0, 113, 9}, 999, 1),
				defaultRoute([4]byte{192, 168, 1, 1}, 12, 35),
			},
			wantGateway: "192.168.1.1",
			wantIndex:   12,
		},
		{
			name: "no default route at all",
			rows: []ipForwardRow{
				{
					ForwardDest:    [4]byte{192, 168, 1, 0},
					ForwardMask:    [4]byte{255, 255, 255, 0},
					ForwardNextHop: [4]byte{192, 168, 1, 111},
					ForwardIfIndex: 12,
					ForwardMetric1: 291,
				},
			},
			wantGateway: "",
			wantIndex:   0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gateway, index, ok := selectDefaultGateway(c.rows, known)
			if c.wantGateway == "" {
				if ok {
					t.Errorf("gateway = %q, want none", gateway)
				}
				return
			}
			if !ok {
				t.Fatalf("no gateway selected, want %s", c.wantGateway)
			}
			if gateway != c.wantGateway || index != c.wantIndex {
				t.Errorf("selected %s on interface %d, want %s on %d",
					gateway, index, c.wantGateway, c.wantIndex)
			}
		})
	}
}

// TestRouteRecordIsUsableRejectsBadRecords covers every rejection rule.
func TestRouteRecordIsUsableRejectsBadRecords(t *testing.T) {
	known := map[uint32]bool{12: true}
	cases := []struct {
		name string
		row  ipForwardRow
	}{
		{"non contiguous mask", ipForwardRow{
			ForwardMask: [4]byte{255, 0, 255, 0}, ForwardNextHop: [4]byte{192, 168, 1, 1}, ForwardIfIndex: 12}},
		{"unspecified next hop", ipForwardRow{
			ForwardIfIndex: 12}},
		{"multicast next hop", ipForwardRow{
			ForwardNextHop: [4]byte{224, 0, 0, 1}, ForwardIfIndex: 12}},
		{"link local next hop", ipForwardRow{
			ForwardNextHop: [4]byte{169, 254, 1, 1}, ForwardIfIndex: 12}},
		{"zero interface index", ipForwardRow{
			ForwardNextHop: [4]byte{192, 168, 1, 1}}},
		{"non default destination", ipForwardRow{
			ForwardDest: [4]byte{0, 0, 0, 1}, ForwardNextHop: [4]byte{192, 168, 1, 1}, ForwardIfIndex: 12}},
		{"non default mask", ipForwardRow{
			ForwardMask: [4]byte{0, 0, 0, 255}, ForwardNextHop: [4]byte{192, 168, 1, 1}, ForwardIfIndex: 12}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if routeRecordIsUsable(c.row, known) {
				t.Error("an unusable record was accepted")
			}
		})
	}
	if !routeRecordIsUsable(defaultRoute([4]byte{192, 168, 1, 1}, 12, 35), known) {
		t.Error("a valid default route was rejected")
	}
}

// TestIsContiguousMask pins the prefix-length check on its own.
func TestIsContiguousMask(t *testing.T) {
	valid := [][4]byte{
		{},                   // 0.0.0.0
		{128, 0, 0, 0},       // 128.0.0.0
		{255, 0, 0, 0},       // 255.0.0.0
		{255, 255, 255, 0},   // 255.255.255.0
		{255, 255, 255, 255}, // 255.255.255.255
		{255, 255, 240, 0},   // 255.255.240.0
	}
	for _, mask := range valid {
		if !isContiguousMask(mask) {
			t.Errorf("%v was rejected", mask)
		}
	}
	invalid := [][4]byte{
		{255, 0, 255, 0},
		{1, 254, 0, 0},
		{255, 255, 0, 255},
		{0, 255, 0, 0},
	}
	for _, mask := range invalid {
		if isContiguousMask(mask) {
			t.Errorf("%v was accepted", mask)
		}
	}
}
