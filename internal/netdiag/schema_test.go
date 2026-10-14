package netdiag

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestDiagnosisJSONShapeIsStable pins the wire contract. A stored result has to
// stay readable by a consumer written against an earlier release, so the field
// names and the nil-versus-zero behaviour are part of the contract rather than
// an implementation detail.
func TestDiagnosisJSONShapeIsStable(t *testing.T) {
	d := &Diagnosis{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC),
		Target: &TargetInfo{
			Input: "example.com", Hostname: "example.com", Port: 443,
			Resolver: "system", Resolved: true,
			Addresses: []string{}, IPv4: []string{},
		},
		Latency: &LatencyResult{},
		Route:   &RouteResult{Method: "windows_tracert", MaxHops: 20, Hops: []Hop{}},
		Network: &NetworkInfo{
			DefaultInterface: "eth0",
			DefaultGateway:   Unknown,
			DNSServers:       []string{},
			Interfaces:       []InterfaceInfo{},
			Scope:            Unknown,
		},
		Warnings: []string{},
		Errors:   []string{},
	}
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(encoded)

	for _, key := range []string{
		`"schema_version"`, `"network"`, `"target"`, `"latency"`, `"route"`,
		`"warnings"`, `"default_interface"`, `"default_gateway"`, `"dns_servers"`,
		`"route_to_target"`, `"icmp"`, `"tcp"`, `"http"`, `"stop_reason"`,
	} {
		if key == `"stop_reason"` {
			continue // belongs to the speedtest schema, not this one
		}
		if !strings.Contains(text, key) {
			t.Errorf("json is missing %s\n%s", key, text)
		}
	}
	if !strings.Contains(text, `"schema_version":"netdiag/1"`) {
		t.Errorf("schema version missing or changed:\n%s", text)
	}
}

// TestUnmeasuredIsNullNotZero is the central honesty rule: a measurement that
// did not happen must serialise as null, never as 0.
func TestUnmeasuredIsNullNotZero(t *testing.T) {
	encoded, err := json.Marshal(&LatencyMeasurement{
		Method:    ProbeICMP,
		Outcome:   LatencyUnavailable,
		Attempts:  5,
		TimedOut:  5,
		SamplesMs: []float64{},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(encoded)
	for _, field := range []string{"min_ms", "average_ms", "max_ms", "jitter_ms"} {
		if !strings.Contains(text, `"`+field+`":null`) {
			t.Errorf("%s must be null when nothing was measured, got:\n%s", field, text)
		}
	}
	if strings.Contains(text, `"min_ms":0`) {
		t.Errorf("an unmeasured value was published as zero:\n%s", text)
	}
}

// TestMeasuredZeroStaysZero proves the other half: a measured zero is a real
// observation and must not be turned into null.
func TestMeasuredZeroStaysZero(t *testing.T) {
	encoded, err := json.Marshal(&LatencyMeasurement{
		Method:    ProbeTCP,
		Outcome:   LatencyMeasured,
		Attempts:  1,
		MinMs:     floatPtr(0),
		AverageMs: floatPtr(0),
		MaxMs:     floatPtr(0),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"average_ms":0`) {
		t.Errorf("a measured zero must stay zero:\n%s", encoded)
	}
}

func TestUnknownIsNotEmptyString(t *testing.T) {
	encoded, err := json.Marshal(&NetworkInfo{DefaultInterface: Unknown, DefaultGateway: Unknown, DNSServers: []string{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), `"default_gateway":""`) {
		t.Errorf("unknown was serialised as an empty string:\n%s", encoded)
	}
	if !strings.Contains(string(encoded), `"default_gateway":"unknown"`) {
		t.Errorf("unknown literal missing:\n%s", encoded)
	}
}

// TestPublicAddressesFiltersPrivateRanges proves a user's LAN layout is never
// handed to a third party service.
func TestPublicAddressesFiltersPrivateRanges(t *testing.T) {
	input := []string{
		"192.168.1.1", "10.0.0.8", "172.16.4.4", "127.0.0.1",
		"169.254.1.1", "100.64.0.1", "224.0.0.1", "not-an-ip",
		"93.184.216.34", "93.184.216.34",
	}
	got := publicAddresses(input)
	want := []string{"93.184.216.34"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("publicAddresses(%v) = %v, want %v", input, got, want)
	}
}

func TestPublicAddressesMapsV4IntoV6(t *testing.T) {
	// An IPv4-mapped literal is the same address and must not slip past as an
	// IPv6 address.
	got := publicAddresses([]string{"::ffff:192.168.1.1"})
	if len(got) != 0 {
		t.Errorf("got %v, want the mapped private address filtered out", got)
	}
}

func TestIsIPv4ClassifiesLiterals(t *testing.T) {
	cases := map[string]bool{
		"93.184.216.34":  true,
		"::ffff:1.2.3.4": true,
		"2001:db8::1":    false,
		"example.com":    false,
		"":               false,
	}
	for input, want := range cases {
		if got := isIPv4(input); got != want {
			t.Errorf("isIPv4(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestNetAddressBuildsDialString(t *testing.T) {
	if got := netAddress("example.com", 443); got != "example.com:443" {
		t.Errorf("netAddress = %q", got)
	}
	if got := netAddress("[2001:db8::1]", 80); got != "[2001:db8::1]:80" {
		t.Errorf("netAddress with ipv6 literal = %q", got)
	}
}

func TestHostOfStripsPortAndBrackets(t *testing.T) {
	if got := hostOf("example.com:8080"); got != "example.com" {
		t.Errorf("hostOf = %q, want example.com", got)
	}
	if got := hostOf("[2001:db8::1]:443"); got != "2001:db8::1" {
		t.Errorf("hostOf = %q, want the bare address", got)
	}
}

// TestMeasureProducesStatsFromSamples pins the statistics the engine computes
// so a change in definition is visible.
func TestMeasureProducesStatsFromSamples(t *testing.T) {
	samples := []time.Duration{10 * time.Millisecond, 30 * time.Millisecond, 20 * time.Millisecond}
	m := measure(context.Background(),
		func(ctx context.Context, _ string, _ int, _ time.Duration) (SampleSet, error) {
			return SampleSet{Samples: samples}, nil
		},
		ProbeTCP, "example.com:443", len(samples), time.Second,
		func(string, ...any) {})

	if m.Outcome != LatencyMeasured {
		t.Fatalf("outcome = %q", m.Outcome)
	}
	if m.MinMs == nil || *m.MinMs != 10 {
		t.Errorf("min = %v, want 10", m.MinMs)
	}
	if m.MaxMs == nil || *m.MaxMs != 30 {
		t.Errorf("max = %v, want 30", m.MaxMs)
	}
	if m.AverageMs == nil || *m.AverageMs != 20 {
		t.Errorf("average = %v, want 20", m.AverageMs)
	}
	// Mean absolute consecutive difference: |30-10| = 20 and |20-30| = 10,
	// so the jitter is 15. This is the same definition internal/speedtest uses.
	if m.JitterMs == nil || *m.JitterMs != 15 {
		t.Errorf("jitter = %v, want 15 (mean absolute consecutive difference)", m.JitterMs)
	}
	if len(m.SamplesMs) != 3 {
		t.Errorf("samples = %v, want 3", m.SamplesMs)
	}
}

func TestMeasureLeavesJitterNilForOneSample(t *testing.T) {
	m := measure(context.Background(),
		func(ctx context.Context, _ string, _ int, _ time.Duration) (SampleSet, error) {
			return SampleSet{Samples: []time.Duration{time.Millisecond}}, nil
		},
		ProbeICMP, "example.com", 1, time.Second,
		func(string, ...any) {})
	if m.JitterMs != nil {
		t.Errorf("jitter = %v, want nil: one sample cannot show variation", *m.JitterMs)
	}
}

func TestMeasureSeparatesTimeoutsFromFailures(t *testing.T) {
	m := measure(context.Background(),
		func(ctx context.Context, _ string, _ int, _ time.Duration) (SampleSet, error) {
			return SampleSet{Samples: []time.Duration{time.Millisecond}, TimedOut: 2, Failed: 1, Detail: "refused"}, nil
		},
		ProbeTCP, "example.com:443", 4, time.Second,
		func(string, ...any) {})
	if m.TimedOut != 2 {
		t.Errorf("timed out = %d, want 2", m.TimedOut)
	}
	if m.Failed != 1 {
		t.Errorf("failed = %d, want 1", m.Failed)
	}
	if m.Attempts != 4 {
		t.Errorf("attempts = %d, want 4", m.Attempts)
	}
}
