package netdiag

import (
	"strings"
	"testing"
	"time"
)

// The parser tests are the only place traceroute or ping output enters the
// package, so they pin the rules that keep a hop from being reported as
// something it was not.

func TestParsePingOutputKeepsExactRoundTrips(t *testing.T) {
	output := "" +
		"PING example.com (93.184.216.34) 56(84) bytes of data.\n" +
		"56 bytes from 93.184.216.34: icmp_seq=1 ttl=57 time=12.3 ms\n" +
		"56 bytes from 93.184.216.34: icmp_seq=2 ttl=57 time=11.8 ms\n" +
		"\n" +
		"--- example.com ping statistics ---\n" +
		"2 packets transmitted, 2 received, 0% packet loss, time 1001ms\n"
	set := parsePingOutput(output, 2)
	if len(set.Samples) != 2 {
		t.Fatalf("samples = %d, want 2: %+v", len(set.Samples), set)
	}
	if set.Samples[0] != 12300*time.Microsecond {
		t.Errorf("first sample = %v, want 12.3ms", set.Samples[0])
	}
	if set.TimedOut != 0 {
		t.Errorf("timed out = %d, want 0", set.TimedOut)
	}
	// The platform's own "0% packet loss" line must not leak into the counters:
	// it describes ICMP echo delivery, not link loss.
	if set.Failed != 0 {
		t.Errorf("failed = %d, want 0", set.Failed)
	}
}

func TestParsePingOutputRecordsUnansweredProbesAsTimeouts(t *testing.T) {
	output := "" +
		"PING example.com (93.184.216.34) 56(84) bytes of data.\n" +
		"56 bytes from 93.184.216.34: icmp_seq=1 ttl=57 time=10.0 ms\n" +
		"Request timed out.\n" +
		"Request timed out.\n" +
		"\n3 packets transmitted, 1 received, 66% packet loss, time 2002ms\n"
	set := parsePingOutput(output, 3)
	if len(set.Samples) != 1 {
		t.Fatalf("samples = %d, want 1", len(set.Samples))
	}
	if set.TimedOut != 2 {
		t.Errorf("timed out = %d, want 2", set.TimedOut)
	}
	// The detail has to say out loud that this is not packet loss, because the
	// raw platform output does use that word.
	if set.Detail == "" {
		t.Fatal("an unanswered probe must leave a detail explaining it is not packet loss")
	}
	if !strings.Contains(set.Detail, "not packet loss") {
		t.Errorf("detail %q does not distinguish a timeout from packet loss", set.Detail)
	}
}

func TestParsePingOutputRefusesUpperBoundAsPointValue(t *testing.T) {
	// A platform that prints "time<1ms" published an upper bound. Turning that
	// into 1 ms would overstate and turning it into 0 would understate.
	output := "64 bytes from 127.0.0.1: icmp_seq=1 ttl=64 time<1 ms\n"
	set := parsePingOutput(output, 1)
	if len(set.Samples) != 0 {
		t.Fatalf("samples = %d, want 0: an upper bound is not a measurement", len(set.Samples))
	}
	if set.Failed != 1 {
		t.Errorf("failed = %d, want 1", set.Failed)
	}
	if set.Detail == "" {
		t.Error("an upper bound must be explained rather than silently dropped")
	}
}

func TestParseTraceOutputReadsWindowsTracert(t *testing.T) {
	output := "" +
		"Tracing route to example.com [93.184.216.34] over a maximum of 30 hops:\n" +
		"\n" +
		"  1    <1 ms    <1 ms    <1 ms   192.168.1.1\n" +
		"  2     3 ms     2 ms     2 ms   10.0.0.1\n" +
		"  3    11 ms    10 ms    11 ms   93.184.216.34\n" +
		"\n" +
		"Trace complete.\n"
	route := parseTraceOutput(output, 30)
	if len(route.Hops) != 3 {
		t.Fatalf("hops = %d, want 3: %+v", len(route.Hops), route.Hops)
	}
	first := route.Hops[0]
	if first.Address != "192.168.1.1" {
		t.Errorf("hop 1 address = %q, want 192.168.1.1", first.Address)
	}
	// "<1 ms" is an upper bound. The hop is known to have answered, so it is
	// responding, but no number is published: 1 would overstate and 0 would
	// understate.
	if first.State != HopResponding {
		t.Errorf("hop 1 state = %q, want %q", first.State, HopResponding)
	}
	if first.RttMs != nil {
		t.Errorf("hop 1 rtt = %v, want nil for an upper bound only", *first.RttMs)
	}
	if first.Responded != 3 {
		t.Errorf("hop 1 responded = %d, want 3", first.Responded)
	}
	if len(first.Notes) == 0 {
		t.Error("an excluded upper bound must be explained on the hop")
	}
	if first.Probes != 3 {
		t.Errorf("hop 1 probes = %d, want 3", first.Probes)
	}
	last := route.Hops[2]
	if last.Address != "93.184.216.34" {
		t.Errorf("hop 3 address = %q, want the target", last.Address)
	}
}

func TestParseTraceOutputKeepsTimedOutHops(t *testing.T) {
	// A star is a timeout. Dropping the hop would shift every later index and
	// make the route look shorter than it is.
	output := "" +
		"  1    <1 ms    <1 ms    <1 ms   192.168.1.1\n" +
		"  2     *        *        *     Request timed out.\n" +
		"  3    11 ms    10 ms    11 ms   93.184.216.34\n"
	route := parseTraceOutput(output, 30)
	if len(route.Hops) != 3 {
		t.Fatalf("hops = %d, want 3: a timeout hop must not be dropped", len(route.Hops))
	}
	if route.Hops[1].Index != 2 {
		t.Errorf("second hop index = %d, want 2", route.Hops[1].Index)
	}
	if route.Hops[1].State != HopTimeout {
		t.Errorf("second hop state = %q, want %q", route.Hops[1].State, HopTimeout)
	}
	if route.Hops[1].RttMs != nil {
		t.Errorf("a timed out hop must not carry an rtt, got %v", *route.Hops[1].RttMs)
	}
	if route.Hops[1].Address != Unknown {
		t.Errorf("a timed out hop address = %q, want %q", route.Hops[1].Address, Unknown)
	}
	if route.Hops[2].Index != 3 {
		t.Errorf("third hop index = %d, want 3: the timeout must not shift the numbering", route.Hops[2].Index)
	}
}

func TestParseTraceOutputReadsUnixTraceroute(t *testing.T) {
	output := "" +
		"traceroute to example.com (93.184.216.34), 30 hops max, 60 byte packets\n" +
		" 1  gateway (192.168.1.1)  0.412 ms  0.301 ms  0.288 ms\n" +
		" 2  10.0.0.1 (10.0.0.1)  3.104 ms  2.998 ms  3.011 ms\n" +
		" 3  * * *\n" +
		" 4  93.184.216.34 (93.184.216.34)  11.204 ms  11.098 ms  11.190 ms\n"
	route := parseTraceOutput(output, 30)
	if len(route.Hops) != 4 {
		t.Fatalf("hops = %d, want 4", len(route.Hops))
	}
	if route.Hops[0].Address != "192.168.1.1" {
		t.Errorf("hop 1 = %q", route.Hops[0].Address)
	}
	if got := route.Hops[0].Hostnames; len(got) != 1 || got[0] != "gateway" {
		t.Errorf("hop 1 hostnames = %v, want [gateway]", got)
	}
	if route.Hops[2].State != HopTimeout {
		t.Errorf("hop 3 state = %q, want timeout", route.Hops[2].State)
	}
	// Every probe is recorded, and the reported rtt is the best of them.
	last := route.Hops[3]
	if len(last.RttMsAll) != 3 {
		t.Fatalf("hop 4 samples = %d, want 3: %v", len(last.RttMsAll), last.RttMsAll)
	}
	if last.RttMs == nil || *last.RttMs != 11.098 {
		t.Errorf("hop 4 best rtt = %v, want 11.098", last.RttMs)
	}
	if last.Responded != 3 {
		t.Errorf("hop 4 responded = %d, want 3", last.Responded)
	}
}

func TestParseTraceOutputKeepsEveryRespondingAddress(t *testing.T) {
	output := "" +
		"  1  10.0.0.1 (10.0.0.1)  1.0 ms  10.0.0.2 (10.0.0.2)  1.5 ms\n"
	route := parseTraceOutput(output, 30)
	hop := route.Hops[0]
	if len(hop.Addresses) != 2 {
		t.Fatalf("addresses = %v, want both responding addresses", hop.Addresses)
	}
	if hop.Address != "10.0.0.1" {
		t.Errorf("primary address = %q, want the first responder", hop.Address)
	}
}

func TestParseTraceOutputLeavesEveryFieldDefined(t *testing.T) {
	// A consumer must never have to interpret a missing field as "unparsed".
	route := parseTraceOutput("  1  192.168.1.1  1.0 ms\n", 30)
	hop := route.Hops[0]
	if hop.State == "" || hop.Address == "" {
		t.Errorf("hop fields left empty: %+v", hop)
	}
	if hop.Addresses == nil || hop.RttMsAll == nil {
		t.Errorf("hop slices left nil: %+v", hop)
	}
}

func TestParseTraceOutputReadsBareWindowsIntegers(t *testing.T) {
	// tracert prints "12 11 13" with the unit only in the header, so all three
	// must be read rather than the last one before the address.
	output := "  3    12     11     13   93.184.216.34\n"
	route := parseTraceOutput(output, 30)
	hop := route.Hops[0]
	if len(hop.RttMsAll) != 3 {
		t.Fatalf("samples = %d (%v), want 3", len(hop.RttMsAll), hop.RttMsAll)
	}
	if hop.RttMs == nil || *hop.RttMs != 11 {
		t.Errorf("best rtt = %v, want 11", hop.RttMs)
	}
}

func TestParseTraceOutputDoesNotPublishLatencyColumnAsHostname(t *testing.T) {
	// "0.412" is a millisecond column, not a hop name. Publishing it as a
	// hostname would turn a latency reading into a claimed identity.
	output := " 1  192.168.1.1  0.412 ms  0.301 ms\n"
	route := parseTraceOutput(output, 30)
	if len(route.Hops[0].Hostnames) != 0 {
		t.Errorf("hostnames = %v, want none", route.Hops[0].Hostnames)
	}
}
