package netdiag

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The fakes below stand in for the network so every assertion is deterministic
// and none of them touches a real host.

// fakeProber returns a scripted SampleSet per method and records what it was
// asked for.
type fakeProber struct {
	icmp      SampleSet
	tcp       SampleSet
	http      SampleSet
	err       error
	calls     []string
	cancelled bool
}

func (f *fakeProber) ICMP(ctx context.Context, host string, samples int, timeout time.Duration) (SampleSet, error) {
	f.calls = append(f.calls, "icmp:"+host)
	if ctx.Err() != nil {
		f.cancelled = true
	}
	return f.icmp, f.err
}

func (f *fakeProber) TCP(ctx context.Context, address string, samples int, timeout time.Duration) (SampleSet, error) {
	f.calls = append(f.calls, "tcp:"+address)
	return f.tcp, f.err
}

func (f *fakeProber) HTTP(ctx context.Context, url string, samples int, timeout time.Duration) (SampleSet, error) {
	f.calls = append(f.calls, "http:"+url)
	return f.http, f.err
}

// fakeResolver returns a fixed answer or a fixed failure.
type fakeResolver struct {
	answers []string
	err     error
}

func (f fakeResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	return f.answers, f.err
}

// fakeInspector returns a fixed network description.
type fakeInspector struct {
	info *NetworkInfo
	err  error
}

func (f fakeInspector) Inspect(ctx context.Context, target string) (*NetworkInfo, error) {
	return f.info, f.err
}

// fakeRoute returns a scripted route.
type fakeRoute struct {
	route *RouteResult
	err   error
	calls int
}

func (f *fakeRoute) Trace(ctx context.Context, target string, maxHops int, timeout time.Duration, resolve bool) (*RouteResult, error) {
	f.calls++
	return f.route, f.err
}

// baseOptions returns options with every measurement switched on and every
// seam filled in, so a test only has to change the one thing it is about.
func baseOptions(target string) Options {
	return Options{
		Target:     target,
		Port:       443,
		Samples:    3,
		Timeout:    time.Second,
		MaxHops:    10,
		ICMP:       true,
		TCP:        true,
		HTTP:       true,
		Traceroute: true,
		Resolver:   fakeResolver{answers: []string{"93.184.216.34"}},
		Inspector: fakeInspector{info: &NetworkInfo{
			DefaultInterface: "eth0",
			DefaultGateway:   "192.168.1.1",
			DNSServers:       []string{"192.168.1.1"},
			Scope:            "remote",
		}},
	}
}

func TestDiagnoseKeepsLatencyMethodsSeparate(t *testing.T) {
	prober := &fakeProber{
		icmp: SampleSet{Samples: []time.Duration{10 * time.Millisecond, 12 * time.Millisecond, 14 * time.Millisecond}},
		tcp:  SampleSet{Samples: []time.Duration{30 * time.Millisecond}},
		http: SampleSet{Samples: []time.Duration{50 * time.Millisecond}},
	}
	options := baseOptions("example.com")
	options.Prober = prober
	options.Route = &fakeRoute{route: &RouteResult{Hops: []Hop{}}}

	result, err := Diagnose(context.Background(), options)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	icmp, tcp, httpResult := result.Latency.ICMP, result.Latency.TCP, result.Latency.HTTP
	if icmp.AverageMs == nil || *icmp.AverageMs != 12 {
		t.Errorf("icmp average = %v, want 12", icmp.AverageMs)
	}
	if tcp.AverageMs == nil || *tcp.AverageMs != 30 {
		t.Errorf("tcp average = %v, want 30", tcp.AverageMs)
	}
	if httpResult.AverageMs == nil || *httpResult.AverageMs != 50 {
		t.Errorf("http average = %v, want 50", httpResult.AverageMs)
	}
	// The three numbers are never merged into one.
	if icmp.MinMs == nil || *icmp.MinMs != 10 {
		t.Errorf("icmp min = %v, want 10", icmp.MinMs)
	}
	if icmp.MaxMs == nil || *icmp.MaxMs != 14 {
		t.Errorf("icmp max = %v, want 14", icmp.MaxMs)
	}
	if icmp.JitterMs == nil || *icmp.JitterMs != 2 {
		t.Errorf("icmp jitter = %v, want 2", icmp.JitterMs)
	}
	// One sample cannot produce a jitter claim.
	if tcp.JitterMs != nil {
		t.Errorf("tcp jitter = %v, want nil for a single sample", *tcp.JitterMs)
	}
	if icmp.Method != ProbeICMP || tcp.Method != ProbeTCP || httpResult.Method != ProbeHTTP {
		t.Errorf("methods were not kept distinct: %+v", result.Latency)
	}
}

func TestDiagnoseReportsMethodThatProducedNothing(t *testing.T) {
	prober := &fakeProber{
		icmp: SampleSet{TimedOut: 3, Detail: "no reply"},
		tcp:  SampleSet{Samples: []time.Duration{5 * time.Millisecond}},
	}
	options := baseOptions("example.com")
	options.Prober = prober
	options.HTTP = false
	options.Route = &fakeRoute{route: &RouteResult{Hops: []Hop{}}}

	result, err := Diagnose(context.Background(), options)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if result.Latency.ICMP.Outcome != LatencyUnavailable {
		t.Errorf("icmp outcome = %q, want %q", result.Latency.ICMP.Outcome, LatencyUnavailable)
	}
	// Every numeric field stays nil: a method that measured nothing must not
	// look like a method that measured zero.
	if result.Latency.ICMP.AverageMs != nil {
		t.Errorf("icmp average = %v, want nil", *result.Latency.ICMP.AverageMs)
	}
	if result.Latency.ICMP.MinMs != nil || result.Latency.ICMP.MaxMs != nil {
		t.Error("icmp min/max must stay nil when nothing was measured")
	}
	if result.Latency.ICMP.TimedOut != 3 {
		t.Errorf("icmp timed out = %d, want 3", result.Latency.ICMP.TimedOut)
	}
	// The other method is unaffected.
	if result.Latency.TCP.Outcome != LatencyMeasured {
		t.Errorf("tcp outcome = %q, want measured", result.Latency.TCP.Outcome)
	}
}

func TestDiagnoseDoesNotTurnICMPTimeoutIntoPacketLoss(t *testing.T) {
	prober := &fakeProber{icmp: SampleSet{TimedOut: 5, Detail: "no reply"}, tcp: SampleSet{}}
	options := baseOptions("example.com")
	options.Prober = prober
	options.HTTP = false
	options.Traceroute = false

	result, err := Diagnose(context.Background(), options)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	// There is no packet loss field anywhere in the result, by construction.
	// The only thing an ICMP timeout may say is that ICMP probes went unanswered.
	if result.Latency.ICMP.Failed != 0 {
		t.Errorf("failed = %d, want 0", result.Latency.ICMP.Failed)
	}
	if result.Latency.TCP.Outcome != LatencyUnavailable {
		t.Errorf("tcp outcome = %q, want unavailable", result.Latency.TCP.Outcome)
	}
}

func TestDiagnoseKeepsDefaultInterfaceSeparateFromRouteToTarget(t *testing.T) {
	options := baseOptions("example.com")
	options.Prober = &fakeProber{}
	options.Route = &fakeRoute{route: &RouteResult{Hops: []Hop{}}}
	options.Inspector = fakeInspector{info: &NetworkInfo{
		DefaultInterface: "eth0",
		DefaultGateway:   "192.168.1.1",
		DNSServers:       []string{"192.168.1.1"},
		Scope:            "lan",
		RouteToTarget: &RouteSelection{
			Destination:  "10.8.0.2",
			LocalAddress: "10.8.0.5",
			Interface:    "tun0",
			Method:       "udp_route_lookup",
		},
	}}

	result, err := Diagnose(context.Background(), options)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if result.Network.DefaultInterface != "eth0" {
		t.Errorf("default interface = %q, want eth0", result.Network.DefaultInterface)
	}
	selection := result.Network.RouteToTarget
	if selection == nil {
		t.Fatal("route to target is missing")
	}
	// The two answers disagree, which is exactly the case a VPN creates, and
	// neither may overwrite the other.
	if selection.Interface != "tun0" || selection.LocalAddress != "10.8.0.5" {
		t.Errorf("route to target = %+v, want the tun0 selection", selection)
	}
}

func TestDiagnoseMarksUnmeasurableNetworkFieldsUnknown(t *testing.T) {
	options := baseOptions("example.com")
	options.Prober = &fakeProber{}
	options.Route = &fakeRoute{route: &RouteResult{Hops: []Hop{}}}
	options.Inspector = fakeInspector{info: &NetworkInfo{}}

	result, err := Diagnose(context.Background(), options)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if result.Network.DefaultInterface != Unknown {
		t.Errorf("default interface = %q, want %q", result.Network.DefaultInterface, Unknown)
	}
	if result.Network.DefaultGateway != Unknown {
		t.Errorf("default gateway = %q, want %q", result.Network.DefaultGateway, Unknown)
	}
	// Unknown must not be an empty string, because an empty field and an
	// unknown field are indistinguishable after a JSON round trip.
	if result.Network.DefaultGateway == "" {
		t.Error("unknown must not be an empty string")
	}
}

func TestDiagnoseReportsDNSFailureWithoutInventingAddresses(t *testing.T) {
	options := baseOptions("nonexistent.invalid")
	options.Prober = &fakeProber{}
	options.Route = &fakeRoute{route: &RouteResult{Hops: []Hop{}}}
	options.Resolver = fakeResolver{err: errors.New("no such host")}

	result, err := Diagnose(context.Background(), options)
	if err != nil {
		t.Fatalf("Diagnose returned an error instead of a partial result: %v", err)
	}
	if result.Target.Resolved {
		t.Error("target must not be reported as resolved")
	}
	if len(result.Target.Addresses) != 0 {
		t.Errorf("addresses = %v, want none", result.Target.Addresses)
	}
	if result.Target.Detail == "" {
		t.Error("a resolution failure must leave a detail")
	}
	if len(result.Errors) == 0 {
		t.Error("the failure must be recorded in Errors")
	}
}

func TestDiagnoseKeepsMultiHopRouteAndMarksTargetReached(t *testing.T) {
	route := &RouteResult{Hops: []Hop{
		{Index: 1, TTL: 1, Address: "192.168.1.1", Addresses: []string{"192.168.1.1"}, State: HopResponding, RttMs: floatPtr(0.4)},
		{Index: 2, TTL: 2, Address: Unknown, Addresses: []string{}, State: HopTimeout},
		{Index: 3, TTL: 3, Address: "93.184.216.34", Addresses: []string{"93.184.216.34"}, State: HopResponding, RttMs: floatPtr(11.2)},
	}}
	options := baseOptions("example.com")
	options.Prober = &fakeProber{}
	options.Route = &fakeRoute{route: route}

	result, err := Diagnose(context.Background(), options)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(result.Route.Hops) != 3 {
		t.Fatalf("hops = %d, want 3", len(result.Route.Hops))
	}
	if result.Route.Hops[1].State != HopTimeout {
		t.Errorf("hop 2 state = %q, want timeout", result.Route.Hops[1].State)
	}
	if !result.Route.TargetReached {
		t.Error("the final hop answered with a target address, so the target was reached")
	}
}

func TestDiagnoseDoesNotClaimTargetReachedWhenItWasNot(t *testing.T) {
	route := &RouteResult{Hops: []Hop{
		{Index: 1, TTL: 1, Address: "192.168.1.1", Addresses: []string{"192.168.1.1"}, State: HopResponding, RttMs: floatPtr(0.4)},
	}}
	options := baseOptions("example.com")
	options.Prober = &fakeProber{}
	options.Route = &fakeRoute{route: route}

	result, err := Diagnose(context.Background(), options)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if result.Route.TargetReached {
		t.Error("an intermediate hop answering is not the target being reached")
	}
}

func TestDiagnoseHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	options := baseOptions("example.com")
	options.Prober = &fakeProber{err: context.Canceled}
	options.Route = &fakeRoute{route: &RouteResult{Hops: []Hop{}}}

	_, err := Diagnose(ctx, options)
	if err == nil {
		t.Fatal("a cancelled run must return an error")
	}
	if !errors.Is(err, ErrCancelled) {
		t.Errorf("error = %v, want it to wrap ErrCancelled", err)
	}
}

func TestDiagnoseRequiresATarget(t *testing.T) {
	_, err := Diagnose(context.Background(), Options{})
	if !errors.Is(err, ErrInvalidOptions) {
		t.Errorf("error = %v, want ErrInvalidOptions", err)
	}
}

func TestDiagnoseSurvivesEnrichmentFailure(t *testing.T) {
	options := baseOptions("example.com")
	options.Prober = &fakeProber{}
	options.Route = &fakeRoute{route: &RouteResult{Hops: []Hop{}}}
	options.Enrich = true
	options.ASN = StaticEnricher{Err: errors.New("the data provider is unreachable")}

	result, err := Diagnose(context.Background(), options)
	if err != nil {
		t.Fatalf("a failed enrichment must not fail the diagnosis: %v", err)
	}
	// The measurements survive untouched.
	if result.Latency.ICMP == nil {
		t.Fatal("latency was dropped because enrichment failed")
	}
	if len(result.Errors) == 0 {
		t.Error("the enrichment failure must still be reported")
	}
	if len(result.Enrichment) != 0 {
		t.Errorf("enrichment = %+v, want none after a failure", result.Enrichment)
	}
}

func TestDiagnoseNeverQueriesPrivateAddressesForEnrichment(t *testing.T) {
	var asked []string
	options := baseOptions("example.com")
	options.Prober = &fakeProber{}
	options.Route = &fakeRoute{route: &RouteResult{Hops: []Hop{
		{Index: 1, TTL: 1, Address: "192.168.1.1", Addresses: []string{"192.168.1.1"}, State: HopResponding},
		{Index: 2, TTL: 2, Address: "10.0.0.8", Addresses: []string{"10.0.0.8"}, State: HopResponding},
	}}}
	options.Resolver = fakeResolver{answers: []string{"192.168.1.50"}}
	options.Enrich = true
	options.ASN = recordingEnricher{asked: &asked}

	result, err := Diagnose(context.Background(), options)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(asked) != 0 {
		t.Errorf("enrichment queried %v, want nothing: those addresses are not public", asked)
	}
	if len(result.Warnings) == 0 {
		t.Error("skipping enrichment should leave a warning so the reader knows why")
	}
}

// recordingEnricher records the addresses it was asked about.
type recordingEnricher struct{ asked *[]string }

func (r recordingEnricher) Enrich(ctx context.Context, addresses []string) ([]*ASInfo, error) {
	*r.asked = append(*r.asked, addresses...)
	return nil, nil
}

func TestDiagnoseOnlyQueriesPublicAddresses(t *testing.T) {
	var asked []string
	options := baseOptions("example.com")
	options.Prober = &fakeProber{}
	options.Route = &fakeRoute{route: &RouteResult{Hops: []Hop{
		{Index: 1, TTL: 1, Address: "192.168.1.1", Addresses: []string{"192.168.1.1"}, State: HopResponding},
		{Index: 2, TTL: 2, Address: "93.184.216.34", Addresses: []string{"93.184.216.34"}, State: HopResponding},
	}}}
	options.Resolver = fakeResolver{answers: []string{"93.184.216.34"}}
	options.Enrich = true
	options.ASN = recordingEnricher{asked: &asked}

	if _, err := Diagnose(context.Background(), options); err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(asked) != 1 || asked[0] != "93.184.216.34" {
		t.Errorf("queried = %v, want only the public address", asked)
	}
}

func TestDiagnoseKeepsPartialRouteOnRunnerError(t *testing.T) {
	options := baseOptions("example.com")
	options.Prober = &fakeProber{}
	options.Route = &fakeRoute{
		route: &RouteResult{Hops: []Hop{{Index: 1, TTL: 1, Address: "192.168.1.1", Addresses: []string{"192.168.1.1"}, State: HopResponding}}},
		err:   errors.New("the traceroute program exited with status 1"),
	}
	result, err := Diagnose(context.Background(), options)
	if err != nil {
		t.Fatalf("a partial route with a runner error must still return a result: %v", err)
	}
	if len(result.Route.Hops) != 1 {
		t.Errorf("hops = %d, want the one that was reported", len(result.Route.Hops))
	}
	if len(result.Route.Notes) == 0 {
		t.Error("the runner error must be recorded as a note on the route")
	}
}

func TestDiagnoseRecordsTracerouteFailure(t *testing.T) {
	options := baseOptions("example.com")
	options.Prober = &fakeProber{}
	options.Route = &fakeRoute{err: ErrToolUnavailable}
	result, err := Diagnose(context.Background(), options)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if result.Route == nil || result.Route.Hops == nil {
		t.Fatal("the route must exist with an empty hop list")
	}
	if len(result.Route.Hops) != 0 {
		t.Errorf("hops = %d, want none", len(result.Route.Hops))
	}
	if result.Route.Detail == "" {
		t.Error("a failed traceroute must leave a detail")
	}
	if len(result.Errors) == 0 {
		t.Error("the failure must be recorded in Errors")
	}
}
