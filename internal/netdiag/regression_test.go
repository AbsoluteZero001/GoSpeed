package netdiag

import (
	"encoding/json"
	"strings"
	"testing"
)

// These are the NET-01-R1 regressions. Each one failed against the real
// Windows implementation before the fix.

// TestPublicTargetIsNotClassifiedAsLAN is the scope defect: a machine on
// 192.168.1.111 measuring 1.1.1.1 is measuring a remote path, and calling it
// "lan" describes the user's own network instead of the one being measured.
func TestPublicTargetIsNotClassifiedAsLAN(t *testing.T) {
	cases := []struct {
		source      string
		destination string
		wantScope   string
		wantLocal   string
	}{
		{"192.168.1.111", "1.1.1.1", ScopeRemote, ScopeLAN},
		{"192.168.1.111", "8.8.8.8", ScopeRemote, ScopeLAN},
		{"10.8.0.5", "93.184.216.34", ScopeRemote, ScopeLAN},
		{"127.0.0.1", "127.0.0.1", ScopeLocal, ScopeLocal},
		{"192.168.1.111", "192.168.1.50", ScopeLAN, ScopeLAN},
		{"203.0.113.9", "203.0.113.10", ScopeRemote, ScopeRemote},
		{"unknown", "unknown", ScopeUnknown, ScopeUnknown},
	}
	for _, c := range cases {
		selection := &RouteSelection{
			Destination:  c.destination,
			LocalAddress: c.source,
			Interface:    "eth0",
			Method:       "udp_route_lookup",
		}
		if got := classifyAddress(selection.Destination); got != c.wantScope {
			t.Errorf("scope for destination %s = %q, want %q", c.destination, got, c.wantScope)
		}
		if got := classifyAddress(selection.LocalAddress); got != c.wantLocal {
			t.Errorf("local scope for source %s = %q, want %q", c.source, got, c.wantLocal)
		}
	}
}

func TestClassifyAddressKeepsUnparsableInputUnknown(t *testing.T) {
	for _, value := range []string{"", "not-an-address", Unknown, "999.1.1.1"} {
		if got := classifyAddress(value); got != ScopeUnknown {
			t.Errorf("classifyAddress(%q) = %q, want %q", value, got, ScopeUnknown)
		}
	}
}

// TestDiagnoseScopesRemoteTargetFromAPrivateSource drives the whole pipeline
// with the exact values from the reported Windows run.
func TestDiagnoseScopesRemoteTargetFromAPrivateSource(t *testing.T) {
	options := baseOptions("1.1.1.1")
	options.Prober = &fakeProber{}
	options.Route = &fakeRoute{route: &RouteResult{Hops: []Hop{}}}
	options.Resolver = fakeResolver{answers: []string{"1.1.1.1"}}
	options.Inspector = fakeInspector{info: &NetworkInfo{
		DefaultInterface: "WLAN",
		DefaultGateway:   "192.168.1.1",
		DNSServers:       []string{"119.6.6.6"},
		Scope:            ScopeRemote,
		LocalScope:       ScopeLAN,
		RouteToTarget: &RouteSelection{
			Destination:  "1.1.1.1",
			LocalAddress: "192.168.1.111",
			Interface:    "WLAN",
			Method:       "udp_route_lookup",
		},
	}}

	result, err := Diagnose(t.Context(), options)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if result.Network.Scope != ScopeRemote {
		t.Errorf("scope = %q, want %q: a public target is not a LAN measurement", result.Network.Scope, ScopeRemote)
	}
	if result.Network.LocalScope != ScopeLAN {
		t.Errorf("local scope = %q, want %q: the source really is on a LAN", result.Network.LocalScope, ScopeLAN)
	}
}

// TestNetworkScopeAndLocalScopeAreSeparateJSONFields proves the two answers
// survive a round trip and cannot be conflated by a consumer.
func TestNetworkScopeAndLocalScopeAreSeparateJSONFields(t *testing.T) {
	encoded, err := json.Marshal(&NetworkInfo{
		DefaultInterface: "WLAN",
		DefaultGateway:   "192.168.1.1",
		DNSServers:       []string{},
		Scope:            ScopeRemote,
		LocalScope:       ScopeLAN,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(encoded)
	if !strings.Contains(text, `"scope":"remote"`) {
		t.Errorf("target scope missing:\n%s", text)
	}
	if !strings.Contains(text, `"local_scope":"lan"`) {
		t.Errorf("local scope missing:\n%s", text)
	}
}

// TestTracerouteDoesNotTurnTimeoutProseIntoHostnames is the parser defect:
// tracert prints "Request timed out." for an unanswered hop and that prose must
// never be published as three host names.
func TestTracerouteDoesNotTurnTimeoutProseIntoHostnames(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{
			name: "english timeout prose",
			line: "  2     *        *        *     Request timed out.",
		},
		{
			name: "english unreachable prose",
			line: "  3     *        *        *     Request timed out.",
		},
		{
			name: "simplified chinese timeout prose",
			line: "  4     *        *        *     请求超时。",
		},
		{
			name: "chinese unreachable prose",
			line: "  5     *        *        *     不可达。",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			route := parseTraceOutput(c.line, 30)
			if len(route.Hops) != 1 {
				t.Fatalf("hops = %d, want 1", len(route.Hops))
			}
			if got := route.Hops[0].Hostnames; len(got) != 0 {
				t.Errorf("hostnames = %q, want none: traceroute prose is not a hop identity", got)
			}
			if route.Hops[0].State != HopTimeout {
				t.Errorf("state = %q, want %q", route.Hops[0].State, HopTimeout)
			}
		})
	}
}

// TestTracerouteKeepsRealHostnames proves the fix did not simply delete names.
func TestTracerouteKeepsRealHostnames(t *testing.T) {
	// Unix form: the name sits immediately before its parenthesised address.
	unix := parseTraceOutput(" 1  gateway (192.168.1.1)  0.412 ms  0.301 ms\n", 30)
	if got := unix.Hops[0].Hostnames; len(got) != 1 || got[0] != "gateway" {
		t.Errorf("unix hostnames = %v, want [gateway]", got)
	}

	// Windows form: the name is the final field of the line.
	windows := parseTraceOutput("  2    10 ms    11 ms    12 ms   edge.example.net\n", 30)
	if got := windows.Hops[0].Hostnames; len(got) != 1 || got[0] != "edge.example.net" {
		t.Errorf("windows hostnames = %v, want [edge.example.net]", got)
	}
}

// TestTracerouteRejectsMessageWordsAnywhere guards the denylist on its own, in
// case a future layout change moves a prose word into a credible position.
func TestTracerouteRejectsMessageWordsAnywhere(t *testing.T) {
	for _, word := range []string{"Request", "timed", "out", "unreachable", "请求", "超时", "不可达"} {
		if got := plausibleHostname(word); got != "" {
			t.Errorf("plausibleHostname(%q) = %q, want it rejected", word, got)
		}
	}
}
